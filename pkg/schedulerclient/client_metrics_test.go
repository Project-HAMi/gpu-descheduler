package schedulerclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetClusterInfoFromMetrics(t *testing.T) {
	mockMetrics := `# HELP hami_node_gpu_overview GPU overview on a certain node
# TYPE hami_node_gpu_overview gauge
hami_node_gpu_overview{node="worker-1",device_uuid="GPU-abc123",device_index="0",device_cores="80",device_memory_limit="24576",device_type="A100"} 10737418240.000000
hami_node_gpu_overview{node="worker-1",device_uuid="GPU-def456",device_index="1",device_cores="80",device_memory_limit="24576",device_type="A100"} 5368709120.000000
hami_node_gpu_overview{node="worker-2",device_uuid="GPU-xyz789",device_index="0",device_cores="40",device_memory_limit="16384",device_type="T4"} 8589934592.000000
# HELP hami_gpu_memory_limit_bytes Device memory limit for a certain GPU
# TYPE hami_gpu_memory_limit_bytes gauge
hami_gpu_memory_limit_bytes{node="worker-1",device_uuid="GPU-abc123",device_index="0",device_type="A100"} 2.581697216e+10
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(mockMetrics))
			return
		}
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.GetClusterInfoFromMetrics()
	if err != nil {
		t.Fatalf("GetClusterInfoFromMetrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("response should not be nil")
	}

	if len(resp.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(resp.Nodes))
	}

	if resp.TotalGPUs != 3 {
		t.Errorf("expected 3 total GPUs, got %d", resp.TotalGPUs)
	}

	if !resp.Healthy {
		t.Error("expected cluster to be healthy")
	}

	worker1 := findNode(resp.Nodes, "worker-1")
	if worker1 == nil {
		t.Fatal("worker-1 node not found")
	}
	if worker1.DeviceCount != 2 {
		t.Errorf("expected 2 devices on worker-1, got %d", worker1.DeviceCount)
	}

	worker2 := findNode(resp.Nodes, "worker-2")
	if worker2 == nil {
		t.Fatal("worker-2 node not found")
	}
	if worker2.DeviceCount != 1 {
		t.Errorf("expected 1 device on worker-2, got %d", worker2.DeviceCount)
	}

	device := findDevice(worker1.Devices, "GPU-abc123")
	if device == nil {
		t.Fatal("GPU-abc123 device not found on worker-1")
	}
	if device.Type != "A100" {
		t.Errorf("expected device type A100, got %s", device.Type)
	}
	if device.Index != 0 {
		t.Errorf("expected device index 0, got %d", device.Index)
	}
	if device.DeviceVendor != "nvidia" {
		t.Errorf("expected vendor nvidia, got %s", device.DeviceVendor)
	}
	if device.Devmem != 24576 {
		t.Errorf("expected devmem 24576, got %d", device.Devmem)
	}
}

func TestParseOverviewMetricLine(t *testing.T) {
	tests := []struct {
		name           string
		line           string
		expectedNode   string
		expectedUUID   string
		expectedType   string
		expectedIndex  uint
		expectedMemory int32
	}{
		{
			name:           "valid A100 metric",
			line:           `hami_node_gpu_overview{node="node1",device_uuid="GPU-001",device_index="0",device_cores="80",device_memory_limit="24576",device_type="A100"} 10737418240.000000`,
			expectedNode:   "node1",
			expectedUUID:   "GPU-001",
			expectedType:   "A100",
			expectedIndex:  0,
			expectedMemory: 24576,
		},
		{
			name:           "valid T4 metric",
			line:           `hami_node_gpu_overview{node="node2",device_uuid="GPU-002",device_index="1",device_cores="40",device_memory_limit="16384",device_type="T4"} 8589934592.000000`,
			expectedNode:   "node2",
			expectedUUID:   "GPU-002",
			expectedType:   "T4",
			expectedIndex:  1,
			expectedMemory: 16384,
		},
		{
			name:         "invalid format - missing braces",
			line:         `hami_node_gpu_overview node="x" 123`,
			expectedNode: "",
			expectedUUID: "",
		},
		{
			name:         "different metric - should be skipped",
			line:         `hami_gpu_memory_limit_bytes{node="x",device_uuid="y"} 12345`,
			expectedNode: "",
			expectedUUID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeName, deviceInfo, err := parseOverviewMetricLine(tt.line)
			if tt.expectedNode == "" && tt.expectedUUID == "" {
				if err == nil && nodeName != "" {
					t.Error("expected error or empty result for invalid input")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if nodeName != tt.expectedNode {
				t.Errorf("expected node %s, got %s", tt.expectedNode, nodeName)
			}
			if deviceInfo.ID != tt.expectedUUID {
				t.Errorf("expected UUID %s, got %s", tt.expectedUUID, deviceInfo.ID)
			}
			if deviceInfo.Type != tt.expectedType {
				t.Errorf("expected type %s, got %s", tt.expectedType, deviceInfo.Type)
			}
			if deviceInfo.Index != tt.expectedIndex {
				t.Errorf("expected index %d, got %d", tt.expectedIndex, deviceInfo.Index)
			}
			if deviceInfo.Devmem != tt.expectedMemory {
				t.Errorf("expected memory %d, got %d", tt.expectedMemory, deviceInfo.Devmem)
			}
		})
	}
}

func TestInferVendorFromType(t *testing.T) {
	tests := []struct {
		deviceType string
		expected   string
	}{
		{"A100", "nvidia"},
		{"V100", "nvidia"},
		{"T4", "nvidia"},
		{"RTX3090", "nvidia"},
		{"MI100", "amd"},
		{"MI250", "amd"},
		{"RX6800", "amd"},
		{"UnknownGPU", "unknown"},
	}

	for _, tt := range tests {
		result := inferVendorFromType(tt.deviceType)
		if result != tt.expected {
			t.Errorf("inferVendorFromType(%s) = %s, expected %s", tt.deviceType, result, tt.expected)
		}
	}
}

func findNode(nodes []NodeInfoResponse, name string) *NodeInfoResponse {
	for i := range nodes {
		if nodes[i].NodeName == name {
			return &nodes[i]
		}
	}
	return nil
}

func findDevice(devices []DeviceInfoResponse, uuid string) *DeviceInfoResponse {
	for i := range devices {
		if devices[i].ID == uuid {
			return &devices[i]
		}
	}
	return nil
}
