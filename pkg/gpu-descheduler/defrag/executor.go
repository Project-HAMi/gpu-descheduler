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
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
)

// Executor carries out a plan one move at a time. Each move is one
// HamiPodPlacement: the placement controller evicts the pod, and the webhook
// pins the replacement that the pod's controller creates to the planned GPU.
type Executor struct {
	Client client.Client
	// Owners reads the controllers of moved pods. It may read the API
	// server directly, so that no informer is needed for them.
	Owners client.Reader
	Opts   Options
	// Refresh returns a snapshot of the current cluster.
	Refresh func(ctx context.Context) (*Snapshot, error)
	// Poll is how often progress is checked.
	Poll time.Duration
	// Now returns the current time.
	Now func() time.Time
}

// Execute carries out res.Recommended and reports how it went. progress is
// called after every completed move.
func (e *Executor) Execute(ctx context.Context, res Result, progress func(Execution)) Execution {
	plan := res.Recommended
	exec := Execution{PlanID: plan.ID, Pod: res.Pod, Result: ExecRunning, StartedAt: e.Now()}
	finish := func(result, format string, args ...any) Execution {
		now := e.Now()
		exec.Result, exec.Message, exec.FinishedAt = result, fmt.Sprintf(format, args...), &now
		executionsTotal.WithLabelValues(result).Inc()
		klog.InfoS("Finished a defrag plan", "plan", plan.ID, "pod", res.Pod, "result", result, "message", exec.Message, "movesDone", exec.MovesDone)
		return exec
	}
	klog.InfoS("Starting a defrag plan", "plan", plan.ID, "pod", res.Pod, "moves", len(plan.Moves), "frees", plan.Frees.String())
	for i, mv := range plan.Moves {
		snap, err := e.Refresh(ctx)
		if err != nil {
			return finish(ExecAborted, "could not read the cluster before move %d: %v", i+1, err)
		}
		pod, err := snap.Verify(plan, res.PodUID, i, e.Opts)
		if err != nil {
			if i == 0 {
				return finish(ExecStale, "plan no longer holds: %v", err)
			}
			return finish(ExecAborted, "plan no longer holds before move %d: %v", i+1, err)
		}
		if err := e.move(ctx, plan, i, mv, pod, exec.StartedAt); err != nil {
			movesTotal.WithLabelValues("failed").Inc()
			return finish(ExecAborted, "move %d (%s to %s): %v", i+1, mv.Pod, mv.To, err)
		}
		movesTotal.WithLabelValues("succeeded").Inc()
		exec.MovesDone++
		if progress != nil {
			progress(exec)
		}
	}
	return e.waitForPendingPod(ctx, res, finish)
}

// move creates the placement for move i and waits until the replacement pod
// holds the target GPU and is ready.
func (e *Executor) move(ctx context.Context, plan *Plan, i int, mv Move, pod *corev1.Pod, started time.Time) error {
	name := fmt.Sprintf("defrag-%s-%d-%d", plan.ID, started.Unix(), i+1)
	selector, err := e.replacementSelector(ctx, pod)
	if err != nil {
		return err
	}
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pod.Namespace,
			Labels:    map[string]string{PlanLabel: plan.ID, OwnerLabel: mv.OwnerUID},
		},
		Spec: placementv1.HamiPodPlacementSpec{
			ReqID:       name,
			PodSelector: selector,
			NodeName:    mv.To.Node,
			GPUDevices:  []string{fmt.Sprintf("gpu-%d", mv.To.Index)},
			EvictPod:    pod.Name,
		},
	}
	if err := e.Client.Create(ctx, pl); err != nil {
		return fmt.Errorf("creating placement %s: %w", name, err)
	}
	klog.InfoS("Created a defrag placement", "placement", klog.KObj(pl), "pod", mv.Pod, "to", mv.To.String())
	// The placement's own status is not used: the pod that replaces the
	// evicted one is found directly, by its controller and the GPU pin the
	// webhook injected.
	err = e.until(ctx, func() (bool, error) {
		var cur placementv1.HamiPodPlacement
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(pl), &cur); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if cur.Status.Phase == "Failed" {
			return false, fmt.Errorf("placement %s failed: %s", name, cur.Status.Message)
		}
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace(pod.Namespace)); err != nil {
			return false, err
		}
		for i := range pods.Items {
			repl := &pods.Items[i]
			owner := metav1.GetControllerOf(repl)
			if repl.UID == pod.UID || owner == nil || string(owner.UID) != mv.OwnerUID ||
				repl.CreationTimestamp.Before(&pl.CreationTimestamp) ||
				!strings.HasPrefix(repl.Annotations[placementAnnotation], mv.To.UUID+",") {
				continue
			}
			if podFinished(repl) {
				return false, fmt.Errorf("replacement pod %s finished with phase %s", repl.Name, repl.Status.Phase)
			}
			value := repl.Annotations[podAllocatedAnnotation]
			if value != "" && !strings.Contains(value, mv.To.UUID+",") {
				return false, fmt.Errorf("replacement pod %s got another GPU: %s", repl.Name, value)
			}
			return value != "" && repl.Status.Phase == corev1.PodRunning && podReady(repl), nil
		}
		return false, nil
	}, "the replacement pod to run on "+mv.To.String())
	if err != nil {
		e.dropUnused(ctx, pl)
		return err
	}
	e.markDone(ctx, pl)
	return nil
}

// markDone labels a placement whose move finished, so that its GPU is no
// longer held for it.
func (e *Executor) markDone(ctx context.Context, pl *placementv1.HamiPodPlacement) {
	patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"`+DoneLabel+`":"true"}}}`))
	if err := e.Client.Patch(ctx, pl, patch); err != nil {
		klog.ErrorS(err, "Could not mark a defrag placement as done", "placement", klog.KObj(pl))
	}
}

// dropUnused deletes a placement that no pod has taken yet, so that it does
// not pin a pod created later.
func (e *Executor) dropUnused(ctx context.Context, pl *placementv1.HamiPodPlacement) {
	if cache.IsReqIDConsumed(pl.Spec.ReqID) {
		return
	}
	if err := e.Client.Delete(ctx, pl); client.IgnoreNotFound(err) != nil {
		klog.ErrorS(err, "Could not delete an unused defrag placement", "placement", klog.KObj(pl))
	}
}

// replacementSelector returns a selector for the pod that pod's controller
// creates in its place. It is the controller's own selector, since a running
// pod carries labels added after it was created, such as hami.io/vgpu-node,
// that its replacement does not have yet. A StatefulSet replaces a pod under
// the same name, so its pod-name label narrows the selector to that pod.
func (e *Executor) replacementSelector(ctx context.Context, pod *corev1.Pod) (metav1.LabelSelector, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return metav1.LabelSelector{}, fmt.Errorf("pod %s has no controller", pod.Name)
	}
	key := types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}
	var obj client.Object
	var selector func() *metav1.LabelSelector
	switch owner.Kind {
	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		obj, selector = rs, func() *metav1.LabelSelector { return rs.Spec.Selector }
	case "StatefulSet":
		sts := &appsv1.StatefulSet{}
		obj, selector = sts, func() *metav1.LabelSelector { return sts.Spec.Selector }
	case "Job":
		job := &batchv1.Job{}
		obj, selector = job, func() *metav1.LabelSelector { return job.Spec.Selector }
	default:
		return metav1.LabelSelector{}, fmt.Errorf("controller kind %s is not supported", owner.Kind)
	}
	if err := e.Owners.Get(ctx, key, obj); err != nil {
		return metav1.LabelSelector{}, fmt.Errorf("reading %s %s: %w", owner.Kind, owner.Name, err)
	}
	if obj.GetUID() != owner.UID || selector() == nil {
		return metav1.LabelSelector{}, fmt.Errorf("%s %s was replaced or has no selector", owner.Kind, owner.Name)
	}
	sel := *selector().DeepCopy()
	if owner.Kind == "StatefulSet" {
		sel.MatchLabels = maps.Clone(sel.MatchLabels)
		if sel.MatchLabels == nil {
			sel.MatchLabels = map[string]string{}
		}
		sel.MatchLabels[appsv1.StatefulSetPodNameLabel] = pod.Name
	}
	return sel, nil
}

// waitForPendingPod waits for the pod the plan was made for to be scheduled.
func (e *Executor) waitForPendingPod(ctx context.Context, res Result, finish func(string, string, ...any) Execution) Execution {
	ns, name, _ := strings.Cut(res.Pod, "/")
	var bound string
	err := e.until(ctx, func() (bool, error) {
		var pod corev1.Pod
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("pod was deleted")
			}
			return false, err
		}
		if string(pod.UID) != res.PodUID {
			return false, fmt.Errorf("pod was replaced")
		}
		bound = pod.Spec.NodeName
		return bound != "", nil
	}, "the pod to be scheduled")
	if err == nil {
		return finish(ExecSucceeded, "pod was scheduled to %s", bound)
	}
	snap, serr := e.Refresh(ctx)
	if serr == nil {
		if _, verr := snap.Verify(res.Recommended, res.PodUID, len(res.Recommended.Moves), e.Opts); verr != nil && !strings.Contains(verr.Error(), "no longer unschedulable") {
			return finish(ExecRaced, "moves done, but the freed space was taken before the pod was scheduled: %v", verr)
		}
	}
	return finish(ExecTimeout, "moves done, but %v", err)
}

// until polls cond until it is true, fails, or MoveTimeout passes.
func (e *Executor) until(ctx context.Context, cond func() (bool, error), what string) error {
	ctx, cancel := context.WithTimeout(ctx, e.Opts.MoveTimeout)
	defer cancel()
	ticker := time.NewTicker(e.Poll)
	defer ticker.Stop()
	for {
		done, err := cond()
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %s", e.Opts.MoveTimeout, what)
		case <-ticker.C:
		}
	}
}
