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

package cache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

// PlacementCache maintains in-memory cache for HamiPodPlacement resources
// Supports fast query of matching placement instructions by namespace + label selector
type PlacementCache struct {
	mu         sync.RWMutex
	placements map[string]*placementv1.HamiPodPlacement // key: namespace/name
}

// NewPlacementCache creates an empty placement cache instance
func NewPlacementCache() *PlacementCache {
	return &PlacementCache{
		placements: make(map[string]*placementv1.HamiPodPlacement),
	}
}

// AddOrUpdate adds or updates a placement record
func (pc *PlacementCache) AddOrUpdate(placement *placementv1.HamiPodPlacement) {
	key := cacheKey(placement.Namespace, placement.Name)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	pc.placements[key] = placement.DeepCopy()
	klog.V(3).InfoS("Cache updated",
		"namespace", placement.Namespace,
		"name", placement.Name,
		"reqID", placement.Spec.ReqID,
		"nodeName", placement.Spec.NodeName,
		"gpuDevices", len(placement.Spec.GPUDevices))
}

// Delete deletes the record for the specified key
func (pc *PlacementCache) Delete(namespace, name string) {
	key := cacheKey(namespace, name)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	if _, exists := pc.placements[key]; exists {
		delete(pc.placements, key)
		klog.V(3).InfoS("Cache entry deleted",
			"namespace", namespace,
			"name", name)
	}
}

// Get retrieves a single record by namespace and name (returns deep copy)
func (pc *PlacementCache) Get(namespace, name string) (*placementv1.HamiPodPlacement, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pl, ok := pc.placements[cacheKey(namespace, name)]; ok {
		return pl.DeepCopy(), true
	}
	return nil, false
}

// FindMatchingPlacements finds placements that match given Pod labels and have active status
// Returns all matching placements (there may be multiple)
func (pc *PlacementCache) FindMatchingPlacements(namespace string, podLabels map[string]string) []*placementv1.HamiPodPlacement {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	var matched []*placementv1.HamiPodPlacement

	for key, pl := range pc.placements {
		// Only search within the same namespace
		if extractNamespace(key) != namespace {
			continue
		}

		// Skip completed or failed instructions
		if isTerminalOrMatchedPhase(pl.Status.Phase) {
			continue
		}

		// Match label selector
		sel, err := metav1.LabelSelectorAsSelector(&pl.Spec.PodSelector)
		if err != nil {
			klog.V(4).ErrorS(err, "Invalid label selector in cached placement",
				"key", key)
			continue
		}

		if sel.Matches(labels.Set(podLabels)) {
			matched = append(matched, pl.DeepCopy())
		}
	}

	return matched
}

// FindByReqID finds placement by reqID (global search, cross-namespace)
func (pc *PlacementCache) FindByReqID(reqID string) (*placementv1.HamiPodPlacement, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	for _, pl := range pc.placements {
		if pl.Spec.ReqID == reqID {
			return pl.DeepCopy(), true
		}
	}
	return nil, false
}

// ListAll returns all cached placements (for debugging or management interfaces)
func (pc *PlacementCache) ListAll() []*placementv1.HamiPodPlacement {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	result := make([]*placementv1.HamiPodPlacement, 0, len(pc.placements))
	for _, pl := range pc.placements {
		result = append(result, pl.DeepCopy())
	}
	return result
}

// ListByNamespace returns all placements under the specified namespace
func (pc *PlacementCache) ListByNamespace(namespace string) []*placementv1.HamiPodPlacement {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	var result []*placementv1.HamiPodPlacement
	for key, pl := range pc.placements {
		if extractNamespace(key) == namespace {
			result = append(result, pl.DeepCopy())
		}
	}
	return result
}

// Count returns the total number of records in cache
func (pc *PlacementCache) Count() int {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return len(pc.placements)
}

// Clear clears the entire cache (mainly used for testing)
func (pc *PlacementCache) Clear() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.placements = make(map[string]*placementv1.HamiPodPlacement)
}

// Helper function: generate cache key
func cacheKey(namespace, name string) string {
	return namespace + "/" + name
}

// Helper function: extract namespace from cache key
func extractNamespace(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i]
		}
	}
	return key
}

// Helper function: check if it's a terminal phase (no further processing needed)
func isTerminalOrMatchedPhase(phase string) bool {
	switch phase {
	case "Completed", "Failed", "Matched", "PartiallyCompleted":
		return true
	default:
		return false
	}
}

// ==================== Observation Cache ====================

// ObservedPodPlacement records the matched placement-pod relationship
type ObservedPodPlacement struct {
	Placement    *placementv1.HamiPodPlacement
	PodNamespace string
	PodName      string
	MatchedAt    time.Time
	RetryCount   int
}

// ObservationCache maintains matched placement records waiting for Pod deployment to complete
// Core functions:
//   - Ensure idempotency: prevent the same placement from being reused
//   - Status tracking: record the binding relationship between placement and pod
//   - Async monitoring: work with background goroutines to check pod deployment status and update CRD
type ObservationCache struct {
	mu           sync.RWMutex
	observations map[string]*ObservedPodPlacement // key: placement namespace/name
	client       kubernetes.Interface
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewObservationCache creates an observation cache instance
func NewObservationCache(client kubernetes.Interface) *ObservationCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &ObservationCache{
		observations: make(map[string]*ObservedPodPlacement),
		client:       client,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Add adds an observation record (called after matching from PlacementCache)
func (oc *ObservationCache) Add(placement *placementv1.HamiPodPlacement, podNamespace, podName string) {
	key := cacheKey(placement.Namespace, placement.Name)

	if podName == "" {
		klog.V(2).InfoS("⚠️ [EMPTY_POD_NAME] Adding to observation cache with empty pod name (Webhook phase, Pod not yet created)",
			"key", key,
			"pod", podNamespace+"/<empty>",
			"reqID", placement.Spec.ReqID,
			"note", "Will skip status checks until Pod name is available")
	}

	oc.mu.Lock()
	defer oc.mu.Unlock()

	now := time.Now()
	if existing, exists := oc.observations[key]; exists {
		klog.V(3).InfoS("Updating existing observation",
			"key", key,
			"oldPod", existing.PodNamespace+"/"+existing.PodName,
			"newPod", podNamespace+"/"+podName)
		existing.Placement = placement.DeepCopy()
		existing.PodNamespace = podNamespace
		existing.PodName = podName
		existing.MatchedAt = now
		existing.RetryCount = 0
	} else {
		oc.observations[key] = &ObservedPodPlacement{
			Placement:    placement.DeepCopy(),
			PodNamespace: podNamespace,
			PodName:      podName,
			MatchedAt:    now,
			RetryCount:   0,
		}
		klog.V(3).InfoS("Added to observation cache",
			"key", key,
			"pod", podNamespace+"/"+podName,
			"reqID", placement.Spec.ReqID)
	}
}

// Get retrieves a single observation record
func (oc *ObservationCache) Get(namespace, name string) (*ObservedPodPlacement, bool) {
	oc.mu.RLock()
	defer oc.mu.RUnlock()

	if obs, ok := oc.observations[cacheKey(namespace, name)]; ok {
		return obs, true
	}
	return nil, false
}

// Remove removes an observation record (called after successful or failed deployment)
func (oc *ObservationCache) Remove(namespace, name string) {
	key := cacheKey(namespace, name)

	oc.mu.Lock()
	defer oc.mu.Unlock()

	if _, exists := oc.observations[key]; exists {
		delete(oc.observations, key)
		klog.V(3).InfoS("Removed from observation cache",
			"namespace", namespace,
			"name", name)
	}
}

// UpdatePodName updates the Pod name in the observation record (used for Controller to backfill after detecting Pod creation)
// Only updates the first matching record with empty PodName, returns whether update was successful
func (oc *ObservationCache) UpdatePodName(namespace, placementName, podName string) bool {
	key := cacheKey(namespace, placementName)

	oc.mu.Lock()
	defer oc.mu.Unlock()

	if obs, exists := oc.observations[key]; exists {
		if obs.PodName == "" && podName != "" {
			oldPodName := obs.PodName
			obs.PodName = podName
			obs.RetryCount = 0 // Reset retry counter

			klog.InfoS("🔄 [POD_NAME_UPDATED] Updated empty Pod name in observation cache",
				"key", key,
				"oldPodName", oldPodName,
				"newPodName", namespace+"/"+podName,
				"reqID", obs.Placement.Spec.ReqID,
				"matchedAt", obs.MatchedAt.Format(time.RFC3339))

			return true
		}

		klog.V(3).InfoS("[POD_NAME_SKIP] Skipping Pod name update",
			"key", key,
			"reason", "Pod name already set or new name is empty",
			"currentPodName", obs.PodName,
			"newPodName", podName)
		return false
	}

	klog.V(3).InfoS("[POD_NAME_NOT_FOUND] Observation record not found for Pod name update",
		"key", key,
		"newPodName", namespace+"/"+podName)
	return false
}

// FindFirstEmptyPodNamePlacement finds the first observation record with empty PodName (used for Pod event handling)
// Returns (namespace, name, found)
func (oc *ObservationCache) FindFirstEmptyPodNamePlacement(podNamespace string, podLabels map[string]string) (string, string, bool) {
	oc.mu.RLock()
	defer oc.mu.RUnlock()

	for key, obs := range oc.observations {
		if obs.PodNamespace != podNamespace {
			continue
		}

		if obs.PodName != "" {
			continue
		}

		if labelsMatch(obs.Placement.Spec.PodSelector, podLabels) {
			klog.V(3).InfoS("[FIND_EMPTY_PLACEMENT] Found first Placement with empty Pod name",
				"key", key,
				"reqID", obs.Placement.Spec.ReqID,
				"podLabels", podLabels)

			return obs.Placement.Namespace, obs.Placement.Name, true
		}
	}

	return "", "", false
}

// labelsMatch checks if Pod labels match LabelSelector
func labelsMatch(selector metav1.LabelSelector, podLabels map[string]string) bool {
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return true
	}

	for k, v := range selector.MatchLabels {
		if podLabels[k] != v {
			return false
		}
	}

	if len(selector.MatchExpressions) > 0 {
		selectorObj, err := metav1.LabelSelectorAsSelector(&selector)
		if err != nil {
			klog.V(4).ErrorS(err, "Failed to parse LabelSelector")
			return false
		}
		labelsSet := labels.Set(podLabels)
		return selectorObj.Matches(labelsSet)
	}

	return true
}

// ListAll returns all observation records (for debugging)
func (oc *ObservationCache) ListAll() []*ObservedPodPlacement {
	oc.mu.RLock()
	defer oc.mu.RUnlock()

	result := make([]*ObservedPodPlacement, 0, len(oc.observations))
	for _, obs := range oc.observations {
		result = append(result, obs)
	}
	return result
}

// Count returns the number of records in observation cache
func (oc *ObservationCache) Count() int {
	oc.mu.RLock()
	defer oc.mu.RUnlock()
	return len(oc.observations)
}

// MatchedPodInfo is used to pass matched Pod name to statusUpdater
type MatchedPodInfo struct {
	PodName string
}

// StatusUpdaterFn defines the type of status update function (supports optional MatchedPodInfo parameter)
type StatusUpdaterFn func(namespace, name, phase, message string, matchedPod ...MatchedPodInfo) error

// StartObserver starts a background goroutine to periodically check Pod deployment status
// Parameters:
//   - checkInterval: check interval (default 10 seconds)
//   - maxRetries: maximum retry count (default 60 times, i.e., 10 minutes timeout)
//   - statusUpdater: callback function for updating CRD status (supports optional MatchedPodInfo parameter)
func (oc *ObservationCache) StartObserver(checkInterval time.Duration, maxRetries int, statusUpdater StatusUpdaterFn) {
	if checkInterval <= 0 {
		checkInterval = 10 * time.Second
	}
	if maxRetries <= 0 {
		maxRetries = 60
	}

	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()

		klog.InfoS("Observation cache monitor started",
			"checkInterval", checkInterval,
			"maxRetries", maxRetries)

		for {
			select {
			case <-oc.ctx.Done():
				klog.InfoS("Observation cache monitor stopped")
				return
			case <-ticker.C:
				oc.checkPodStatus(maxRetries, statusUpdater)
			}
		}
	}()
}

// Stop stops the observation goroutine
func (oc *ObservationCache) Stop() {
	if oc.cancel != nil {
		oc.cancel()
	}
}

// checkPodStatus checks the running status of Pods in all observation records
func (oc *ObservationCache) checkPodStatus(maxRetries int, statusUpdater StatusUpdaterFn) {
	if oc.client == nil {
		klog.V(4).InfoS("Kubernetes client not set, skipping pod status check")
		return
	}

	oc.mu.Lock()
	defer oc.mu.Unlock()

	for key, obs := range oc.observations {
		if obs.PodName == "" {
			klog.V(2).InfoS("ℹ️ [EMPTY_POD_NAME] Observation has empty pod name, will attempt admission-uid based matching",
				"key", key,
				"pod", obs.PodNamespace+"/<empty>",
				"reqID", obs.Placement.Spec.ReqID,
				"note", "Using admission-uid annotation to find pod instead of name")
		}
		podClient := oc.client.CoreV1().Pods(obs.PodNamespace)

		var pod *corev1.Pod
		var err error

		if obs.PodName != "" {
			pod, err = podClient.Get(oc.ctx, obs.PodName, metav1.GetOptions{})
		} else {
			klog.V(2).InfoS("🔍 [LIST_PODS_BY_ANNOTATION] Listing pods with admission-uid annotation (empty podName fallback)",
				"namespace", obs.PodNamespace,
				"placement", key,
				"reqID", obs.Placement.Spec.ReqID)

			listOpts := metav1.ListOptions{
				FieldSelector: "metadata.namespace=" + obs.PodNamespace,
			}

			podList, listErr := podClient.List(oc.ctx, listOpts)
			if listErr != nil {
				klog.ErrorS(listErr, "Failed to list pods for admission-uid matching",
					"namespace", obs.PodNamespace,
					"key", key)
				err = listErr
			} else {
				found := false
				for _, p := range podList.Items {
					if admissionUID, hasAnnotation := p.Annotations[GetAdmissionAnnotationKey()]; hasAnnotation {
						if info, exists := FindPlacementByAdmissionUID(admissionUID); exists && info.PlacementName == obs.Placement.Name && info.PlacementNS == obs.Placement.Namespace {
							pod = &p
							found = true
							klog.InfoS("✅ [FOUND_POD_BY_ADMISSION_UID] Located pod via admission-uid annotation matching",
								"pod", p.Namespace+"/"+p.Name,
								"podUID", p.UID,
								"admissionUID", admissionUID,
								"placement", key)
							break
						}
					}
				}

				if !found {
					err = &NotFoundError{Message: fmt.Sprintf("no pod with matching admission-uid found for placement %s", key)}
					klog.V(4).InfoS("No pod with matching admission-uid found yet",
						"key", key,
						"totalPodsChecked", len(podList.Items))
				}
			}
		}

		if err != nil {
			if isNotFoundError(err) {
				klog.V(4).InfoS("Pod not found yet, will retry",
					"pod", obs.PodNamespace+"/"+obs.PodName,
					"key", key,
					"retryCount", obs.RetryCount)
				obs.RetryCount++
			} else {
				klog.ErrorS(err, "Failed to get pod status",
					"pod", obs.PodNamespace+"/"+obs.PodName,
					"key", key)
				obs.RetryCount++
			}

			if obs.RetryCount >= maxRetries {
				klog.V(1).InfoS("Max retries exceeded, marking as failed",
					"key", key,
					"pod", obs.PodNamespace+"/"+obs.PodName,
					"retryCount", obs.RetryCount)

				if statusUpdater != nil {
					if err := statusUpdater(
						obs.Placement.Namespace,
						obs.Placement.Name,
						"Failed",
						fmt.Sprintf("Pod %s/%s not running after %d retries", obs.PodNamespace, obs.PodName, maxRetries),
					); err != nil {
						klog.ErrorS(err, "Failed to update placement status to Failed",
							"key", key)
					}
				}

				delete(oc.observations, key)
			}
			continue
		}

		admissionUIDFromAnnotation, hasAdmissionAnnotation := pod.Annotations[GetAdmissionAnnotationKey()]
		var matchedPlacementNS, matchedPlacementName string
		var admissionMatched bool
		var admissionInfo *PendingAdmissionInfo

		if hasAdmissionAnnotation {
			admissionInfo, admissionMatched = ConsumeAdmissionMapping(admissionUIDFromAnnotation)
			if admissionMatched {
				matchedPlacementNS = admissionInfo.PlacementNS
				matchedPlacementName = admissionInfo.PlacementName

				klog.InfoS("✅ [ADMISSION_UID_MATCH] Perfect match via admission-uid (O(1) lookup)",
					"admissionUID", admissionUIDFromAnnotation,
					"realPodUID", pod.UID,
					"podName", pod.Name,
					"nodeName", pod.Spec.NodeName,
					"placement", matchedPlacementNS+"/"+matchedPlacementName,
					"mapSizeAfterConsume", GetPendingMapSize())

				klog.InfoS("🎯 [ADMISSION_BASED_UPDATE] Updating placement status immediately on detection (no pod status check)",
					"placement", matchedPlacementNS+"/"+matchedPlacementName,
					"podUID", pod.UID,
					"podName", pod.Name,
					"nodeName", pod.Spec.NodeName,
					"podPhase", pod.Status.Phase)

				if statusUpdater != nil {
					matchedPod := MatchedPodInfo{
						PodName: pod.Name,
					}

					if err := statusUpdater(
						matchedPlacementNS,
						matchedPlacementName,
						"Matched",
						fmt.Sprintf("Pod %s (UID: %s) detected on node %s with phase %s [verified via admission-uid: %s]",
							pod.Name, pod.UID, pod.Spec.NodeName, pod.Status.Phase, admissionUIDFromAnnotation),
						matchedPod,
					); err != nil {
						klog.ErrorS(err, "Failed to update placement status (admission-uid verified)",
							"placement", matchedPlacementNS+"/"+matchedPlacementName)
					} else {
						delete(oc.observations, key)
						continue
					}
				} else {
					delete(oc.observations, key)
					continue
				}
			} else {
				klog.V(3).InfoS("[ADMISSION_UID_NO_MAP] Pod has admission-uid annotation but no pending map entry",
					"admissionUID", admissionUIDFromAnnotation,
					"possibleReasons", "already consumed by another watcher, or expired")
			}
		} else {
			klog.V(3).InfoS("[NO_ADMISSION_ANNOTATION] Pod missing admission-uid annotation (legacy or pre-upgrade)",
				"pod", obs.PodNamespace+"/"+obs.PodName,
				"fallback", "using original observation cache logic")
		}

		if !admissionMatched && isPodRunningSuccessfully(pod) {
			klog.InfoS("Pod is running successfully, updating placement status",
				"pod", obs.PodNamespace+"/"+obs.PodName,
				"key", key,
				"reqID", obs.Placement.Spec.ReqID)

			if statusUpdater != nil {
				if err := statusUpdater(
					obs.Placement.Namespace,
					obs.Placement.Name,
					"Completed",
					fmt.Sprintf("Pod %s/%s is running successfully on node %s", obs.PodNamespace, obs.PodName, pod.Spec.NodeName),
				); err != nil {
					klog.ErrorS(err, "Failed to update placement status to Completed",
						"key", key)
				} else {
					delete(oc.observations, key)
					continue
				}
			} else {
				delete(oc.observations, key)
				continue
			}
		}

		obs.RetryCount++
		if obs.RetryCount >= maxRetries {
			klog.V(1).InfoS("Max retries exceeded but pod not ready, marking as failed",
				"key", key,
				"podPhase", pod.Status.Phase,
				"retryCount", obs.RetryCount)

			if statusUpdater != nil {
				if err := statusUpdater(
					obs.Placement.Namespace,
					obs.Placement.Name,
					"Failed",
					fmt.Sprintf("Pod %s/%s stuck in phase %s after %d checks", obs.PodNamespace, obs.PodName, pod.Status.Phase, maxRetries),
				); err != nil {
					klog.ErrorS(err, "Failed to update placement status to Failed",
						"key", key)
				}
			}

			delete(oc.observations, key)
		}
	}
}

// NotFoundError is a custom error type used to indicate Pod not found
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	return e.Message + ": not found"
}

// isNotFoundError checks if it's a resource not found error
func isNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// isPodRunningSuccessfully checks if the Pod is running successfully
func isPodRunningSuccessfully(pod *corev1.Pod) bool {
	switch pod.Status.Phase {
	case corev1.PodRunning:
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	case corev1.PodSucceeded:
		return true
	default:
		return false
	}
}

var globalObservationCache *ObservationCache

// consumedReqIDs records all consumed ReqIDs (globally unique, cross-namespace)
// Used to implement strong consistency idempotency based on ReqID
var (
	consumedReqMu  sync.RWMutex
	consumedReqIDs = make(map[string]ConsumedReqInfo)
)

// pendingAdmissionMap records Admission Request UID mapping relationships waiting for Pod matching
// Core optimization: use admission.UID for precise one-to-one matching, replacing Label Selector traversal
// Memory management strategy: "use and delete immediately" - remove from Map immediately after successful matching to avoid memory leaks
// Exception protection: 5-minute timeout auto-cleanup of unmatched records (handle extreme cases like Pod creation failure)
var (
	admissionMapMu      sync.RWMutex
	pendingAdmissionMap = make(map[string]PendingAdmissionInfo)
)

const (
	admissionMapStaleThreshold = 5 * time.Minute // Timeout for unmatched records
	admissionAnnotationKey     = "hami.io/admission-uid"
)

// PendingAdmissionInfo records information of Admission Request waiting for matching
type PendingAdmissionInfo struct {
	AdmissionUID  string    `json:"admissionUid"`
	PlacementNS   string    `json:"placementNs"`
	PlacementName string    `json:"placementName"`
	PodNamespace  string    `json:"podNamespace"`
	PodName       string    `json:"podName"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ConsumedReqIDTTL defines the expiration time for ReqID records (default 24 hours)
// Records exceeding this time will be automatically cleaned up to prevent unlimited memory and disk growth
const ConsumedReqIDTTL = 24 * time.Hour

// cleanupInterval defines the cleanup task execution interval (default: check once per hour)
const cleanupInterval = 1 * time.Hour

// ConsumedReqInfo records detailed information about ReqID consumption
type ConsumedReqInfo struct {
	ReqID         string
	PlacementNS   string
	PlacementName string
	PodNamespace  string
	PodName       string
	ConsumedAt    time.Time
	Phase         string // "Observing", "Completed", "Failed"
}

// IsReqIDConsumed checks if the ReqID has been consumed
func IsReqIDConsumed(reqID string) bool {
	consumedReqMu.RLock()
	defer consumedReqMu.RUnlock()

	_, exists := consumedReqIDs[reqID]
	return exists
}

// GetReqIDConsumptionInfo gets ReqID consumption details
func GetReqIDConsumptionInfo(reqID string) (ConsumedReqInfo, bool) {
	consumedReqMu.RLock()
	defer consumedReqMu.RUnlock()

	if info, exists := consumedReqIDs[reqID]; exists {
		return info, true
	}
	return ConsumedReqInfo{}, false
}

// MarkReqIDConsumed marks ReqID as consumed (and persists to file simultaneously)
func MarkReqIDConsumed(reqID, placementNS, placementName, podNS, podName, phase string) {
	consumedReqMu.Lock()
	defer consumedReqMu.Unlock()

	now := time.Now()
	if existing, exists := consumedReqIDs[reqID]; exists {
		klog.V(3).InfoS("Updating consumed ReqID info",
			"reqID", reqID,
			"oldPhase", existing.Phase,
			"newPhase", phase)
		existing.Phase = phase
		existing.PodNamespace = podNS
		existing.PodName = podName
		existing.ConsumedAt = now
	} else {
		consumedReqIDs[reqID] = ConsumedReqInfo{
			ReqID:         reqID,
			PlacementNS:   placementNS,
			PlacementName: placementName,
			PodNamespace:  podNS,
			PodName:       podName,
			ConsumedAt:    now,
			Phase:         phase,
		}
		klog.V(3).InfoS("Marked ReqID as consumed",
			"reqID", reqID,
			"placement", placementNS+"/"+placementName,
			"pod", podNS+"/"+podName,
			"phase", phase)
	}

	go func() {
		if err := SaveConsumedReqIDsToFile(); err != nil {
			klog.ErrorS(err, "Failed to persist consumed ReqIDs to file",
				"reqID", reqID)
		}
	}()
}

// UnmarkReqIDConsumed unmarks (only used when failed and retry is needed)
func UnmarkReqIDConsumed(reqID string) {
	consumedReqMu.Lock()
	defer consumedReqMu.Unlock()

	if _, exists := consumedReqIDs[reqID]; exists {
		delete(consumedReqIDs, reqID)
		klog.V(3).InfoS("Unmarked ReqID as consumed (allowed retry)",
			"reqID", reqID)
	}
}

// CleanExpiredConsumedReqIDs cleans up expired ReqID records
// Deletes all records where ConsumedAt exceeds TTL to prevent unlimited memory and disk growth
// Automatically persists to file after cleanup is complete
func CleanExpiredConsumedReqIDs() (int, error) {
	consumedReqMu.Lock()
	defer consumedReqMu.Unlock()

	now := time.Now()
	expiredKeys := make([]string, 0)

	for reqID, info := range consumedReqIDs {
		if now.Sub(info.ConsumedAt) > ConsumedReqIDTTL {
			expiredKeys = append(expiredKeys, reqID)
		}
	}

	if len(expiredKeys) == 0 {
		klog.V(3).InfoS("No expired ReqID records to clean",
			"totalRecords", len(consumedReqIDs),
			"ttl", ConsumedReqIDTTL)
		return 0, nil
	}

	for _, reqID := range expiredKeys {
		info := consumedReqIDs[reqID]
		klog.V(2).InfoS("🗑️ [CLEANUP] Removing expired ReqID record",
			"reqID", reqID,
			"placement", info.PlacementNS+"/"+info.PlacementName,
			"pod", info.PodNamespace+"/"+info.PodName,
			"consumedAt", info.ConsumedAt.Format(time.RFC3339),
			"age", now.Sub(info.ConsumedAt).Round(time.Second),
			"phase", info.Phase)
		delete(consumedReqIDs, reqID)
	}

	cleanedCount := len(expiredKeys)

	klog.InfoS("✅ [CLEANUP_COMPLETE] Cleaned expired ReqID records",
		"cleanedCount", cleanedCount,
		"remainingCount", len(consumedReqIDs),
		"ttl", ConsumedReqIDTTL)

	if err := SaveConsumedReqIDsToFile(); err != nil {
		klog.ErrorS(err, "Failed to persist ReqIDs after cleanup")
		return cleanedCount, err
	}

	return cleanedCount, nil
}

// StartConsumedReqIDCleanup starts a background goroutine for periodic cleanup of expired ReqIDs
// Should be called once at program startup, will execute cleanup every cleanupInterval
func StartConsumedReqIDCleanup(stopCh <-chan struct{}) {
	klog.InfoS("Starting consumed ReqID cleanup goroutine",
		"cleanupInterval", cleanupInterval,
		"ttl", ConsumedReqIDTTL)

	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				klog.InfoS("Stopping consumed ReqID cleanup goroutine")
				return
			case <-ticker.C:
				cleaned, err := CleanExpiredConsumedReqIDs()
				if err != nil {
					klog.ErrorS(err, "Failed to cleanup expired ReqIDs")
				} else if cleaned > 0 {
					klog.V(1).InfoS("Periodic cleanup completed",
						"cleaned", cleaned)
				}
			}
		}
	}()
}

// ListAllConsumedReqIDs lists all consumed ReqIDs (for debugging)
func ListAllConsumedReqIDs() []ConsumedReqInfo {
	consumedReqMu.RLock()
	defer consumedReqMu.RUnlock()

	result := make([]ConsumedReqInfo, 0, len(consumedReqIDs))
	for _, info := range consumedReqIDs {
		result = append(result, info)
	}
	return result
}

// ==================== Admission-UID Mapping Management (Core Optimization) ====================
// The following functions implement precise matching mechanism based on Admission Request UID
// Replaces the original Label Selector traversal approach, providing O(1) lookup performance

// RecordPendingAdmission records the mapping relationship between Admission-UID and Placement in Webhook phase
// Parameters:
//   - admissionUID: unique identifier of AdmissionReview request generated by Kubernetes API Server
//   - placement: matched HamiPodPlacement resource object
//   - podNS: namespace of target Pod
//   - podName: name of target Pod (may be empty if using GenerateName)
//
// Usage scenario:
//
//	Webhook receives CREATE request -> matches Placement -> calls this function to record mapping -> injects annotation into Pod
//
// Memory management:
//
//	Records are kept in Map until one of the following occurs:
//	1. Controller detects Pod creation and calls ConsumeAdmissionMapping for successful matching (normal flow)
//	2. Exceeds admissionMapStaleThreshold time without matching (exception cleanup)
func RecordPendingAdmission(
	admissionUID string,
	placement *placementv1.HamiPodPlacement,
	podNS, podName string,
) {
	admissionMapMu.Lock()
	defer admissionMapMu.Unlock()

	pendingAdmissionMap[admissionUID] = PendingAdmissionInfo{
		AdmissionUID:  admissionUID,
		PlacementNS:   placement.Namespace,
		PlacementName: placement.Name,
		PodNamespace:  podNS,
		PodName:       podName,
		CreatedAt:     time.Now(),
	}

	klog.InfoS("[ADMISSION_MAP_ADD] Recorded pending admission mapping",
		"admissionUID", admissionUID,
		"pod", podNS+"/"+podName,
		"placement", placement.Namespace+"/"+placement.Name,
		"currentMapSize", len(pendingAdmissionMap))
}

// ConsumeAdmissionMapping consumes (finds + deletes) Admission-UID mapping record
// Core optimization: atomic operation, completes find and delete in one call to avoid concurrency issues
//
// Returns:
//   - *PendingAdmissionInfo: found mapping information (if exists)
//   - bool: whether found and successfully consumed
//
// Usage scenario:
//
//	Controller/Observer detects Pod creation event -> gets admission-uid from Pod annotation
//	-> calls this function to get corresponding Placement information -> establishes complete Pod <-> Placement association
//
// Performance characteristics:
//
//	Time complexity O(1), several orders of magnitude faster than Label Selector traversal
func FindPlacementByAdmissionUID(admissionUID string) (*PendingAdmissionInfo, bool) {
	admissionMapMu.RLock()
	defer admissionMapMu.RUnlock()

	info, exists := pendingAdmissionMap[admissionUID]
	if !exists {
		return nil, false
	}
	return &info, true
}

// ConsumeAdmissionMapping consumes an Admission mapping relationship (find + delete)
// Used to find corresponding Placement information via admission-uid in Pod annotation during Controller/Observer phase
func ConsumeAdmissionMapping(admissionUID string) (*PendingAdmissionInfo, bool) {
	admissionMapMu.Lock()
	defer admissionMapMu.Unlock()

	info, exists := pendingAdmissionMap[admissionUID]
	if !exists {
		klog.V(3).InfoS("[ADMISSION_MAP_MISS] Admission mapping not found",
			"admissionUID", admissionUID,
			"possibleReasons", "already consumed, never recorded, or expired")
		return nil, false
	}

	delete(pendingAdmissionMap, admissionUID)

	klog.InfoS("[ADMISSION_MAP_CONSUMED] Successfully consumed admission mapping",
		"admissionUID", admissionUID,
		"placement", info.PlacementNS+"/"+info.PlacementName,
		"pod", info.PodNamespace+"/"+info.PodName,
		"remainingMapSize", len(pendingAdmissionMap),
		"lifetime", time.Since(info.CreatedAt).Round(time.Millisecond))

	return &info, true
}

// GetPendingMapSize gets the number of Admission records currently waiting for matching (for monitoring)
func GetPendingMapSize() int {
	admissionMapMu.RLock()
	defer admissionMapMu.RUnlock()
	return len(pendingAdmissionMap)
}

// CleanupStaleAdmissionMappings cleans up Admission records that have timed out without matching
// Exception protection mechanism: handles extreme cases like Pod creation failure, Webhook restart, etc.
// Normally, most records should be actively consumed through ConsumeAdmissionMapping rather than cleaned up by this function
//
// Cleanup conditions:
//
//	Record creation time exceeds admissionMapStaleThreshold (default 5 minutes) and status is still pending
//
// Returns:
//   - int: number of records cleaned
//   - error: error information (currently always returns nil)
func CleanupStaleAdmissionMappings() (int, error) {
	admissionMapMu.Lock()
	defer admissionMapMu.Unlock()

	now := time.Now()
	var staleKeys []string

	for uid, info := range pendingAdmissionMap {
		if now.Sub(info.CreatedAt) > admissionMapStaleThreshold {
			staleKeys = append(staleKeys, uid)
		}
	}

	if len(staleKeys) == 0 {
		return 0, nil
	}

	for _, uid := range staleKeys {
		info := pendingAdmissionMap[uid]
		klog.InfoS("[ADMISSION_MAP_STALE_CLEANUP] Removing stale admission mapping",
			"admissionUID", uid,
			"pod", info.PodNamespace+"/"+info.PodName,
			"placement", info.PlacementNS+"/"+info.PlacementName,
			"age", now.Sub(info.CreatedAt).Round(time.Second),
			"reason", "Pod creation likely failed or webhook restarted")

		delete(pendingAdmissionMap, uid)
	}

	cleanedCount := len(staleKeys)

	klog.InfoS("[ADMISSION_MAP_CLEANUP_COMPLETE] Cleaned stale admission mappings",
		"cleanedCount", cleanedCount,
		"remainingCount", len(pendingAdmissionMap),
		"staleThreshold", admissionMapStaleThreshold)

	return cleanedCount, nil
}

// GetAdmissionAnnotationKey returns the key name injected into Pod annotations
// Unifies management of annotation keys to avoid scattered hardcoding
func GetAdmissionAnnotationKey() string {
	return admissionAnnotationKey
}

// InitGlobalObservationCache initializes global observation cache instance
func InitGlobalObservationCache(client kubernetes.Interface) *ObservationCache {
	if globalObservationCache == nil {
		globalObservationCache = NewObservationCache(client)
		klog.InfoS("Global observation cache initialized")
	}
	return globalObservationCache
}

// GetGlobalObservationCache gets global observation cache instance
func GetGlobalObservationCache() *ObservationCache {
	if globalObservationCache == nil {
		klog.InfoS("Global observation cache not initialized, creating without k8s client")
		return NewObservationCache(nil)
	}
	return globalObservationCache
}

// ==================== Global Cache Manager ====================

// globalCacheManager globally unique cache instance
var globalCacheManager *PlacementCache

// InitGlobalCache initializes global cache instance (should be called once at program startup)
func InitGlobalCache() *PlacementCache {
	if globalCacheManager == nil {
		globalCacheManager = NewPlacementCache()
		klog.InfoS("Global placement cache initialized")
	}
	return globalCacheManager
}

// GetGlobalCache gets global cache instance (auto-creates if not initialized)
func GetGlobalCache() *PlacementCache {
	if globalCacheManager == nil {
		return InitGlobalCache()
	}
	return globalCacheManager
}

// ==================== Convenience Functions for Webhook ====================

// FindPlacementForPod finds matching deployment instructions based on Pod object (enhanced version)
// This is the main entry function called by Webhook
//
// Key features:
//   - Idempotency guarantee: after successful matching, placement moves from PlacementCache to ObservationCache to prevent reuse
//   - Status tracking: records the binding relationship between placement and pod for subsequent monitoring
//   - Auto-cleanup: automatically detects deployment results and updates CRD status via ObservationCache background goroutine
//
// Parameters:
//   - pod: Kubernetes Pod object pointer (must contain Namespace, Name, Labels information)
//
// Returns:
//   - []*placementv1.HamiPodPlacement: list of all matched deployment instructions (sorted by priority)
//   - bool: whether at least one matching instruction was found
//
// Usage example:
//
//	pod := &corev1.Pod{
//	    ObjectMeta: metav1.ObjectMeta{
//	        Namespace: "yixin",
//	        Name:      "my-pod-xxx",
//	        Labels:    map[string]string{"app": "app-2018239454967238656-sh"},
//	    },
//	}
//	placements, found := cache.FindPlacementForPod(pod)
//	if found {
//	    for _, pl := range placements {
//	        // Use pl.Spec.NodeName, pl.Spec.GPUDevices, etc.
//
// At this point, the placement has been moved to observation cache and won't match again
//
//	    }
//	}
func FindPlacementForPod(pod *corev1.Pod) (*placementv1.HamiPodPlacement, bool) {
	cache := GetGlobalCache()
	obsCache := GetGlobalObservationCache()

	namespace := pod.Namespace
	podName := pod.Name
	podLabels := pod.Labels

	matched := cache.FindMatchingPlacements(namespace, podLabels)

	if len(matched) > 0 {
		pl := matched[0]

		klog.V(4).InfoS("Checking first placement for ReqID idempotency",
			"name", pl.Name,
			"reqID", pl.Spec.ReqID)

		if IsReqIDConsumed(pl.Spec.ReqID) {
			existingInfo, _ := GetReqIDConsumptionInfo(pl.Spec.ReqID)
			klog.V(2).InfoS("First placement already consumed (idempotency enforcement)",
				"name", pl.Name,
				"reqID", pl.Spec.ReqID,
				"consumedBy", existingInfo.PodNamespace+"/"+existingInfo.PodName,
				"consumedAt", existingInfo.ConsumedAt,
				"currentPhase", existingInfo.Phase)
			return nil, false
		}

		klog.InfoS("[PLACEMENT_CONSUMED] Found and consuming placement (single-use guarantee)",
			"placement", pl.Namespace+"/"+pl.Name,
			"pod", namespace+"/"+podName,
			"reqID", pl.Spec.ReqID,
			"nodeName", pl.Spec.NodeName,
			"gpuDevices", pl.Spec.GPUDevices,
			"action", "Removing from active cache to prevent reuse")

		klog.V(3).InfoS("Moving placement from active cache to observation cache (ensuring idempotency)",
			"placement", pl.Namespace+"/"+pl.Name,
			"pod", namespace+"/"+podName,
			"reqID", pl.Spec.ReqID)

		cache.Delete(pl.Namespace, pl.Name)
		obsCache.Add(pl, namespace, podName)

		MarkReqIDConsumed(
			pl.Spec.ReqID,
			pl.Namespace,
			pl.Name,
			namespace,
			podName,
			"Observing",
		)

		return pl, true
	}

	klog.V(4).InfoS("No matching placement found for pod",
		"namespace", namespace,
		"podName", podName,
		"podLabels", podLabels)

	return nil, false
}

// FindFirstPlacementForPod finds the first matching deployment instruction (shortcut method)
// Use this method when only need to get the first matching record
//
// Parameters:
//   - pod: Kubernetes Pod object pointer
//
// Returns:
//   - *placementv1.HamiPodPlacement: the matched unique deployment instruction (or nil)
//   - bool: whether found
//
// Key features:
//   - Singleton return: returns only one Placement each time to avoid reuse
//   - Immediate consumption: moves from active cache to observation cache immediately after finding
//   - Idempotency guarantee: ensures same ReqID can only be used once through ReqID consumption records
//   - Natural deduplication: cache layer control, no need for additional status checks at Webhook layer

// HasActivePlacement checks if there are active deployment instructions for the specified Pod
// Does not return specific data, only used for quick judgment (better performance)
// Note: This function does not trigger cache movement operations, only queries
func HasActivePlacement(pod *corev1.Pod) bool {
	cache := GetGlobalCache()

	matched := cache.FindMatchingPlacements(pod.Namespace, pod.Labels)
	return len(matched) > 0
}
