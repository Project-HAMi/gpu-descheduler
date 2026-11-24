/*
Copyright 2026 The HAMi Authors.

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

package defrag

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

// Inputs are the objects a snapshot is built from.
type Inputs struct {
	Nodes      []corev1.Node
	Pods       []corev1.Pod
	PDBs       []policyv1.PodDisruptionBudget
	Placements []placementv1.HamiPodPlacement
	Now        time.Time
}

// GPU is one NVIDIA GPU registered by HAMi.
type GPU struct {
	Ref        GPURef
	Type       string
	Mode       string
	Healthy    bool
	MemTotal   int64
	CoreTotal  int64
	ShareLimit int64
	// busy is set while a HamiPodPlacement targets the GPU, so nothing else
	// is planned onto it or off it.
	busy bool
	node *corev1.Node
}

// podAlloc is a pod that holds exactly one GPU, or one GPU of several.
type podAlloc struct {
	key      string
	uid      string
	ownerUID string
	pod      *corev1.Pod
	gpu      int
	memMB    int64
	cores    int64
	req      request
	movable  bool
	pdbs     []string
}

// pendingPod is a Pending HAMi pod that asks for one GPU.
type pendingPod struct {
	key   string
	uid   string
	pod   *corev1.Pod
	req   request
	since time.Time
}

type usage struct {
	mem   int64
	cores int64
	count int64
}

// Snapshot is a consistent view of GPUs, the pods on them and Pending pods.
// The planner only reads it, so it can be tested without a cluster.
type Snapshot struct {
	gpus    []*GPU
	byUUID  map[string]int
	allocs  []*podAlloc
	onGPU   [][]int
	base    []usage
	pending []*pendingPod
	// pdbAllowed is the number of disruptions each PDB still allows.
	pdbAllowed map[string]int32
	skipped    []Skipped
	// inFlight is set while a placement made for a defrag plan has not
	// finished, so a new plan is not started on top of it.
	inFlight bool
	accepts  map[acceptKey]bool
}

type acceptKey struct {
	pod  *corev1.Pod
	node *corev1.Node
}

// BuildSnapshot builds a snapshot from in.
func BuildSnapshot(in Inputs, opts Options) *Snapshot {
	s := &Snapshot{
		byUUID:     map[string]int{},
		pdbAllowed: map[string]int32{},
		accepts:    map[acceptKey]bool{},
	}
	s.addGPUs(in.Nodes)
	pods := map[string]*corev1.Pod{}
	for i := range in.Pods {
		pods[in.Pods[i].Namespace+"/"+in.Pods[i].Name] = &in.Pods[i]
	}
	cooldown := s.readPlacements(in.Placements, pods, in.Now, opts)
	pdbSelectors := s.readPDBs(in.PDBs)
	for i := range in.Pods {
		s.addPod(&in.Pods[i], pdbSelectors, cooldown, in.Now, opts)
	}
	sort.SliceStable(s.pending, func(i, j int) bool {
		if !s.pending[i].since.Equal(s.pending[j].since) {
			return s.pending[i].since.Before(s.pending[j].since)
		}
		return s.pending[i].key < s.pending[j].key
	})
	return s
}

func (s *Snapshot) addGPUs(nodes []corev1.Node) {
	for i := range nodes {
		node := &nodes[i]
		value, ok := node.Annotations[nodeRegisterAnnotation]
		if !ok {
			continue
		}
		registered, err := parseRegisteredGPUs(value)
		if err != nil {
			klog.V(3).InfoS("Skipping node with an unreadable HAMi GPU registration", "node", node.Name, "err", err)
			continue
		}
		for _, r := range registered {
			s.gpus = append(s.gpus, &GPU{
				Ref:        GPURef{Node: node.Name, UUID: r.ID, Index: r.Index},
				Type:       r.Type,
				Mode:       r.Mode,
				Healthy:    r.Health,
				MemTotal:   r.Devmem,
				CoreTotal:  r.Devcore,
				ShareLimit: r.Count,
				node:       node,
			})
		}
	}
	sort.SliceStable(s.gpus, func(i, j int) bool {
		a, b := s.gpus[i].Ref, s.gpus[j].Ref
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.Index < b.Index
	})
	s.onGPU = make([][]int, len(s.gpus))
	s.base = make([]usage, len(s.gpus))
	for i, g := range s.gpus {
		s.byUUID[g.Ref.UUID] = i
	}
}

// readPlacements marks the GPUs that unfinished placements target as busy,
// and returns the owners still in cooldown after a defrag move.
func (s *Snapshot) readPlacements(placements []placementv1.HamiPodPlacement, pods map[string]*corev1.Pod, now time.Time, opts Options) map[string]bool {
	cooldown := map[string]bool{}
	for i := range placements {
		pl := &placements[i]
		_, isDefrag := pl.Labels[PlanLabel]
		age := now.Sub(pl.CreationTimestamp.Time)
		if owner := pl.Labels[OwnerLabel]; isDefrag && owner != "" && age < opts.Cooldown {
			cooldown[owner] = true
		}
		if placementDone(pl, pods) || (isDefrag && (pl.Labels[DoneLabel] == "true" || age > 2*opts.MoveTimeout)) {
			continue
		}
		if isDefrag {
			s.inFlight = true
		}
		for _, spec := range pl.Spec.GPUDevices {
			index, err := deviceIndex(spec)
			if err != nil {
				continue
			}
			for _, g := range s.gpus {
				if g.Ref.Node == pl.Spec.NodeName && g.Ref.Index == index {
					g.busy = true
				}
			}
		}
	}
	return cooldown
}

// placementDone reports whether a placement no longer reserves its target:
// it failed or completed, or the pod it matched already holds its GPU.
func placementDone(pl *placementv1.HamiPodPlacement, pods map[string]*corev1.Pod) bool {
	switch pl.Status.Phase {
	case "Completed", "Failed":
		return true
	case "Matched":
		pod, ok := pods[pl.Namespace+"/"+pl.Status.MatchedPodName]
		return !ok || pod.Annotations[podAllocatedAnnotation] != "" || podFinished(pod)
	}
	return false
}

// deviceIndex parses a placement device spec, "gpu-<index>[/<cores>]".
func deviceIndex(spec string) (int, error) {
	index, _, _ := strings.Cut(strings.TrimSpace(spec), "/")
	if !strings.HasPrefix(index, "gpu-") {
		return 0, errors.New("missing gpu- prefix")
	}
	return strconv.Atoi(strings.TrimPrefix(index, "gpu-"))
}

type pdbSelector struct {
	key       string
	namespace string
	selector  labels.Selector
}

func (s *Snapshot) readPDBs(pdbs []policyv1.PodDisruptionBudget) []pdbSelector {
	var out []pdbSelector
	for i := range pdbs {
		pdb := &pdbs[i]
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || sel.Empty() {
			continue
		}
		key := pdb.Namespace + "/" + pdb.Name
		s.pdbAllowed[key] = pdb.Status.DisruptionsAllowed
		out = append(out, pdbSelector{key: key, namespace: pdb.Namespace, selector: sel})
	}
	return out
}

func podFinished(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (s *Snapshot) addPod(pod *corev1.Pod, pdbs []pdbSelector, cooldown map[string]bool, now time.Time, opts Options) {
	if podFinished(pod) {
		return
	}
	key := pod.Namespace + "/" + pod.Name
	if value := pod.Annotations[podAllocatedAnnotation]; value != "" {
		allocs, containers, err := parseAllocations(value)
		if err != nil {
			klog.V(3).InfoS("Skipping pod with an unreadable HAMi allocation", "pod", key, "err", err)
			return
		}
		var held []*podAlloc
		for _, a := range allocs {
			g, ok := s.byUUID[a.uuid]
			if !ok {
				continue
			}
			pa := &podAlloc{key: key, uid: string(pod.UID), pod: pod, gpu: g, memMB: a.memMB, cores: a.cores}
			if owner := metav1.GetControllerOf(pod); owner != nil {
				pa.ownerUID = string(owner.UID)
			}
			s.base[g].mem += a.memMB
			s.base[g].cores += a.cores
			s.base[g].count++
			s.onGPU[g] = append(s.onGPU[g], len(s.allocs))
			s.allocs = append(s.allocs, pa)
			held = append(held, pa)
		}
		if len(held) == 1 && len(allocs) == 1 && containers == 1 {
			s.checkMovable(held[0], pdbs, cooldown, opts)
		} else if len(held) > 0 && pod.Labels[OptInLabel] == "true" {
			s.skip(key, "uses more than one GPU, which is never split")
		}
		return
	}
	if pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil || pod.Spec.SchedulerName != hamiSchedulerName {
		return
	}
	since, ok := unschedulableSince(pod)
	if !ok || now.Sub(since) < opts.MinPendingAge {
		return
	}
	req, err := parseRequest(pod, opts)
	switch {
	case errors.Is(err, errNoGPU):
		return
	case err != nil:
		s.skip(key, "pending pod "+err.Error())
		return
	case numaBound(pod.Annotations):
		s.skip(key, "pending pod uses "+numaBindAnnotation+", which is not modelled")
		return
	}
	s.pending = append(s.pending, &pendingPod{key: key, uid: string(pod.UID), pod: pod, req: req, since: since})
}

// unschedulableSince returns when kube-scheduler last reported the pod as
// unschedulable.
func unschedulableSince(pod *corev1.Pod) (time.Time, bool) {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}

// checkMovable decides whether pa may be moved. Only pods that opted in are
// considered, and those that cannot move are reported with the reason.
func (s *Snapshot) checkMovable(pa *podAlloc, pdbs []pdbSelector, cooldown map[string]bool, opts Options) {
	pod := pa.pod
	if pod.Labels[OptInLabel] != "true" {
		return
	}
	reason := ""
	owner := metav1.GetControllerOf(pod)
	req, err := parseRequest(pod, opts)
	switch {
	case len(opts.Namespaces) > 0 && !slices.Contains(opts.Namespaces, pod.Namespace):
		reason = "namespace is not in the defrag namespace list"
	case owner == nil:
		reason = "has no controller to recreate it"
	case owner.Kind != "ReplicaSet" && owner.Kind != "StatefulSet" && owner.Kind != "Job":
		reason = "is owned by a " + owner.Kind + "; only ReplicaSet, StatefulSet and Job pods are moved"
	case pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" || pod.Status.Phase != corev1.PodRunning || !podReady(pod):
		reason = "is not running and ready"
	case err != nil:
		reason = err.Error()
	case numaBound(pod.Annotations):
		reason = "uses " + numaBindAnnotation + ", which is not modelled"
	case strings.EqualFold(s.gpus[pa.gpu].Mode, migMode):
		reason = "runs on a MIG GPU, which is not modelled"
	case cooldown[pa.ownerUID]:
		reason = "its workload was moved less than the cooldown ago"
	}
	if reason == "" {
		podLabels := labels.Set(pod.Labels)
		for _, p := range pdbs {
			if p.namespace == pod.Namespace && p.selector.Matches(podLabels) {
				pa.pdbs = append(pa.pdbs, p.key)
				if s.pdbAllowed[p.key] < 1 {
					reason = "its PodDisruptionBudget " + p.key + " allows no disruption"
				}
			}
		}
	}
	if reason != "" {
		s.skip(pa.key, reason)
		return
	}
	pa.req = req
	pa.movable = true
}

func (s *Snapshot) skip(pod, reason string) {
	s.skipped = append(s.skipped, Skipped{Pod: pod, Reason: reason})
}

// InFlight reports whether a defrag placement is still in progress.
func (s *Snapshot) InFlight() bool { return s.inFlight }

// Skipped returns the pods left out of planning, with the reason.
func (s *Snapshot) Skipped() []Skipped { return s.skipped }
