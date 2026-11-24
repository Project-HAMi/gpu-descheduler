package schedulerclient

import (
	"strings"
	"testing"
)

func FuzzParseMetrics(f *testing.F) {
	f.Add(`hami_node_gpu_overview{device_cores="100",device_index="0",device_memory_limit="24576",device_type="NVIDIA",device_uuid="GPU-a",node="n1"} 2.097152e+10`)
	f.Add(`hami_vgpu_memory_allocated_bytes{device_uuid="GPU-a",namespace="default",node="n1",pod="p"} 1e9`)
	f.Add(`hami_gpu_core_limit_ratio{device_index="0",device_type="NVIDIA",device_uuid="GPU-a",node="n1"} 100`)
	f.Add(`hami_node_gpu_overview{device_type="GPU, rev } B",node="n1",device_uuid="GPU-a",device_index="0"} NaN`)
	f.Add("hami_node_gpu_overview{")
	f.Fuzz(func(t *testing.T, metrics string) {
		resp, err := parseMetricsToClusterInfo(strings.NewReader(metrics))
		if err != nil {
			return
		}
		gpus := 0
		for _, node := range resp.Nodes {
			if node.NodeName == "" || node.DeviceCount != len(node.Devices) {
				t.Fatalf("bad node %+v", node)
			}
			gpus += len(node.Devices)
		}
		if gpus != resp.TotalGPUs {
			t.Fatalf("totalGPUs %d, devices %d", resp.TotalGPUs, gpus)
		}
	})
}
