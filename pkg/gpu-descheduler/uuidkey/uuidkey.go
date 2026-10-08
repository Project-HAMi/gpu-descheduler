// Package uuidkey names the annotation that pins a pod to one GPU. It imports
// nothing from HAMi, whose client package needs a cluster at init, so it can be
// unit tested without one.
package uuidkey

import (
	"fmt"
	"strings"
)

// The NVIDIA values mirror HAMi's pkg/device/nvidia.
const (
	nvidiaDevice  = "NVIDIA"
	nvidiaUseUUID = "nvidia.com/use-gpuuuid"
)

// Build returns the use-gpuuuid annotation key HAMi reads for a device type and vendor.
func Build(deviceType string, deviceVendor string) (annotationKey string) {
	deviceTypeUpper := strings.ToUpper(deviceType)
	deviceVendorUpper := strings.ToUpper(deviceVendor)

	switch {
	case deviceTypeUpper == strings.ToUpper(nvidiaDevice) ||
		strings.Contains(deviceTypeUpper, "NVIDIA") ||
		deviceVendorUpper == "NVIDIA":
		return nvidiaUseUUID
	case strings.Contains(deviceTypeUpper, "AMD") || deviceVendorUpper == "AMD":
		// HAMi's AMD backend reads amd.com/use-gpu-uuid, not the use-gpuuuid
		// spelling of the other vendors, so the default branch would be ignored.
		return "amd.com/use-gpu-uuid"
	case strings.Contains(deviceTypeUpper, "VASTAI") || deviceVendorUpper == "VASTAI":
		return "vastaitech.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "METAX") || deviceVendorUpper == "METAX":
		return "metax-tech.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "MTHREADS") || deviceVendorUpper == "MTHREADS":
		return "mthreads.ai/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "CAMBRICON") || deviceVendorUpper == "CAMBRICON":
		return "cambricon.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "HYGON") || deviceVendorUpper == "HYGON":
		return "hygon.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "ENFLAME") || deviceVendorUpper == "ENFLAME":
		return "enflame.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "KUNLUN") || deviceVendorUpper == "KUNLUN":
		return "baidu.com/use-gpuuuid"
	case strings.Contains(deviceTypeUpper, "BIREN") || deviceVendorUpper == "BIREN":
		return "birentech.com/use-gpuuuid"
	default:
		if deviceVendor != "" {
			vendorLower := strings.ToLower(deviceVendor)
			if strings.Contains(vendorLower, ".") {
				return fmt.Sprintf("%s/use-gpuuuid", vendorLower)
			}
			return fmt.Sprintf("%s.com/use-gpuuuid", vendorLower)
		}
		return nvidiaUseUUID
	}
}
