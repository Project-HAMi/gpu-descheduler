package uuidkey

import "testing"

func TestBuild(t *testing.T) {
	for _, tc := range []struct {
		name, deviceType, vendor, want string
	}{
		// HAMi's AMD backend reads amd.com/use-gpu-uuid; the use-gpuuuid
		// spelling would be ignored and the pod unpinned.
		{"amd by vendor", "", "AMD", "amd.com/use-gpu-uuid"},
		{"amd by registered type", "AMD Radeon Graphics", "", "amd.com/use-gpu-uuid"},
		{"amd by vendor and type", "AMD Radeon Graphics", "AMD", "amd.com/use-gpu-uuid"},
		{"amd lower case vendor", "", "amd", "amd.com/use-gpu-uuid"},
		{"vastai keeps its key", "", "VASTAI", "vastaitech.com/use-gpuuuid"},
		{"metax keeps its key", "", "METAX", "metax-tech.com/use-gpuuuid"},
		{"unknown vendor falls back to <vendor>.com", "", "acme", "acme.com/use-gpuuuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Build(tc.deviceType, tc.vendor); got != tc.want {
				t.Fatalf("Build(%q, %q) = %q, want %q", tc.deviceType, tc.vendor, got, tc.want)
			}
		})
	}
}
