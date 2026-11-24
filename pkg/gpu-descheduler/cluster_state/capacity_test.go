package clusterstate

import (
	"strings"
	"testing"
)

func TestCheckMoveCapacity(t *testing.T) {
	gpu0 := &DeviceInfo{ID: "GPU-0", Index: 0, Devmem: 24576, Usedmem: 20000, CoreLimit: 100, CoreAllocated: 30, Health: true}
	gpu1 := &DeviceInfo{ID: "GPU-1", Index: 1, Devmem: 24576, Usedmem: 10000, CoreLimit: 100, CoreAllocated: 80, Health: true}
	cs := &ClusterState{
		nodes: map[string]*NodeInfo{"n1": {
			NodeName:       "n1",
			Devices:        map[string]*DeviceInfo{"GPU-0": gpu0, "GPU-1": gpu1},
			DevicesByIndex: map[uint]*DeviceInfo{0: gpu0, 1: gpu1},
		}},
		podAllocations: map[string][]PodDeviceAllocation{
			"default/victim": {{DeviceUUID: "GPU-0", Mem: 20000, Cores: 30}},
			"default/small":  {{DeviceUUID: "GPU-0", Mem: 4000}},
		},
	}

	for _, tc := range []struct {
		name, pod, node string
		devices         []uint
		wantErr         string
	}{
		{name: "own share is released", pod: "victim", node: "n1", devices: []uint{0}},
		{name: "memory too small", pod: "victim", node: "n1", devices: []uint{1}, wantErr: "has 14576 MiB free"},
		{name: "zero core share fits", pod: "small", node: "n1", devices: []uint{1}},
		{name: "missing device", pod: "victim", node: "n1", devices: []uint{7}, wantErr: "gpu-7 not found"},
		{name: "missing node", pod: "victim", node: "n2", devices: []uint{0}, wantErr: "node n2 not found"},
		{name: "unknown pod skips capacity", pod: "other", node: "n1", devices: []uint{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cs.CheckMoveCapacity("default", tc.pod, tc.node, tc.devices)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want %q", err, tc.wantErr)
			}
		})
	}

	cs.podAllocations["default/hungry"] = []PodDeviceAllocation{{DeviceUUID: "GPU-0", Mem: 1000, Cores: 50}}
	if err := cs.CheckMoveCapacity("default", "hungry", "n1", []uint{1}); err == nil || !strings.Contains(err.Error(), "20 cores free") {
		t.Fatalf("error %v, want core shortage", err)
	}
}
