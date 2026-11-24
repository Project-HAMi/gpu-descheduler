package webhook

import (
	"fmt"
	"math"
	"regexp"
	"testing"
)

// crdDeviceSpec is the gpuDevices item pattern from the HamiPodPlacement CRD.
var crdDeviceSpec = regexp.MustCompile(`^gpu-[0-9]+(/([1-9][0-9]?|100))?$`)

func FuzzParseDeviceSpec(f *testing.F) {
	for _, seed := range []string{"gpu-0", "gpu-1/70", "gpu-12/100", "gpu-", "gpu--1", "gpu-0/abc", "gpu-0/1/2", "", "gpu-99999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		index, core, err := parseDeviceSpec(spec)
		if !crdDeviceSpec.MatchString(spec) {
			return
		}
		// Every spec the API server accepts must parse. Indexes past uint32
		// are out of range on purpose; no node has that many GPUs.
		var want uint64
		if _, scanErr := fmt.Sscanf(spec, "gpu-%d", &want); scanErr != nil || want > math.MaxUint32 {
			return
		}
		if err != nil {
			t.Fatalf("CRD-valid spec %q failed to parse: %v", spec, err)
		}
		if uint64(index) != want || core < 1 || core > 100 {
			t.Fatalf("spec %q parsed to index %d core %d", spec, index, core)
		}
	})
}
