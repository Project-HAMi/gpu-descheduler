package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const defaultSyncInterval = 1 * time.Minute

// PlacementReconciler is responsible for watching HamiPodPlacement CRD and maintaining cache and status
type PlacementReconciler struct {
	client.Client
	Cache        *cache.PlacementCache
	ClusterState *clusterstate.ClusterState
}

func (r *PlacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pl placementv1.HamiPodPlacement
	if err := r.Get(ctx, req.NamespacedName, &pl); err != nil {
		if errors.IsNotFound(err) {
			klog.V(3).InfoS("Placement deleted or not found, removing from cache",
				"namespace", req.Namespace,
				"name", req.Name)

			r.Cache.Delete(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	klog.V(4).InfoS("Reconciling placement",
		"namespace", pl.Namespace,
		"name", pl.Name,
		"reqID", pl.Spec.ReqID,
		"phase", pl.Status.Phase)

	if cache.IsReqIDConsumed(pl.Spec.ReqID) {
		consumedInfo, _ := cache.GetReqIDConsumptionInfo(pl.Spec.ReqID)

		if consumedInfo.PlacementNS == pl.Namespace && consumedInfo.PlacementName == pl.Name {
			klog.V(3).InfoS("Placement with consumed ReqID is being reconciled, checking for configuration drift",
				"reqID", pl.Spec.ReqID,
				"currentPhase", pl.Status.Phase,
				"consumedPhase", consumedInfo.Phase)

			cachedPl, foundInCache := r.Cache.Get(pl.Namespace, pl.Name)
			if foundInCache {
				isDrifted := !isSpecEqual(&pl, cachedPl)
				if isDrifted {
					klog.ErrorS(nil, "Configuration drift detected! Attempting to modify already-consumed placement",
						"reqID", pl.Spec.ReqID,
						"placement", pl.Namespace+"/"+pl.Name,
						"consumedBy", consumedInfo.PodNamespace+"/"+consumedInfo.PodName,
						"action", "Rejecting modification and marking as Failed")

					pl.Status.Phase = "Failed"
					pl.Status.Message = fmt.Sprintf(
						"Configuration modification rejected: ReqID %s was already consumed by Pod %s/%s at %v. "+
							"To apply new configuration, create a new HamiPodPlacement with a different ReqID.",
						pl.Spec.ReqID,
						consumedInfo.PodNamespace,
						consumedInfo.PodName,
						consumedInfo.ConsumedAt.Format(time.RFC3339),
					)

					if err := r.Status().Update(ctx, &pl); err != nil {
						klog.ErrorS(err, "Failed to update placement status after config drift detection",
							"name", req.Name)
						return ctrl.Result{}, err
					}

					cache.MarkReqIDConsumed(
						pl.Spec.ReqID,
						pl.Namespace,
						pl.Name,
						consumedInfo.PodNamespace,
						consumedInfo.PodName,
						"Failed",
					)

					return ctrl.Result{}, nil
				}
			}

			if consumedInfo.Phase == "Observing" {
				if pl.Status.Phase == "Failed" || pl.Status.Phase == "Completed" {
					klog.InfoS("Placement reached terminal state while observing, updating consumed phase",
						"reqID", pl.Spec.ReqID,
						"placementPhase", pl.Status.Phase,
						"previousConsumedPhase", consumedInfo.Phase)

					cache.MarkReqIDConsumed(
						pl.Spec.ReqID,
						pl.Namespace,
						pl.Name,
						consumedInfo.PodNamespace,
						consumedInfo.PodName,
						pl.Status.Phase,
					)

					klog.V(4).InfoS("Placement in terminal state, stopping observation",
						"reqID", pl.Spec.ReqID,
						"phase", pl.Status.Phase)
					return ctrl.Result{}, nil
				}

				klog.V(4).InfoS("Placement is being observed, skipping validation",
					"reqID", pl.Spec.ReqID)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}

			klog.V(4).InfoS("Placement in terminal state, no further action needed",
				"reqID", pl.Spec.ReqID,
				"phase", consumedInfo.Phase)
			return ctrl.Result{}, nil
		} else {
			klog.V(2).InfoS("ReqID consumed by different placement, this might be a duplicate ReqID error",
				"currentPlacement", pl.Namespace+"/"+pl.Name,
				"originalPlacement", consumedInfo.PlacementNS+"/"+consumedInfo.PlacementName,
				"reqID", pl.Spec.ReqID)

			pl.Status.Phase = "Failed"
			pl.Status.Message = fmt.Sprintf(
				"Duplicate ReqID detected: %s is already used by placement %s/%s",
				pl.Spec.ReqID,
				consumedInfo.PlacementNS,
				consumedInfo.PlacementName,
			)

			if err := r.Status().Update(ctx, &pl); err != nil {
				klog.ErrorS(err, "Failed to update placement status for duplicate ReqID",
					"name", req.Name)
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil
		}
	}

	if cache.IsReqIDConsumed(pl.Spec.ReqID) {
		klog.V(3).InfoS("Placement already consumed, skipping cache update",
			"reqID", pl.Spec.ReqID,
			"placement", pl.Namespace+"/"+pl.Name,
			"phase", pl.Status.Phase)
	} else {
		r.Cache.AddOrUpdate(&pl)
	}

	if r.ClusterState == nil || !r.ClusterState.IsReady() {
		klog.V(3).InfoS("Cluster state not ready, skipping validation")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Phase 1: Handle evictPod (must be before validation!)
	// If currently evicting (EvictingOldPod), check if Pod has completely disappeared
	if pl.Status.Phase == "EvictingOldPod" {
		klog.InfoS("⏳ [CHECKING_EVICTION] Checking old Pod eviction status",
			"namespace", pl.Namespace,
			"name", pl.Name,
			"phase", pl.Status.Phase,
			"evictPod", pl.Spec.EvictPod)

		var targetPod corev1.Pod
		err := r.Get(ctx, client.ObjectKey{Namespace: pl.Namespace, Name: pl.Spec.EvictPod}, &targetPod)

		if err != nil && errors.IsNotFound(err) {
			// Pod fully evicted/deleted, transition to Pending phase
			klog.InfoS("✅ [POD_FULLY_EVICTED] Old Pod fully evicted, preparing to enter Pending phase",
				"namespace", pl.Namespace,
				"name", pl.Name,
				"evictedPod", pl.Spec.EvictPod)

			pl.Status.Phase = ""
			pl.Status.Message = "Old pod evicted, waiting for new pod creation"

			if updateErr := r.Status().Update(ctx, &pl); updateErr != nil {
				klog.ErrorS(updateErr, "Failed to clear eviction status", "name", req.Name)
				return ctrl.Result{}, updateErr
			}

			klog.InfoS("✅ [EVICTION_COMPLETE] Eviction phase complete, will perform node/GPU validation",
				"namespace", pl.Namespace,
				"name", pl.Name)

			pl.Spec.EvictPod = "" // Clear evictPod to avoid re-triggering
			if specErr := r.Update(ctx, &pl); specErr != nil {
				klog.Warning(specErr, "Failed to clear evictPod field (non-critical)")
			}

			// Don't return, continue executing node/GPU validation logic below
		} else if err != nil {
			// Query error, continue waiting
			klog.ErrorS(err, "❌ [CHECK_ERROR] Failed to check Pod status",
				"namespace", pl.Namespace,
				"podName", pl.Spec.EvictPod)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		} else {
			// Pod still exists, continue waiting
			klog.V(3).InfoS("⏳ [STILL_EVICTING] Old Pod is still being evicted...",
				"namespace", pl.Namespace,
				"podName", pl.Spec.EvictPod,
				"podPhase", targetPod.Status.Phase,
				"placement", pl.Name)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// If evictPod is configured and eviction hasn't started, execute eviction
	if pl.Spec.EvictPod != "" {
		klog.InfoS("🎯 [EVICT_POD] Detected evictPod configuration, preparing to evict old Pod",
			"namespace", pl.Namespace,
			"placementName", pl.Name,
			"targetPodToEvict", pl.Spec.EvictPod,
			"currentPhase", pl.Status.Phase)

		err := r.handleEvictPod(ctx, &pl)
		if err != nil {
			klog.ErrorS(err, "❌ [EVICT_POD_FAILED] Failed to evict old Pod",
				"namespace", pl.Namespace,
				"podName", pl.Spec.EvictPod,
				"placementName", pl.Name)

			pl.Status.Phase = "Failed"
			pl.Status.Message = fmt.Sprintf("Failed to evict old pod %s: %v", pl.Spec.EvictPod, err)
			if updateErr := r.Status().Update(ctx, &pl); updateErr != nil {
				klog.ErrorS(updateErr, "Failed to update placement status after eviction failure", "name", req.Name)
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}

		klog.InfoS("✅ [EVICT_POD_SUCCESS] Old Pod eviction operation completed",
			"namespace", pl.Namespace,
			"evictedPod", pl.Spec.EvictPod,
			"placementName", pl.Name,
			"newPhase", pl.Status.Phase)

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Phase 2: Only perform node/GPU validation if no evictPod or after eviction is complete
	nodeExists := r.ClusterState.NodeExists(pl.Spec.NodeName)
	allDevicesValid := true
	for _, deviceSpec := range pl.Spec.GPUDevices {
		deviceIndex, parseErr := parseDeviceSpecFromController(deviceSpec)
		if parseErr != nil {
			klog.ErrorS(parseErr, "Invalid GPU device spec format in placement", "node", pl.Spec.NodeName, "device", deviceSpec)
			allDevicesValid = false
			break
		}

		if !r.ClusterState.DeviceExistsByIndexOnNode(pl.Spec.NodeName, deviceIndex) {
			klog.ErrorS(nil, "Invalid GPU device in placement", "node", pl.Spec.NodeName, "deviceIndex", deviceIndex, "deviceSpec", deviceSpec)
			allDevicesValid = false
			break
		}
	}

	status := pl.Status.DeepCopy()
	if nodeExists && allDevicesValid && len(pl.Spec.GPUDevices) > 0 {
		status.Phase = "Pending"
		status.Message = "Waiting for pod to be created"
	} else if !nodeExists {
		status.Phase = "Failed"
		status.Message = "Target node does not exist in scheduler cluster"
	} else if !allDevicesValid {
		status.Phase = "Failed"
		status.Message = "One or more GPU devices do not exist on target node"
	} else {
		status.Phase = "Failed"
		status.Message = "No GPU devices specified"
	}

	if pl.Status.Phase != status.Phase || pl.Status.Message != status.Message {
		pl.Status = *status
		if err := r.Status().Update(ctx, &pl); err != nil {
			klog.ErrorS(err, "Failed to update placement status", "name", req.Name)
			return ctrl.Result{}, err
		}

		klog.V(2).InfoS("Updated placement status",
			"namespace", pl.Namespace,
			"name", pl.Name,
			"phase", status.Phase,
			"message", status.Message)
	}

	return ctrl.Result{}, nil
}

// isSpecEqual compares whether two Placement Specs are completely identical
func isSpecEqual(a, b *placementv1.HamiPodPlacement) bool {
	if a.Spec.ReqID != b.Spec.ReqID {
		return false
	}
	if a.Spec.NodeName != b.Spec.NodeName {
		return false
	}
	if a.Spec.EvictPod != b.Spec.EvictPod {
		return false
	}
	if len(a.Spec.GPUDevices) != len(b.Spec.GPUDevices) {
		return false
	}
	for i := range a.Spec.GPUDevices {
		if a.Spec.GPUDevices[i] != b.Spec.GPUDevices[i] {
			return false
		}
	}
	return labelSelectorEqual(&a.Spec.PodSelector, &b.Spec.PodSelector)
}

// labelSelectorEqual compares whether two LabelSelectors are identical
func labelSelectorEqual(a, b *metav1.LabelSelector) bool {
	if a.MatchLabels == nil && b.MatchLabels == nil {
		return true
	}
	if a.MatchLabels == nil || b.MatchLabels == nil {
		return false
	}
	if len(a.MatchLabels) != len(b.MatchLabels) {
		return false
	}
	for k, v := range a.MatchLabels {
		if b.MatchLabels[k] != v {
			return false
		}
	}
	return true
}

// handleEvictPod handles evictPod logic: attempts to evict the specified old Pod
func (r *PlacementReconciler) handleEvictPod(ctx context.Context, pl *placementv1.HamiPodPlacement) error {
	podToEvict := pl.Spec.EvictPod
	namespace := pl.Namespace

	klog.InfoS("🔄 [EVICT_POD_ACTION] Starting Pod eviction operation",
		"namespace", namespace,
		"podName", podToEvict,
		"placement", pl.Name)

	// Try to get target Pod (check if it exists)
	var targetPod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podToEvict}, &targetPod)

	if err != nil {
		if errors.IsNotFound(err) {
			// Pod already doesn't exist, consider eviction successful
			klog.InfoS("✅ [POD_ALREADY_EVICTED] Target Pod doesn't exist, no need to evict",
				"namespace", namespace,
				"podName", podToEvict,
				"placement", pl.Name)

			pl.Status.Phase = "EvictingOldPod"
			pl.Status.Message = fmt.Sprintf("Old pod %s already evicted or not found, ready for new pod creation", podToEvict)
			if updateErr := r.Status().Update(ctx, pl); updateErr != nil {
				return fmt.Errorf("failed to update status after pod already evicted: %w", updateErr)
			}
			return nil
		}

		// Other errors (insufficient permissions, network issues, etc.)
		return fmt.Errorf("failed to check pod %s/%s: %w", namespace, podToEvict, err)
	}

	// Pod exists, execute eviction operation
	klog.InfoS("🚪 [EVICTING_POD] Found target Pod, evicting...",
		"namespace", namespace,
		"podName", podToEvict,
		"podUID", targetPod.UID,
		"podPhase", targetPod.Status.Phase,
		"placement", pl.Name)

	// Update status to "EvictingOldPod"
	pl.Status.Phase = "EvictingOldPod"
	pl.Status.Message = fmt.Sprintf("Evicting old pod %s to allow migration", podToEvict)
	if statusErr := r.Status().Update(ctx, pl); statusErr != nil {
		return fmt.Errorf("failed to update status to EvictingOldPod: %w", statusErr)
	}

	klog.InfoS("⏳ [STATUS_UPDATED] Status updated to EvictingOldPod",
		"placement", pl.Name,
		"podToEvict", podToEvict)

	// Create Eviction object and execute eviction
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podToEvict,
			Namespace: namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: ptr.To(int64(30)), // 30 seconds graceful termination time
		},
	}

	if evictErr := r.SubResource("eviction").Create(ctx, &targetPod, eviction); evictErr != nil && !errors.IsNotFound(evictErr) {
		return fmt.Errorf("failed to evict pod %s/%s: %w", namespace, podToEvict, evictErr)
	}

	klog.InfoS("✅ [POD_EVICTED] Pod eviction request sent",
		"namespace", namespace,
		"evictedPod", podToEvict,
		"podUID", targetPod.UID,
		"gracePeriodSeconds", 30,
		"placement", pl.Name)

	return nil
}

// parseDeviceSpecFromController parses device spec in format "gpu-0/70"
// Returns: deviceIndex (device index), error
// Example: "gpu-0/70" -> (deviceIndex=0, err=nil)
func parseDeviceSpecFromController(deviceSpec string) (uint, error) {
	parts := strings.Split(deviceSpec, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, fmt.Errorf("invalid device spec format: %s, expected format: gpu-<index>[/<core%%>]", deviceSpec)
	}

	indexPart := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(indexPart, "gpu-") {
		return 0, fmt.Errorf("invalid device index format: %s, expected prefix: gpu-", indexPart)
	}

	indexStr := strings.TrimPrefix(indexPart, "gpu-")
	rawIndex, parseErr := strconv.Atoi(indexStr)
	if parseErr != nil {
		return 0, fmt.Errorf("invalid device index number in %s: %v", indexStr, parseErr)
	}

	return uint(rawIndex), nil
}

func (r *PlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&placementv1.HamiPodPlacement{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findPlacementsForPod),
		).
		Complete(r)
}

// findPlacementsForPod finds matching Placements and updates ObservationCache when Pod is created/updated
// Only processes the first Placement with empty PodName (matches user requirement: find only the first one)
func (r *PlacementReconciler) findPlacementsForPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		klog.V(4).InfoS("[POD_WATCH] Object is not a Pod, skipping")
		return nil
	}

	if pod.Name == "" {
		klog.V(4).InfoS("[POD_WATCH] Pod has empty name, skipping",
			"namespace", pod.Namespace)
		return nil
	}

	obsCache := cache.GetGlobalObservationCache()
	if obsCache == nil {
		klog.V(4).InfoS("[POD_WATCH] Observation cache not initialized")
		return nil
	}

	placementNS, placementName, found := obsCache.FindFirstEmptyPodNamePlacement(
		pod.Namespace,
		pod.Labels,
	)

	if !found {
		return nil
	}

	updated := obsCache.UpdatePodName(placementNS, placementName, pod.Name)
	if updated {
		klog.InfoS("✅ [POD_WATCH_SUCCESS] Successfully updated Pod name via Controller watch",
			"pod", pod.Namespace+"/"+pod.Name,
			"placement", placementNS+"/"+placementName,
			"podLabels", pod.Labels)
	}

	return nil
}

// StartClusterStateSync starts the cluster state sync goroutine, should be called before manager.Start()
func StartClusterStateSync(schedulerURL string, syncInterval time.Duration, rediscoverFn clusterstate.RediscoverFunc) *clusterstate.ClusterState {
	if syncInterval <= 0 {
		syncInterval = defaultSyncInterval
	}

	cs := clusterstate.NewClusterState(schedulerURL, rediscoverFn)
	//cs := clusterstate.NewFakeClusterState()
	go cs.StartSync(syncInterval)

	klog.InfoS("Cluster state sync started",
		"schedulerURL", schedulerURL,
		"syncInterval", syncInterval,
		"autoRediscover", rediscoverFn != nil)

	printClusterStateInfo(cs)

	return cs
}

// printClusterStateInfo prints cluster state information (for debugging)
func printClusterStateInfo(cs *clusterstate.ClusterState) {
	klog.InfoS("========== Cluster State Information ==========")
	klog.InfoS("Total nodes in cluster:", "count", len(cs.GetAllNodes()))

	for nodeName, nodeInfo := range cs.GetAllNodes() {
		klog.InfoS("--- Node Details ---",
			"nodeName", nodeName,
			"healthy", nodeInfo.Healthy,
			"gpuCount", len(nodeInfo.DevicesByIndex))

		for deviceIndex, deviceInfo := range nodeInfo.DevicesByIndex {
			klog.InfoS("GPU Device",
				"nodeName", nodeName,
				"deviceIndex", deviceIndex,
				"deviceType", deviceInfo.Type,
				"deviceVendor", deviceInfo.DeviceVendor,
				"devmem(MB)", deviceInfo.Devmem,
				"devcore", deviceInfo.Devcore,
				"health", deviceInfo.Health,
				"mode", deviceInfo.Mode,
				"numa", deviceInfo.Numa)
		}
	}

	klog.InfoS("========== End of Cluster State Information ==========")
}
