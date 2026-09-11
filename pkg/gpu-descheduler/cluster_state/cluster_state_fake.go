package clusterstate

import (
	"time"
)

// NewFakeClusterState creates a cluster state instance with fake data (for debugging and testing)
func NewFakeClusterState() *ClusterState {
	fakeCS := &ClusterState{
		nodes:      make(map[string]*NodeInfo),
		lastSyncAt: time.Now(),
		syncErr:    nil,
		stopCh:     make(chan struct{}),
	}

	fakeNode := &NodeInfo{
		NodeName: "k8s-gpu-3080x2",
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "gpu-node",
			"hami.io/gpu":                      "true",
		},
		Devices: map[string]*DeviceInfo{
			"GPU-7e6e20c7-9ebf-887b-9448-07d4a17068a8": {
				ID:           "GPU-7e6e20c7-9ebf-887b-9448-07d4a17068a8",
				Index:        0,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory (unit: MB)
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
			"GPU-3f2e6992-6787-7b8d-e17f-48a6d4257835": {
				ID:           "GPU-3f2e6992-6787-7b8d-e17f-48a6d4257835",
				Index:        1,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory (unit: MB)
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
		},
		DevicesByIndex: map[uint]*DeviceInfo{
			0: {
				ID:           "GPU-7e6e20c7-9ebf-887b-9448-07d4a17068a8",
				Index:        0,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory (unit: MB)
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
			1: {
				ID:           "GPU-3f2e6992-6787-7b8d-e17f-48a6d4257835",
				Index:        1,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory (unit: MB)
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
		},
		Healthy: true,
	}

	fakeCS.nodes["k8s-gpu-3080x2"] = fakeNode

	// Add second node k8s-node01 (RTX 3080 10GB)
	fakeNode01 := &NodeInfo{
		NodeName: "k8s-node01",
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "gpu-node",
			"hami.io/gpu":                      "true",
		},
		Devices: map[string]*DeviceInfo{
			"GPU-1864e608-c5ce-a88f-c536-8e07b15a3743": {
				ID:           "GPU-1864e608-c5ce-a88f-c536-8e07b15a3743",
				Index:        0,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
		},
		DevicesByIndex: map[uint]*DeviceInfo{
			0: {
				ID:           "GPU-1864e608-c5ce-a88f-c536-8e07b15a3743",
				Index:        0,
				Type:         "NVIDIA GeForce RTX 3080",
				DeviceVendor: "NVIDIA",
				Count:        1,
				Devmem:       10240, // 10GB video memory
				Devcore:      8704,  // RTX 3080 CUDA core count
				Numa:         0,
				Mode:         "CGROUP",
				Health:       true,
			},
		},
		Healthy: true,
	}
	fakeCS.nodes["k8s-node01"] = fakeNode01

	return fakeCS
}
