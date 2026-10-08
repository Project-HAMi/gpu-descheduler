package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"
	"gpu-descheduler/pkg/gpu-descheduler/uuidkey"
)

// +kubebuilder:webhook:path=/mutate,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=gpu-descheduler.k8s.io,admissionReviewVersions=v1

// PodMutator implements the admission.Handler interface
type PodMutator struct {
	Client       client.Client
	Decoder      admission.Decoder
	ClusterState *clusterstate.ClusterState
}

// Handle processes the AdmissionReview request
func (m *PodMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	klog.InfoS("[WEBHOOK_TRIGGERED] Webhook triggered",
		"operation", req.Operation,
		"namespace", req.Namespace,
		"name", req.Name)

	pod := &corev1.Pod{}
	if err := m.Decoder.Decode(req, pod); err != nil {
		klog.Errorf("decode pod failed: %v", err)
		return admission.Errored(http.StatusBadRequest, err)
	}

	klog.InfoS("[POD_DECODED] Pod decoded successfully",
		"namespace", pod.Namespace,
		"name", pod.Name,
		"labels", pod.Labels)

	if req.Operation != "CREATE" {
		klog.V(3).InfoS("[SKIP_NOT_CREATE] Not CREATE operation, skip")
		return admission.Allowed("operation is not CREATE")
	}

	// Check if Pod uses hami-scheduler
	if pod.Spec.SchedulerName != "hami-scheduler" && pod.Spec.SchedulerName != "" {
		klog.V(3).InfoS("[SKIP_SCHEDULER_NAME] Skipping non-hami-scheduler Pod",
			"namespace", pod.Namespace,
			"name", pod.Name,
			"schedulerName", pod.Spec.SchedulerName)
		return admission.Allowed("not using hami-scheduler")
	}

	klog.InfoS("[SCHEDULER_CHECK] Pod uses hami-scheduler, continuing",
		"namespace", pod.Namespace,
		"name", pod.Name,
		"schedulerName", pod.Spec.SchedulerName)

	cachedPlacement, found := cache.FindPlacementForPod(pod)

	klog.InfoS("[CACHE_LOOKUP] Cache lookup result (authoritative source)",
		"namespace", pod.Namespace,
		"podName", pod.Name,
		"foundInCache", found,
		"cachedPlacementName", func() string {
			if cachedPlacement != nil {
				return cachedPlacement.Name
			}
			return "<nil>"
		}(),
		"note", "Active cache represents all available placements. No API fallback needed.")

	var placement *placementv1.HamiPodPlacement
	if found && cachedPlacement != nil {
		placement = cachedPlacement
		klog.InfoS("[PLACEMENT_FROM_CACHE] Using placement from cache (single-use, already consumed)",
			"namespace", pod.Namespace,
			"podName", pod.Name,
			"placementName", placement.Name,
			"reqID", placement.Spec.ReqID,
			"targetNode", placement.Spec.NodeName)
	} else if !found {
		klog.InfoS("[NO_PLACEMENT_IN_CACHE] No available placement in active cache",
			"namespace", pod.Namespace,
			"podName", pod.Name,
			"reason", "All placements have been consumed or none exist for this pod",
			"action", "Allowing normal scheduling without placement annotations")
	}

	if placement == nil {
		klog.InfoS("[NO_MATCHING_PLACEMENT] No matching placement found, skip webhook processing",
			"namespace", pod.Namespace,
			"podName", pod.Name,
			"podLabels", pod.Labels)
		return admission.Allowed("no matching placement - cache is authoritative")
	}

	klog.InfoS("[PLACEMENT_SELECTED] Selected placement for annotation injection",
		"namespace", pod.Namespace,
		"podName", pod.Name,
		"selectedPlacement", placement.Name,
		"placementPhase", placement.Status.Phase,
		"reqID", placement.Spec.ReqID,
		"targetNode", placement.Spec.NodeName,
		"gpuDevices", placement.Spec.GPUDevices)

	annotations := map[string]string{}

	admissionUID := string(req.UID)

	annotations[cache.GetAdmissionAnnotationKey()] = admissionUID

	cache.RecordPendingAdmission(admissionUID, placement, pod.Namespace, pod.Name)

	klog.InfoS("[ADMISSION_UID_INJECTED] Injected admission-UID annotation and recorded mapping",
		"admissionUID", admissionUID,
		"podNamespace", pod.Namespace,
		"podName", pod.Name,
		"placement", placement.Namespace+"/"+placement.Name)

	klog.InfoS("[MATCHED_POD_RECORDING] Pod matched to Placement in Webhook phase",
		"podNamespace", pod.Namespace,
		"podName", pod.Name,
		"placement", placement.Namespace+"/"+placement.Name,
		"note", "Pod UID will be available after creation in Controller phase")

	klog.InfoS("[CLUSTER_STATE_CHECK] Checking if ClusterState is available",
		"isNil", m.ClusterState == nil)

	if m.ClusterState != nil {
		gpuUUIDs, annotationKey, err := m.resolveGPUDeviceUUIDsWithKey(placement.Spec.NodeName, placement.Spec.GPUDevices)
		if err != nil {
			klog.Errorf("failed to resolve GPU device UUIDs: %v", err)
			return admission.Errored(http.StatusInternalServerError, err)
		}

		nodeName := placement.Spec.NodeName

		if len(gpuUUIDs) > 0 && nodeName != "" {
			// GPU UUID Annotation
			if annotationKey != "" {
				annotations[annotationKey] = strings.Join(gpuUUIDs, ",")
				klog.V(3).InfoS("Resolved GPU UUIDs from device indices",
					"nodeName", nodeName,
					"deviceSpecs", placement.Spec.GPUDevices,
					"annotationKey", annotationKey,
					"resolvedUUIDs", gpuUUIDs)
			}

			// Placement Annotation (contains GPU UUID + node name)
			placementValue := fmt.Sprintf("%s,%s", strings.Join(gpuUUIDs, ","), nodeName)
			annotations["hami.placement.com/placement"] = placementValue

			klog.InfoS("[INJECT_PLACEMENT_ANNOTATION] Injecting placement composite annotation",
				"namespace", pod.Namespace,
				"podName", pod.Name,
				"placementName", placement.Name,
				"reqID", placement.Spec.ReqID,
				"targetNode", nodeName,
				"gpuUUIDs", gpuUUIDs,
				"annotationKey", "hami.placement.com/placement",
				"annotationValue", placementValue)

			// Target node name (for debugging and validation)
			annotations["hami.placement.com/target-node"] = nodeName

			klog.V(3).InfoS("[INJECT_NODE_ANNOTATION] Injecting target node annotation",
				"namespace", pod.Namespace,
				"podName", pod.Name,
				"targetNode", nodeName)
		}
	}

	klog.InfoS("[ANNOTATIONS_READY] Annotations ready to inject",
		"namespace", pod.Namespace,
		"podName", pod.Name,
		"annotationCount", len(annotations),
		"annotations", annotations)

	if len(annotations) == 0 {
		klog.Warningf("[NO_ANNOTATIONS] No annotations to inject namespace=%s pod=%s clusterStateNil=%v targetNode=%s gpuDevices=%v",
			pod.Namespace, pod.Name, m.ClusterState == nil, placement.Spec.NodeName, placement.Spec.GPUDevices)
	}

	patches := buildAnnotationPatches(pod.Annotations, annotations)

	klog.InfoS("[SUCCESS] Webhook processing completed, returning Patch response",
		"namespace", pod.Namespace,
		"podName", pod.Name,
		"reqID", placement.Spec.ReqID,
		"patchCount", len(patches))

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		klog.Errorf("marshal patches failed: %v", err)
		return admission.Errored(http.StatusInternalServerError, err)
	}

	klog.V(3).InfoS("[PATCH_CONTENT] Generated JSON Patch",
		"patchSize", len(patchBytes),
		"patchContent", string(patchBytes))

	pt := admissionv1.PatchTypeJSONPatch
	resp := admission.Response{
		AdmissionResponse: admissionv1.AdmissionResponse{
			Patch:     patchBytes,
			PatchType: &pt,
			Allowed:   true,
			Result: &metav1.Status{
				Status: "Success",
			},
		},
	}

	klog.InfoS("[RESPONSE_DETAILS] Admission response details",
		"allowed", resp.Allowed,
		"patchLen", len(resp.Patch))

	return resp
}

func buildAnnotationPatches(existing map[string]string, toAdd map[string]string) (patches []map[string]interface{}) {
	if len(existing) == 0 {
		patches = append(patches, map[string]interface{}{
			"op":    "add",
			"path":  "/metadata/annotations",
			"value": toAdd,
		})
	} else {
		for k, v := range toAdd {
			escaped := strings.ReplaceAll(k, "/", "~1")
			patches = append(patches, map[string]interface{}{
				"op":    "add",
				"path":  fmt.Sprintf("/metadata/annotations/%s", escaped),
				"value": v,
			})
		}
	}
	return patches
}

// parseDeviceSpec parses device spec in format "gpu-0/70"
// Returns: deviceIndex (device index), corePercentage (compute percentage), error
// Example: "gpu-0/70" -> (deviceIndex=0, corePercentage=70, err=nil)
func parseDeviceSpec(deviceSpec string) (deviceIndex uint, corePercentage int, err error) {
	parts := strings.Split(deviceSpec, "/")
	if len(parts) < 1 || len(parts) > 2 {
		err = fmt.Errorf("invalid device spec format: %s, expected format: gpu-<index> or gpu-<index>/<core%%>", deviceSpec)
		return
	}

	indexPart := strings.TrimSpace(parts[0])

	if !strings.HasPrefix(indexPart, "gpu-") {
		err = fmt.Errorf("invalid device index format: %s, expected prefix: gpu-", indexPart)
		return
	}

	indexStr := strings.TrimPrefix(indexPart, "gpu-")
	rawIndex, parseErr := strconv.ParseUint(indexStr, 10, 32)
	if parseErr != nil {
		err = fmt.Errorf("invalid device index number in %s: %v", indexStr, parseErr)
		return
	}
	deviceIndex = uint(rawIndex)

	if len(parts) == 2 {
		corePart := strings.TrimSpace(parts[1])
		corePercentage, err = strconv.Atoi(corePart)
		if err != nil {
			err = fmt.Errorf("invalid core percentage in %s: %v", corePart, err)
			return
		}
	} else {
		corePercentage = 100
	}

	return
}

// resolveGPUDeviceUUIDsWithKey converts device spec list to real GPU UUID list and returns correct annotation key
// Input: nodeName="aitem-node", deviceSpecs=["gpu-0/70", "gpu-1/70"]
// Improved: Returns comprehensive error list instead of failing on first error
func (m *PodMutator) resolveGPUDeviceUUIDsWithKey(nodeName string, deviceSpecs []string) (uuids []string, annotationKey string, err error) {
	var validationErrors []string

	for i, spec := range deviceSpecs {
		deviceIndex, corePercentage, parseErr := parseDeviceSpec(spec)
		if parseErr != nil {
			errMsg := fmt.Sprintf("gpuDevices[%d]: invalid format '%s' - %v", i, spec, parseErr)
			validationErrors = append(validationErrors, errMsg)
			klog.Warningf("Failed to parse device spec %s: %v", spec, parseErr)
			continue
		}

		klog.V(4).InfoS("Looking up device by index",
			"nodeName", nodeName,
			"deviceIndex", deviceIndex,
			"corePercentage", corePercentage)

		deviceInfo, found := m.ClusterState.GetDeviceInfoByIndexOnNode(nodeName, deviceIndex)
		if !found {
			errMsg := fmt.Sprintf("gpuDevices[%d]: device index %d not found on node '%s' (node may not exist or device index out of range)", i, deviceIndex, nodeName)
			validationErrors = append(validationErrors, errMsg)
			klog.Errorf("Device index %d not found on node %s", deviceIndex, nodeName)
			continue
		}

		if !deviceInfo.Health {
			errMsg := fmt.Sprintf("gpuDevices[%d]: device index %d on node '%s' is not healthy (status: unknown)", i, deviceIndex, nodeName)
			validationErrors = append(validationErrors, errMsg)
			klog.Errorf("Device index %d on node %s is not healthy", deviceIndex, nodeName)
			continue
		}

		uuids = append(uuids, deviceInfo.ID)

		if annotationKey == "" {
			annotationKey = uuidkey.Build(deviceInfo.Type, deviceInfo.DeviceVendor)
		}

		klog.V(3).InfoS("Resolved device index to UUID",
			"nodeName", nodeName,
			"deviceIndex", deviceIndex,
			"corePercentage", corePercentage,
			"resolvedUUID", deviceInfo.ID,
			"deviceType", deviceInfo.Type,
			"deviceVendor", deviceInfo.DeviceVendor,
			"annotationKey", annotationKey)
	}

	if len(validationErrors) > 0 {
		errorSummary := fmt.Sprintf("GPU device validation failed for node '%s':\n%s\nTotal errors: %d/%d devices",
			nodeName,
			strings.Join(validationErrors, "\n"),
			len(validationErrors),
			len(deviceSpecs))
		err = fmt.Errorf("%s", errorSummary)
		return
	}

	return
}
