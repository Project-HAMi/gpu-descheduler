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

package clusterstate

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"gpu-descheduler/pkg/schedulerclient"
)

// RediscoverFunc is a callback function type for rediscovering scheduler URL
type RediscoverFunc func() string

// ClusterState maintains cluster node and GPU device information fetched from the scheduler
type ClusterState struct {
	mu                         sync.RWMutex
	nodes                      map[string]*NodeInfo
	lastSyncAt                 time.Time
	syncErr                    error
	client                     *schedulerclient.Client
	clientMu                   sync.RWMutex // Mutex specifically protecting the client field
	stopCh                     chan struct{}
	infoPrinted                bool // flag to indicate if detailed info has been printed (print only once)
	rediscoverFn               RediscoverFunc
	consecutiveFailures        int
	maxRetriesBeforeRediscover int
	lastRediscoverTime         time.Time     // Last rediscovery time, for cooldown mechanism
	rediscoverCooldown         time.Duration // Rediscovery cooldown duration
}

// NodeInfo represents device information for a single node
type NodeInfo struct {
	NodeName       string
	Labels         map[string]string
	Devices        map[string]*DeviceInfo // key: deviceUUID (primary key)
	DevicesByIndex map[uint]*DeviceInfo   // key: GPU index (auxiliary index)
	Healthy        bool
}

// DeviceInfo represents information for a single GPU device
type DeviceInfo struct {
	ID           string
	Index        uint
	Type         string
	DeviceVendor string
	Count        int32
	Devmem       int32
	Devcore      int32
	Numa         int
	Mode         string
	Health       bool
}

// NewClusterState creates a cluster state instance, schedulerURL is the HTTP service address of the scheduler
// rediscoverFn is an optional callback to rediscover scheduler URL when connection fails
func NewClusterState(schedulerURL string, rediscoverFn RediscoverFunc) *ClusterState {
	return &ClusterState{
		nodes:                      make(map[string]*NodeInfo),
		client:                     schedulerclient.NewClient(schedulerURL),
		stopCh:                     make(chan struct{}),
		rediscoverFn:               rediscoverFn,
		consecutiveFailures:        0,
		maxRetriesBeforeRediscover: 3,                // After 3 consecutive failures, try to rediscover URL
		lastRediscoverTime:         time.Time{},      // Zero value means never rediscovered
		rediscoverCooldown:         30 * time.Second, // At least 30 seconds between rediscoveries
	}
}

// StartSync starts the periodic sync goroutine, interval is the sync interval (e.g., 10*time.Second)
func (cs *ClusterState) StartSync(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	klog.InfoS("Starting cluster state sync", "interval", interval)

	cs.SyncNow()

	for {
		select {
		case <-ticker.C:
			cs.SyncNow()
		case <-cs.stopCh:
			klog.Info("Stopping cluster state sync")
			return
		}
	}
}

// Stop stops the sync process
func (cs *ClusterState) Stop() {
	close(cs.stopCh)
}

// getClient safely returns the current scheduler client (thread-safe)
func (cs *ClusterState) getClient() *schedulerclient.Client {
	cs.clientMu.RLock()
	defer cs.clientMu.RUnlock()
	return cs.client
}

// setClient safely replaces the scheduler client (thread-safe)
func (cs *ClusterState) setClient(client *schedulerclient.Client) {
	cs.clientMu.Lock()
	defer cs.clientMu.Unlock()
	cs.client = client
}

// SyncNow immediately executes an operation to fetch cluster information from the scheduler
// It implements automatic retry and URL rediscovery when connection fails
func (cs *ClusterState) SyncNow() {
	client := cs.getClient()
	resp, err := client.GetClusterInfoFromMetrics()
	if err != nil {
		cs.mu.Lock()
		cs.syncErr = err
		cs.consecutiveFailures++
		currentFailures := cs.consecutiveFailures
		cs.mu.Unlock()

		klog.ErrorS(err, "Failed to sync cluster state from scheduler metrics endpoint",
			"consecutiveFailures", currentFailures)

		// Try to rediscover scheduler URL after consecutive failures
		if cs.rediscoverFn != nil && currentFailures >= cs.maxRetriesBeforeRediscover {
			// Check cooldown period to avoid frequent rediscovery
			cs.mu.RLock()
			timeSinceLastRediscover := time.Since(cs.lastRediscoverTime)
			cooldown := cs.rediscoverCooldown
			cs.mu.RUnlock()

			if !cs.lastRediscoverTime.IsZero() && timeSinceLastRediscover < cooldown {
				klog.V(3).InfoS("Skipping rediscovery due to cooldown",
					"timeSinceLastRediscover", timeSinceLastRediscover,
					"cooldown", cooldown,
					"nextRetryIn", cooldown-timeSinceLastRediscover)
				return
			}

			klog.V(1).InfoS("Multiple consecutive failures detected, attempting to rediscover scheduler URL...",
				"failures", currentFailures,
				"threshold", cs.maxRetriesBeforeRediscover)

			newURL := cs.rediscoverFn()
			if newURL != "" {
				// Check if new URL is same as current URL
				currentClient := cs.getClient()
				currentURL := currentClient.BaseURL()

				if newURL == currentURL {
					klog.V(2).InfoS("Rediscovered URL is same as current, skipping client replacement",
						"currentURL", currentURL,
						"action", "Resetting failure counter and will retry")
					// Same URL but may be transient network issue, reset counter to allow retry
					cs.mu.Lock()
					cs.consecutiveFailures = 0
					cs.lastRediscoverTime = time.Now()
					cs.mu.Unlock()
					return
				}

				newClient := schedulerclient.NewClient(newURL)
				cs.setClient(newClient)

				cs.mu.Lock()
				cs.consecutiveFailures = 0 // Reset failure counter after successful rediscovery
				cs.lastRediscoverTime = time.Now()
				cs.mu.Unlock()

				klog.InfoS("Scheduler URL rediscovered successfully",
					"oldURL", currentURL,
					"newURL", newURL,
					"action", "Will retry sync with new URL in next cycle")

				// Don't return here, let the next sync cycle try with the new URL
				return
			} else {
				klog.ErrorS(nil, "Failed to rediscover scheduler URL, will keep retrying with current URL")
			}
		}
		return
	}

	newNodes := make(map[string]*NodeInfo, len(resp.Nodes))
	for _, nodeResp := range resp.Nodes {
		devices := make(map[string]*DeviceInfo, len(nodeResp.Devices))
		devicesByIndex := make(map[uint]*DeviceInfo, len(nodeResp.Devices))

		for _, devResp := range nodeResp.Devices {
			deviceInfo := &DeviceInfo{
				ID:           devResp.ID,
				Index:        devResp.Index,
				Type:         devResp.Type,
				DeviceVendor: devResp.DeviceVendor,
				Count:        devResp.Count,
				Devmem:       devResp.Devmem,
				Devcore:      devResp.Devcore,
				Numa:         devResp.Numa,
				Mode:         devResp.Mode,
				Health:       devResp.Health,
			}

			devices[devResp.ID] = deviceInfo
			devicesByIndex[devResp.Index] = deviceInfo
		}

		newNodes[nodeResp.NodeName] = &NodeInfo{
			NodeName:       nodeResp.NodeName,
			Labels:         nodeResp.Labels,
			Devices:        devices,
			DevicesByIndex: devicesByIndex,
			Healthy:        nodeResp.Healthy,
		}
	}

	cs.mu.Lock()
	cs.nodes = newNodes
	cs.lastSyncAt = time.Now()
	cs.syncErr = nil
	cs.consecutiveFailures = 0 // Reset failure counter on successful sync
	cs.mu.Unlock()

	klog.V(2).InfoS("Cluster state synced successfully",
		"nodeCount", len(newNodes),
		"totalGPUs", resp.TotalGPUs,
		"healthy", resp.Healthy)

	if !cs.infoPrinted {
		cs.printDetailedNodeInfo(newNodes, int32(resp.TotalGPUs), resp.Healthy)
		cs.infoPrinted = true
	}
}

func (cs *ClusterState) printDetailedNodeInfo(newNodes map[string]*NodeInfo, totalGPUs int32, healthy bool) {
	klog.InfoS("========== Cluster Node Information (First Sync) ==========")
	for nodeName, nodeInfo := range newNodes {
		klog.InfoS("Node",
			"nodeName", nodeName,
			"healthy", nodeInfo.Healthy,
			"deviceCount", len(nodeInfo.Devices))

		for _, device := range nodeInfo.Devices {
			klog.InfoS("  GPU Device",
				"deviceID", device.ID,
				"index", device.Index,
				"type", device.Type,
				"vendor", device.DeviceVendor,
				"memoryMB", device.Devmem,
				"cores", device.Devcore,
				"health", device.Health,
				"mode", device.Mode)
		}
	}
	klog.InfoS("========== End Cluster Node Information ==========",
		"summary", fmt.Sprintf("Total: %d nodes, %d GPUs, healthy=%v",
			len(newNodes), totalGPUs, healthy))
}

// GetNode retrieves information for a specified node (read-only)
func (cs *ClusterState) GetNode(nodeName string) (*NodeInfo, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	node, ok := cs.nodes[nodeName]
	return node, ok
}

// GetAllNodes retrieves information for all nodes (read-only)
func (cs *ClusterState) GetAllNodes() map[string]*NodeInfo {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := make(map[string]*NodeInfo, len(cs.nodes))
	for k, v := range cs.nodes {
		result[k] = v
	}
	return result
}

// NodeExists checks if the node exists and is healthy
func (cs *ClusterState) NodeExists(nodeName string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	node, ok := cs.nodes[nodeName]
	return ok && node != nil && len(node.Devices) > 0
}

// DeviceExistsOnNode checks if the specified GPU device exists on the node (by UUID)
func (cs *ClusterState) DeviceExistsOnNode(nodeName, deviceUUID string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	node, ok := cs.nodes[nodeName]
	if !ok || node == nil {
		return false
	}
	dev, exists := node.Devices[deviceUUID]
	return exists && dev != nil && dev.Health
}

// DeviceExistsByIndexOnNode checks if the GPU device with the specified index exists on the node (by Index)
func (cs *ClusterState) DeviceExistsByIndexOnNode(nodeName string, deviceIndex uint) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	node, ok := cs.nodes[nodeName]
	if !ok || node == nil {
		return false
	}
	dev, exists := node.DevicesByIndex[deviceIndex]
	return exists && dev != nil && dev.Health
}

// GetDeviceInfoByIndexOnNode gets detailed information for the GPU device with the specified index on the node
func (cs *ClusterState) GetDeviceInfoByIndexOnNode(nodeName string, deviceIndex uint) (*DeviceInfo, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	node, ok := cs.nodes[nodeName]
	if !ok || node == nil {
		return nil, false
	}
	dev, exists := node.DevicesByIndex[deviceIndex]
	return dev, exists
}

// GetLastSyncTime returns the time of the last successful sync
func (cs *ClusterState) GetLastSyncTime() time.Time {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastSyncAt
}

// GetSyncError returns the error from the last sync (if any)
func (cs *ClusterState) GetSyncError() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.syncErr
}

// IsReady checks if the cluster state is ready (at least one successful sync has been completed)
func (cs *ClusterState) IsReady() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return !cs.lastSyncAt.IsZero() && cs.syncErr == nil
}
