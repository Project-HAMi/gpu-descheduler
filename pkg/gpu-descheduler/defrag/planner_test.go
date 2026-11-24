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
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

func movesOf(p *Plan) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, m := range p.Moves {
		out = append(out, fmt.Sprintf("%s>%s", m.Pod, m.To))
	}
	return out
}

func onlyResult(t *testing.T, results []Result) Result {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	return results[0]
}

// The example of issue #3483: three 36 GB GPUs, 52 GB free in total, and a
// pod that needs 30 GB. Moving the 8 GB pod frees a whole GPU.
func TestPlanIssueExample(t *testing.T) {
	in := Inputs{
		Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb})},
		Pods: []corev1.Pod{
			runningPod("task1", "n1", 0, 24*gb, 0),
			runningPod("task2", "n1", 1, 24*gb, 0),
			runningPod("task3", "n1", 2, 8*gb, 0),
			pendingPodFor("new", 30*gb, 0),
		},
	}
	_, results := plan(in, DefaultOptions())
	r := onlyResult(t, results)
	if r.Outcome != OutcomePlanned {
		t.Fatalf("outcome %q (%s), want planned", r.Outcome, r.Message)
	}
	got := movesOf(r.Recommended)
	if len(got) != 1 || got[0] != "default/task3>n1/gpu-0" || r.Recommended.Frees.Index != 2 {
		t.Fatalf("recommended %v freeing %s, want task3 to gpu-0 freeing gpu-2", got, r.Recommended.Frees)
	}
	if r.Recommended.FreeAfterMB != 36*gb || r.Recommended.MemoryMovedMB != 8*gb {
		t.Fatalf("free after %d, moved %d", r.Recommended.FreeAfterMB, r.Recommended.MemoryMovedMB)
	}
}

// The larger example from the design doc, with cores and a pinned bare pod.
func TestPlanRanksAlternatives(t *testing.T) {
	bare := runningPod("task5", "node-a", 1, 4*gb, 10)
	bare.OwnerReferences = nil
	in := Inputs{
		Nodes: []corev1.Node{
			testNode("node-a", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb}),
			testNode("node-b", gpuSpec{memMB: 36 * gb}),
		},
		Pods: []corev1.Pod{
			runningPod("task1", "node-a", 0, 24*gb, 30),
			runningPod("task2", "node-a", 1, 20*gb, 30),
			bare,
			runningPod("task3", "node-b", 0, 8*gb, 20),
			runningPod("task4", "node-b", 0, 6*gb, 10),
			pendingPodFor("new-job", 30*gb, 50),
		},
	}
	s, results := plan(in, testOptions())
	r := onlyResult(t, results)
	if r.Outcome != OutcomePlanned {
		t.Fatalf("outcome %q (%s)", r.Outcome, r.Message)
	}
	if got := movesOf(r.Recommended); len(got) != 1 || got[0] != "default/task3>node-a/gpu-0" {
		t.Fatalf("recommended %v, want task3 to node-a/gpu-0", got)
	}
	if len(r.Alternatives) != 3 {
		t.Fatalf("got %d alternatives, want 3", len(r.Alternatives))
	}
	if got := movesOf(&r.Alternatives[0]); len(got) != 1 || got[0] != "default/task2>node-b/gpu-0" {
		t.Fatalf("first alternative %v, want task2 to node-b/gpu-0", got)
	}
	for _, alt := range r.Alternatives[1:] {
		if len(alt.Moves) != 2 {
			t.Fatalf("later alternatives must have two moves: %v", movesOf(&alt))
		}
	}
	if len(s.Skipped()) != 1 || s.Skipped()[0].Pod != "default/task5" {
		t.Fatalf("skipped %+v, want the bare pod task5", s.Skipped())
	}
}

func TestPlanFitsWithoutMoves(t *testing.T) {
	in := Inputs{
		Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb})},
		Pods:  []corev1.Pod{runningPod("task1", "n1", 0, 4*gb, 0), pendingPodFor("new", 30*gb, 0)},
	}
	_, results := plan(in, DefaultOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomeFits || r.Recommended != nil {
		t.Fatalf("got %+v, want fits without a plan", r)
	}
}

func TestPlanSameNodeScope(t *testing.T) {
	in := Inputs{
		Nodes: []corev1.Node{testNode("a", gpuSpec{memMB: 36 * gb}), testNode("b", gpuSpec{memMB: 36 * gb})},
		Pods: []corev1.Pod{
			runningPod("task1", "a", 0, 24*gb, 0),
			runningPod("task2", "b", 0, 8*gb, 0),
			pendingPodFor("new", 30*gb, 0),
		},
	}
	_, results := plan(in, DefaultOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomeNoPlan {
		t.Fatalf("same-node scope found %+v, want no plan", r.Recommended)
	}
	_, results = plan(in, testOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomePlanned || movesOf(r.Recommended)[0] != "default/task2>a/gpu-0" {
		t.Fatalf("cross-node scope got %+v", r)
	}
}

// Pods that may not move are left alone, and the reason is reported.
func TestPlanSafetyRules(t *testing.T) {
	nodes := []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb})}
	// Only small may move: big has not opted in.
	base := func() []corev1.Pod {
		big := runningPod("big", "n1", 0, 30*gb, 0)
		delete(big.Labels, OptInLabel)
		return []corev1.Pod{big, runningPod("small", "n1", 1, 4*gb, 0), pendingPodFor("new", 33*gb, 0)}
	}
	cases := map[string]struct {
		mutate     func(pods []corev1.Pod)
		pdbs       []policyv1.PodDisruptionBudget
		placements []placementv1.HamiPodPlacement
		opts       func(*Options)
		reason     string
	}{
		"not opted in": {mutate: func(p []corev1.Pod) { delete(p[1].Labels, OptInLabel) }},
		"bare pod":     {mutate: func(p []corev1.Pod) { p[1].OwnerReferences = nil }, reason: "has no controller to recreate it"},
		"daemonset": {
			mutate: func(p []corev1.Pod) { p[1].OwnerReferences[0].Kind = "DaemonSet" },
			reason: "is owned by a DaemonSet; only ReplicaSet, StatefulSet and Job pods are moved",
		},
		"not ready": {
			mutate: func(p []corev1.Pod) { p[1].Status.Conditions[0].Status = corev1.ConditionFalse },
			reason: "is not running and ready",
		},
		"namespace": {opts: func(o *Options) { o.Namespaces = []string{"prod"} }, reason: "namespace is not in the defrag namespace list"},
		"pdb": {
			pdbs: []policyv1.PodDisruptionBudget{{
				ObjectMeta: metav1.ObjectMeta{Name: "small", Namespace: "default"},
				Spec: policyv1.PodDisruptionBudgetSpec{
					MinAvailable: ptrIntStr(1),
					Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "small"}},
				},
				Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
			}},
			reason: "its PodDisruptionBudget default/small allows no disruption",
		},
		"cooldown": {
			placements: []placementv1.HamiPodPlacement{{ObjectMeta: metav1.ObjectMeta{
				Name: "defrag-old-1", Namespace: "default", CreationTimestamp: metav1.NewTime(testNow.Add(-10 * time.Minute)),
				Labels: map[string]string{PlanLabel: "old", OwnerLabel: "owner-small"},
			}, Status: placementv1.HamiPodPlacementStatus{Phase: "Completed"}}},
			reason: "its workload was moved less than the cooldown ago",
		},
		"numa bind": {
			mutate: func(p []corev1.Pod) { p[1].Annotations[numaBindAnnotation] = "true" },
			reason: "uses nvidia.com/numa-bind, which is not modelled",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pods := base()
			if tc.mutate != nil {
				tc.mutate(pods)
			}
			opts := DefaultOptions()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			s, results := plan(Inputs{Nodes: nodes, Pods: pods, PDBs: tc.pdbs, Placements: tc.placements}, opts)
			if r := onlyResult(t, results); r.Outcome != OutcomeNoPlan {
				t.Fatalf("planned %v, want no plan", movesOf(r.Recommended))
			}
			if tc.reason == "" {
				if len(s.Skipped()) != 0 {
					t.Fatalf("skipped %+v, want nothing reported", s.Skipped())
				}
				return
			}
			var small *Skipped
			for i := range s.Skipped() {
				if s.Skipped()[i].Pod == "default/small" {
					small = &s.Skipped()[i]
				}
			}
			if small == nil || small.Reason != tc.reason {
				t.Fatalf("skipped %+v, want default/small with reason %q", s.Skipped(), tc.reason)
			}
		})
	}
	// Control: with nothing changed, small moves.
	_, results := plan(Inputs{Nodes: nodes, Pods: base()}, DefaultOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomePlanned {
		t.Fatalf("control case: %+v", r)
	}
}

func ptrIntStr(v int) *intstr.IntOrString {
	i := intstr.FromInt32(int32(v))
	return &i
}

// A GPU that an unfinished placement targets is neither freed nor used.
func TestPlanSkipsBusyGPU(t *testing.T) {
	in := Inputs{
		Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb})},
		Pods: []corev1.Pod{
			runningPod("task1", "n1", 0, 24*gb, 0),
			runningPod("task2", "n1", 1, 24*gb, 0),
			runningPod("task3", "n1", 2, 8*gb, 0),
			pendingPodFor("new", 30*gb, 0),
		},
		Placements: []placementv1.HamiPodPlacement{{
			ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "default", CreationTimestamp: metav1.NewTime(testNow)},
			Spec:       placementv1.HamiPodPlacementSpec{NodeName: "n1", GPUDevices: []string{"gpu-0"}},
			Status:     placementv1.HamiPodPlacementStatus{Phase: "Pending"},
		}},
	}
	s, results := plan(in, DefaultOptions())
	r := onlyResult(t, results)
	if got := movesOf(r.Recommended); len(got) != 1 || got[0] != "default/task3>n1/gpu-1" {
		t.Fatalf("recommended %v, want task3 to gpu-1 since gpu-0 is busy", got)
	}
	if s.InFlight() {
		t.Fatal("a manual placement must not count as a defrag plan in flight")
	}
	// A defrag placement holds its GPU until it is marked done.
	in.Placements[0].Labels = map[string]string{PlanLabel: "p", OwnerLabel: "o"}
	if s, _ = plan(in, DefaultOptions()); !s.InFlight() || !s.gpus[0].busy {
		t.Fatal("an unfinished defrag placement must hold its GPU")
	}
	in.Placements[0].Labels[DoneLabel] = "true"
	if s, _ = plan(in, DefaultOptions()); s.InFlight() || s.gpus[0].busy {
		t.Fatal("a finished defrag placement must not hold its GPU")
	}
}

// A moved pod only goes to nodes its node selector and tolerations allow.
func TestPlanRespectsNodeRules(t *testing.T) {
	tainted := testNode("b", gpuSpec{memMB: 36 * gb})
	tainted.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "x", Effect: corev1.TaintEffectNoSchedule}}
	pods := []corev1.Pod{
		runningPod("task1", "a", 0, 30*gb, 0),
		runningPod("task2", "c", 0, 8*gb, 0),
		pendingPodFor("new", 32*gb, 0),
	}
	in := Inputs{Nodes: []corev1.Node{testNode("a", gpuSpec{memMB: 36 * gb}), tainted, testNode("c", gpuSpec{memMB: 36 * gb})}, Pods: pods}
	_, results := plan(in, testOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomeNoPlan {
		t.Fatalf("moved a pod onto a tainted node: %v", movesOf(r.Recommended))
	}
	in.Pods[1].Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}}
	_, results = plan(in, testOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomePlanned || movesOf(r.Recommended)[0] != "default/task2>b/gpu-0" {
		t.Fatalf("with the toleration got %+v", r)
	}
	in.Pods[1].Spec.NodeSelector = map[string]string{"zone": "z1"}
	_, results = plan(in, testOptions())
	if r := onlyResult(t, results); r.Outcome != OutcomeNoPlan {
		t.Fatalf("ignored the node selector: %v", movesOf(r.Recommended))
	}
}

// The second Pending pod is planned against the state after the first plan,
// so the two never count on the same free space.
func TestPlanSeveralPendingPods(t *testing.T) {
	second := pendingPodFor("second", 10*gb, 0)
	second.Status.Conditions[0].LastTransitionTime = metav1.NewTime(testNow.Add(-time.Minute))
	in := Inputs{
		Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb})},
		Pods: []corev1.Pod{
			runningPod("task1", "n1", 0, 24*gb, 0),
			runningPod("task2", "n1", 1, 24*gb, 0),
			runningPod("task3", "n1", 2, 8*gb, 0),
			pendingPodFor("first", 30*gb, 0),
			second,
		},
	}
	_, results := plan(in, DefaultOptions())
	if len(results) != 2 || results[0].Pod != "default/first" || results[0].Outcome != OutcomePlanned {
		t.Fatalf("results %+v", results)
	}
	// After task3 moves to gpu-0, only gpu-1 has 12 GB free.
	if results[1].Outcome != OutcomeFits || results[1].Message != "fits on n1/gpu-1 without moves, so it waits for something other than GPU space" {
		t.Fatalf("second result %+v", results[1])
	}
}

// Only a swap works here: the big pod must leave gpu-0, and it fits on gpu-1
// only after the small pod there moves onto gpu-0, which it fits on first.
// The pinned pod on gpu-1 keeps gpu-1 from being freed instead.
func TestPlanSwap(t *testing.T) {
	in := Inputs{
		Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 20 * gb}, gpuSpec{memMB: 20 * gb})},
		Pods: []corev1.Pod{
			runningPod("big", "n1", 0, 12*gb, 0),
			runningPod("small", "n1", 1, 4*gb, 0),
			runningPod("other", "n1", 1, 6*gb, 0),
			pendingPodFor("new", 16*gb, 0),
		},
	}
	in.Pods[2].Labels[OptInLabel] = "false"
	_, results := plan(in, DefaultOptions())
	r := onlyResult(t, results)
	got := movesOf(r.Recommended)
	if r.Outcome != OutcomePlanned || len(got) != 2 || got[0] != "default/small>n1/gpu-0" || got[1] != "default/big>n1/gpu-1" {
		t.Fatalf("got %v (%s), want small to gpu-0, then big to gpu-1", got, r.Message)
	}
}

func TestPlanMaxPlans(t *testing.T) {
	in := Inputs{Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 10 * gb})}}
	for i := range 4 {
		in.Pods = append(in.Pods, pendingPodFor(fmt.Sprintf("p%d", i), 20*gb, 0))
	}
	opts := DefaultOptions()
	opts.MaxPlans = 2
	_, results := plan(in, opts)
	if len(results) != 2 || results[0].Outcome != OutcomeNoPlan {
		t.Fatalf("results %+v", results)
	}
}

func TestPendingPodRules(t *testing.T) {
	fresh := pendingPodFor("fresh", gb, 0)
	fresh.Status.Conditions[0].LastTransitionTime = metav1.NewTime(testNow.Add(-10 * time.Second))
	multi := pendingPodFor("multi", gb, 0)
	multi.Spec.Containers[0].Resources.Limits[resourceGPU] = *resourceQuantity("2")
	other := pendingPodFor("other", gb, 0)
	other.Spec.SchedulerName = "default-scheduler"
	in := Inputs{Nodes: []corev1.Node{testNode("n1", gpuSpec{memMB: 36 * gb})}, Pods: []corev1.Pod{fresh, multi, other}}
	s, results := plan(in, DefaultOptions())
	if len(results) != 0 {
		t.Fatalf("planned for %+v, want nothing", results)
	}
	if len(s.Skipped()) != 1 || s.Skipped()[0].Pod != "default/multi" {
		t.Fatalf("skipped %+v, want the multi-GPU pod", s.Skipped())
	}
}
