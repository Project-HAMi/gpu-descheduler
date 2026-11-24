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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

// cluster is the issue example: task3 must move from gpu-2 to gpu-0 so that
// new fits on gpu-2.
func cluster() []client.Object {
	node := testNode("n1", gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb}, gpuSpec{memMB: 36 * gb})
	objs := []client.Object{&node}
	for _, p := range []corev1.Pod{
		runningPod("task1", "n1", 0, 24*gb, 0),
		runningPod("task2", "n1", 1, 24*gb, 0),
		runningPod("task3", "n1", 2, 8*gb, 0),
		pendingPodFor("new", 30*gb, 0),
	} {
		objs = append(objs, &p)
		if owner := metav1.GetControllerOf(&p); owner != nil {
			objs = append(objs, &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{Name: owner.Name, Namespace: p.Namespace, UID: owner.UID},
				Spec:       appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": p.Labels["app"]}}},
			})
		}
	}
	return objs
}

// behaviour decides what the simulated placement controller and webhook do
// when a placement is created.
type behaviour struct {
	fail        bool   // mark the placement Failed
	unpinned    bool   // the webhook does not pin the replacement
	wrongGPU    bool   // the replacement gets another GPU
	bindPending bool   // kube-scheduler then binds the Pending pod
	takeTarget  string // a third pod takes the freed GPU instead
	deleteOwner bool   // the moved pod's ReplicaSet is gone
	ignore      bool   // nothing acts on the placement
}

// newCluster returns a fake client that acts like the placement controller,
// the webhook and HAMi when a placement is created: it evicts the pod,
// creates its replacement on the target GPU, and marks the placement Matched.
func newCluster(t *testing.T, b behaviour) (client.Client, *[]*placementv1.HamiPodPlacement) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := placementv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var created []*placementv1.HamiPodPlacement
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster()...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := w.Create(ctx, obj, opts...); err != nil {
				return err
			}
			pl, ok := obj.(*placementv1.HamiPodPlacement)
			if !ok || pl.Spec.EvictPod == "" {
				return nil
			}
			created = append(created, pl.DeepCopy())
			if b.ignore {
				return nil
			}
			if b.fail {
				pl.Status.Phase, pl.Status.Message = "Failed", "Target node does not exist"
				return w.Update(ctx, pl)
			}
			var old corev1.Pod
			if err := w.Get(ctx, k8stypes.NamespacedName{Namespace: pl.Namespace, Name: pl.Spec.EvictPod}, &old); err != nil {
				return err
			}
			if err := w.Delete(ctx, &old); err != nil {
				return err
			}
			index, err := deviceIndex(pl.Spec.GPUDevices[0])
			if err != nil {
				return err
			}
			target := index
			if b.wrongGPU {
				index = 1
			}
			repl := old.DeepCopy()
			repl.Name, repl.UID, repl.ResourceVersion = old.Name+"-new", k8stypes.UID(string(old.UID)+"-new"), ""
			repl.Spec.NodeName = pl.Spec.NodeName
			repl.Annotations[podAllocatedAnnotation] = fmt.Sprintf("%s,NVIDIA,%d,0:;", gpuUUID(pl.Spec.NodeName, index), 8*gb)
			if !b.unpinned {
				repl.Annotations[placementAnnotation] = gpuUUID(pl.Spec.NodeName, target) + "," + pl.Spec.NodeName
			}
			if err := w.Create(ctx, repl); err != nil {
				return err
			}
			pl.Status.Phase, pl.Status.MatchedPodName = "Matched", repl.Name
			if err := w.Update(ctx, pl); err != nil {
				return err
			}
			if b.takeTarget != "" {
				thief := runningPod("thief", "n1", 2, 30*gb, 0)
				if err := w.Create(ctx, &thief); err != nil {
					return err
				}
			}
			if b.bindPending {
				var pending corev1.Pod
				if err := w.Get(ctx, k8stypes.NamespacedName{Namespace: "default", Name: "new"}, &pending); err != nil {
					return err
				}
				pending.Spec.NodeName = "n1"
				return w.Update(ctx, &pending)
			}
			return nil
		},
	}).Build()
	if b.deleteOwner {
		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "task3-rs", Namespace: "default"}}
		if err := c.Delete(context.Background(), rs); err != nil {
			t.Fatal(err)
		}
	}
	return c, &created
}

func testRunner(c client.Client, mode Mode) *Runner {
	opts := DefaultOptions()
	opts.Mode = mode
	opts.MoveTimeout = 300 * time.Millisecond
	r := NewRunner(c, c, opts)
	r.now = func() time.Time { return testNow }
	r.exec.Now = r.now
	r.exec.Poll = 5 * time.Millisecond
	return r
}

func TestDryRunCreatesNothing(t *testing.T) {
	c, created := newCluster(t, behaviour{})
	r := testRunner(c, ModeDryRun)
	r.RunOnce(context.Background())
	report := r.Report()
	if report == nil || len(report.Results) != 1 || report.Results[0].Outcome != OutcomePlanned {
		t.Fatalf("report %+v", report)
	}
	if len(*created) != 0 || report.Execution != nil {
		t.Fatalf("dry run created %d placements and execution %+v", len(*created), report.Execution)
	}
}

func TestExecuteSucceeds(t *testing.T) {
	c, created := newCluster(t, behaviour{bindPending: true})
	r := testRunner(c, ModeExecute)
	r.RunOnce(context.Background())
	exec := r.Report().Execution
	if exec == nil || exec.Result != ExecSucceeded || exec.MovesDone != 1 {
		t.Fatalf("execution %+v", exec)
	}
	if len(*created) != 1 {
		t.Fatalf("created %d placements, want 1", len(*created))
	}
	pl := (*created)[0]
	if pl.Spec.EvictPod != "task3" || pl.Spec.NodeName != "n1" || len(pl.Spec.GPUDevices) != 1 || pl.Spec.GPUDevices[0] != "gpu-0" ||
		len(pl.Spec.PodSelector.MatchLabels) != 1 || pl.Spec.PodSelector.MatchLabels["app"] != "task3" || pl.Labels[OwnerLabel] != "owner-task3" || pl.Labels[PlanLabel] == "" ||
		pl.Spec.ReqID != pl.Name {
		t.Fatalf("placement %+v", pl)
	}
	var done placementv1.HamiPodPlacement
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pl), &done); err != nil || done.Labels[DoneLabel] != "true" {
		t.Fatalf("placement not marked done: %v %v", done.Labels, err)
	}
	// The next run sees the move done and the pod bound: nothing to do, and
	// the workload is in cooldown.
	r.RunOnce(context.Background())
	if got := r.Report(); len(got.Results) != 0 || len(*created) != 1 {
		t.Fatalf("second run: %+v, %d placements", got, len(*created))
	}
}

func TestExecuteFailures(t *testing.T) {
	cases := map[string]struct {
		b      behaviour
		result string
		msg    string
	}{
		"placement failed":  {b: behaviour{fail: true}, result: ExecAborted, msg: "Target node does not exist"},
		"not pinned":        {b: behaviour{unpinned: true}, result: ExecAborted, msg: "waiting for the replacement pod"},
		"wrong GPU":         {b: behaviour{wrongGPU: true}, result: ExecAborted, msg: "got another GPU"},
		"pending pod waits": {b: behaviour{}, result: ExecTimeout, msg: "timed out"},
		"owner gone":        {b: behaviour{deleteOwner: true}, result: ExecAborted, msg: "reading ReplicaSet"},
		"freed space taken": {b: behaviour{takeTarget: "thief"}, result: ExecRaced, msg: "freed space was taken"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newCluster(t, tc.b)
			r := testRunner(c, ModeExecute)
			r.RunOnce(context.Background())
			exec := r.Report().Execution
			if exec == nil || exec.Result != tc.result || !strings.Contains(exec.Message, tc.msg) {
				t.Fatalf("execution %+v, want %s containing %q", exec, tc.result, tc.msg)
			}
		})
	}
}

// A placement no pod took is deleted when the move gives up, so that it
// cannot pin a pod created later.
func TestExecuteDropsUnusedPlacement(t *testing.T) {
	c, created := newCluster(t, behaviour{ignore: true})
	r := testRunner(c, ModeExecute)
	r.RunOnce(context.Background())
	if exec := r.Report().Execution; exec == nil || exec.Result != ExecAborted || !strings.Contains(exec.Message, "timed out") {
		t.Fatalf("execution %+v", exec)
	}
	var left placementv1.HamiPodPlacementList
	if err := c.List(context.Background(), &left); err != nil {
		t.Fatal(err)
	}
	if len(*created) != 1 || len(left.Items) != 0 {
		t.Fatalf("created %d, left %d placements; want the unused one deleted", len(*created), len(left.Items))
	}
}

// A plan that no longer holds when it is about to run evicts nothing.
func TestExecuteStalePlan(t *testing.T) {
	c, created := newCluster(t, behaviour{})
	r := testRunner(c, ModeExecute)
	snap, err := r.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res := PlanAll(snap, r.opts)[0]
	var task1 corev1.Pod
	if err := c.Get(context.Background(), k8stypes.NamespacedName{Namespace: "default", Name: "task1"}, &task1); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), &task1); err != nil {
		t.Fatal(err)
	}
	thief := runningPod("thief", "n1", 0, 10*gb, 0)
	if err := c.Create(context.Background(), &thief); err != nil {
		t.Fatal(err)
	}
	// gpu-0 now has 26 GB free, so the planned move still fits; take more.
	big := runningPod("big", "n1", 0, 24*gb, 0)
	if err := c.Create(context.Background(), &big); err != nil {
		t.Fatal(err)
	}
	exec := r.exec.Execute(context.Background(), res, nil)
	if exec.Result != ExecStale || len(*created) != 0 {
		t.Fatalf("execution %+v with %d placements, want stale and none", exec, len(*created))
	}
}

func TestExecuteWaitsForInFlightPlacement(t *testing.T) {
	c, created := newCluster(t, behaviour{})
	inFlight := &placementv1.HamiPodPlacement{}
	inFlight.Name, inFlight.Namespace = "defrag-x-1-1", "default"
	inFlight.Labels = map[string]string{PlanLabel: "x", OwnerLabel: "o"}
	inFlight.CreationTimestamp.Time = testNow
	inFlight.Spec = placementv1.HamiPodPlacementSpec{ReqID: "defrag-x-1-1", NodeName: "n1", GPUDevices: []string{"gpu-1"}}
	if err := c.Create(context.Background(), inFlight); err != nil {
		t.Fatal(err)
	}
	*created = nil
	r := testRunner(c, ModeExecute)
	r.RunOnce(context.Background())
	if r.Report().Execution != nil || len(*created) != 0 {
		t.Fatal("started a plan while another defrag placement is in flight")
	}
}

func TestTrigger(t *testing.T) {
	c, _ := newCluster(t, behaviour{})
	r := testRunner(c, ModeDryRun)
	if r.Trigger() != TriggerNotLeader {
		t.Fatal("a runner that has not started must not accept a trigger")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for r.Report() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.Trigger() != TriggerAccepted {
		t.Fatal("trigger not accepted")
	}
	cancel()
	<-done
}

// A StatefulSet recreates a pod under the same name, so the selector is
// narrowed to that pod; labels added after creation are left out.
func TestReplacementSelectorStatefulSet(t *testing.T) {
	pod := runningPod("db-0", "n1", 0, gb, 0)
	pod.OwnerReferences[0].Kind, pod.OwnerReferences[0].Name = "StatefulSet", "db"
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default", UID: pod.OwnerReferences[0].UID},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	e := &Executor{Owners: fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build()}
	sel, err := e.replacementSelector(context.Background(), &pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.MatchLabels) != 2 || sel.MatchLabels["app"] != "db" || sel.MatchLabels[appsv1.StatefulSetPodNameLabel] != "db-0" {
		t.Fatalf("selector %+v", sel)
	}
	if sts.Spec.Selector.MatchLabels[appsv1.StatefulSetPodNameLabel] != "" {
		t.Fatal("changed the StatefulSet's own selector")
	}
	pod.OwnerReferences[0].Kind = "CronJob"
	if _, err := e.replacementSelector(context.Background(), &pod); err == nil {
		t.Fatal("accepted an unsupported controller")
	}
}
