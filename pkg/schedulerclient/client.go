/*
Copyright 2024 The HAMi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package schedulerclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// ClusterInfoResponse mirrors the scheduler's /cluster-info response payload.
type ClusterInfoResponse struct {
	Nodes     []NodeInfoResponse `json:"nodes"`
	TotalGPUs int                `json:"totalGPUs"`
	Healthy   bool               `json:"healthy"`
}

// NodeInfoResponse mirrors the per-node entry returned by the scheduler.
type NodeInfoResponse struct {
	NodeName    string               `json:"nodeName"`
	Labels      map[string]string    `json:"labels,omitempty"`
	Devices     []DeviceInfoResponse `json:"devices"`
	DeviceCount int                  `json:"deviceCount"`
	Healthy     bool                 `json:"healthy"`
}

// DeviceInfoResponse mirrors the per-device entry returned by the scheduler.
type DeviceInfoResponse struct {
	ID           string        `json:"id"`
	Index        uint          `json:"index"`
	Type         string        `json:"type"`
	DeviceVendor string        `json:"deviceVendor"`
	Count        int32         `json:"count"`
	Devmem       int32         `json:"devmem"`
	Devcore      int32         `json:"devcore"`
	Numa         int           `json:"numa"`
	Mode         string        `json:"mode"`
	Health       bool          `json:"health"`
	MIGProfiles  []interface{} `json:"migProfiles,omitempty"`
}

// NodeDeviceMatch mirrors the /find-gpu response payload.
type NodeDeviceMatch struct {
	NodeName     string             `json:"nodeName"`
	DeviceID     string             `json:"deviceId"`
	DeviceVendor string             `json:"deviceVendor"`
	DeviceType   string             `json:"deviceType"`
	DeviceInfo   DeviceInfoResponse `json:"deviceInfo"`
}

// NodeDetailResponse mirrors the /nodes/:nodeName response payload.
type NodeDetailResponse struct {
	NodeName string               `json:"nodeName"`
	Devices  []DeviceInfoResponse `json:"devices"`
}

// Client talks to the HAMi scheduler HTTP endpoints that expose cluster
// node and GPU information.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient returns a scheduler client pointing at the given base URL.
// For Prometheus metrics endpoint, use port 9395 (e.g. "http://hami-scheduler.kube-system.svc:9395").
// For legacy HTTP API endpoints (cluster-info, filter, bind), use port 8080.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// SetHTTPClient overrides the default http.Client (useful for tests or TLS).
func (c *Client) SetHTTPClient(hc *http.Client) {
	c.httpClient = hc
}

// BaseURL returns the current base URL of the client
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetClusterInfo retrieves the full cluster node and GPU inventory from the
// scheduler's /cluster-info endpoint.
func (c *Client) GetClusterInfo() (*ClusterInfoResponse, error) {
	var out ClusterInfoResponse
	if err := c.doGet("/cluster-info", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetClusterInfoFromMetrics retrieves cluster information by parsing the
// scheduler's Prometheus /metrics endpoint. This extracts node and GPU device
// information from hami_gpu_* metrics without relying on the /cluster-info REST API.
//
// IMPORTANT: The baseURL must point to the scheduler's Prometheus metrics port (default :9395),
// NOT the HTTP API port (:8080). Example: "http://hami-scheduler.hami.svc:9395"
func (c *Client) GetClusterInfoFromMetrics() (*ClusterInfoResponse, error) {
	metricsURL := c.baseURL + "/metrics"
	klog.V(2).InfoS("Scheduler client requesting Prometheus metrics endpoint",
		"url", metricsURL,
		"note", "Ensure this URL points to the metrics port (default 9395), not HTTP API port (8080)")

	resp, err := c.httpClient.Get(metricsURL)
	if err != nil {
		return nil, fmt.Errorf("metrics request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metrics endpoint returned status %d for %s", resp.StatusCode, metricsURL)
	}

	return parseMetricsToClusterInfo(resp.Body)
}

// parseMetricsToClusterInfo parses Prometheus text format metrics and extracts
// cluster node/GPU information from hami_node_gpu_overview metric lines.
func parseMetricsToClusterInfo(reader io.Reader) (*ClusterInfoResponse, error) {
	response := &ClusterInfoResponse{
		Nodes:     make([]NodeInfoResponse, 0),
		TotalGPUs: 0,
		Healthy:   true,
	}

	nodeMap := make(map[string]*NodeInfoResponse)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !strings.HasPrefix(line, "hami_node_gpu_overview{") {
			continue
		}

		nodeName, deviceInfo, err := parseOverviewMetricLine(line)
		if err != nil {
			klog.V(5).ErrorS(err, "Failed to parse overview metric line", "line", line)
			continue
		}
		if nodeName == "" || deviceInfo == nil {
			continue
		}

		nodeResp, exists := nodeMap[nodeName]
		if !exists {
			nodeResp = &NodeInfoResponse{
				NodeName:    nodeName,
				Labels:      make(map[string]string),
				Devices:     make([]DeviceInfoResponse, 0),
				DeviceCount: 0,
				Healthy:     true,
			}
			nodeMap[nodeName] = nodeResp
		}

		nodeResp.Devices = append(nodeResp.Devices, *deviceInfo)
		nodeResp.DeviceCount++
		response.TotalGPUs++

		if !deviceInfo.Health {
			nodeResp.Healthy = false
			response.Healthy = false
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading metrics response: %w", err)
	}

	for _, nodeResp := range nodeMap {
		response.Nodes = append(response.Nodes, *nodeResp)
	}

	klog.V(4).InfoS("Parsed cluster info from metrics",
		"nodeCount", len(response.Nodes),
		"totalGPUs", response.TotalGPUs,
		"healthy", response.Healthy)

	return response, nil
}

// parseOverviewMetricLine parses a single hami_node_gpu_overview metric line in the format:
// hami_node_gpu_overview{node="node1",device_uuid="GPU-abc123",device_index="0",device_cores="80",device_memory_limit="24576",device_type="A100"} 10737418240.000000
func parseOverviewMetricLine(line string) (string, *DeviceInfoResponse, error) {
	if !strings.HasPrefix(line, "hami_node_gpu_overview{") {
		return "", nil, fmt.Errorf("not an overview metric line")
	}

	bracketStart := strings.Index(line, "{")
	bracketEnd := strings.Index(line, "}")
	if bracketStart == -1 || bracketEnd == -1 || bracketEnd <= bracketStart {
		return "", nil, fmt.Errorf("invalid metric format: missing braces")
	}

	labelsPart := line[bracketStart+1 : bracketEnd]
	labels := parseMetricLabels(labelsPart)

	nodeName := labels["node"]
	deviceUUID := labels["device_uuid"]

	if nodeName == "" || deviceUUID == "" {
		return "", nil, nil
	}

	deviceIndexStr := labels["device_index"]
	deviceType := labels["device_type"]
	deviceMemoryLimitStr := labels["device_memory_limit"]

	deviceIndex, err := strconv.ParseUint(deviceIndexStr, 10, 32)
	if err != nil {
		klog.V(5).ErrorS(err, "Invalid device index", "value", deviceIndexStr)
		deviceIndex = 0
	}

	deviceMemoryLimit, err := strconv.ParseInt(deviceMemoryLimitStr, 10, 32)
	if err != nil {
		klog.V(5).ErrorS(err, "Invalid memory limit", "value", deviceMemoryLimitStr)
		deviceMemoryLimit = 0
	}

	deviceInfo := &DeviceInfoResponse{
		ID:           deviceUUID,
		Index:        uint(deviceIndex),
		Type:         deviceType,
		DeviceVendor: inferVendorFromType(deviceType),
		Count:        1,
		Devmem:       int32(deviceMemoryLimit),
		Devcore:      0,
		Numa:         0,
		Mode:         "",
		Health:       true,
	}

	return nodeName, deviceInfo, nil
}

// parseMetricLabels splits a comma-separated label string into a map.
func parseMetricLabels(labelsPart string) map[string]string {
	labels := make(map[string]string)
	parts := strings.Split(labelsPart, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		eqIdx := strings.Index(part, "=")
		if eqIdx == -1 {
			continue
		}
		key := strings.TrimSpace(part[:eqIdx])
		value := strings.TrimSpace(part[eqIdx+1:])
		value = strings.Trim(value, "\"")
		labels[key] = value
	}
	return labels
}

// inferVendorFromType attempts to determine GPU vendor from device type name.
func inferVendorFromType(deviceType string) string {
	upperType := strings.ToUpper(deviceType)
	switch {
	case strings.Contains(upperType, "NVIDIA"),
		strings.Contains(upperType, "A100"),
		strings.Contains(upperType, "V100"),
		strings.Contains(upperType, "T4"),
		strings.Contains(upperType, "RTX"),
		strings.Contains(upperType, "GTX"),
		strings.Contains(upperType, "TESLA"),
		strings.Contains(upperType, "QUADRO"):
		return "nvidia"
	case strings.Contains(upperType, "AMD"),
		strings.Contains(upperType, "MI"),
		strings.Contains(upperType, "RX"),
		strings.Contains(upperType, "RADEON"):
		return "amd"
	default:
		return "unknown"
	}
}

// GetNodeInfo retrieves the device information for a specific node.
func (c *Client) GetNodeInfo(nodeName string) (*NodeDetailResponse, error) {
	var out NodeDetailResponse
	path := fmt.Sprintf("/nodes/%s", url.PathEscape(nodeName))
	if err := c.doGet(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindGPU searches for devices matching the given optional criteria. Passing
// an empty string for any criterion skips that filter.
func (c *Client) FindGPU(uuid, deviceType, nodeName string) ([]NodeDeviceMatch, error) {
	query := url.Values{}
	if uuid != "" {
		query.Set("uuid", uuid)
	}
	if deviceType != "" {
		query.Set("type", deviceType)
	}
	if nodeName != "" {
		query.Set("node", nodeName)
	}
	path := "/find-gpu"
	if encoded := query.Encode(); encoded != "" {
		path = path + "?" + encoded
	}
	var out []NodeDeviceMatch
	if err := c.doGet(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// NodeExists reports whether the scheduler currently knows about a node with
// the given name and that node has at least one device registered.
func (c *Client) NodeExists(nodeName string) (bool, error) {
	resp, err := c.GetNodeInfo(nodeName)
	if err != nil {
		return false, err
	}
	return resp != nil && len(resp.Devices) > 0, nil
}

// GPUDeviceExists reports whether the scheduler has registered a GPU with the
// given UUID on the given node (pass empty nodeName to skip node filtering).
func (c *Client) GPUDeviceExists(nodeName, deviceUUID string) (bool, error) {
	matches, err := c.FindGPU(deviceUUID, "", nodeName)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

func (c *Client) doGet(path string, out any) error {
	reqURL := c.baseURL + path
	klog.V(5).InfoS("Scheduler client issuing request", "url", reqURL)

	resp, err := c.httpClient.Get(reqURL)
	if err != nil {
		return fmt.Errorf("scheduler request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("scheduler returned status %d for %s", resp.StatusCode, reqURL)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode scheduler response: %w", err)
	}
	return nil
}
