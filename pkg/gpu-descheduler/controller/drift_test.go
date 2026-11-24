package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"
)

func TestEditingConsumedPlacementFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := placementv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "edited", Namespace: "default", Generation: 1},
		Spec: placementv1.HamiPodPlacementSpec{
			ReqID:       "edited-req",
			NodeName:    "k8s-node01",
			GPUDevices:  []string{"gpu-0"},
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "edited"}},
		},
	}
	pc := cache.InitGlobalCache()
	cache.InitGlobalObservationCache(nil)
	pc.AddOrUpdate(pl)
	t.Cleanup(func() {
		pc.Delete(pl.Namespace, pl.Name)
		cache.GetGlobalObservationCache().Remove(pl.Namespace, pl.Name)
		cache.UnmarkReqIDConsumed(pl.Spec.ReqID)
	})
	pod := podWithLabels("edited-pod", map[string]string{"app": "edited"})
	if _, ok := cache.FindPlacementForPod(pod); !ok {
		t.Fatal("placement was not consumed")
	}

	pl.Generation = 2
	pl.Spec.GPUDevices = []string{"gpu-0/50"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pl).WithStatusSubresource(pl).Build()
	r := &PlacementReconciler{Client: c, Cache: pc, ClusterState: clusterstate.NewFakeClusterState()}
	key := types.NamespacedName{Namespace: "default", Name: "edited"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got placementv1.HamiPodPlacement
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Failed" || !strings.Contains(got.Status.Message, "modification rejected") {
		t.Fatalf("status %+v", got.Status)
	}
	msg := got.Status.Message
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, &got); err != nil || got.Status.Message != msg {
		t.Fatalf("second reconcile rewrote the status: %q", got.Status.Message)
	}
}

func TestConsumedPhaseUpdateIsKept(t *testing.T) {
	cache.MarkReqIDConsumed("phase-req", "default", "p", "default", "pod", "Observing")
	t.Cleanup(func() { cache.UnmarkReqIDConsumed("phase-req") })
	cache.MarkReqIDConsumed("phase-req", "default", "p", "default", "pod", "Matched")
	if info, _ := cache.GetReqIDConsumptionInfo("phase-req"); info.Phase != "Matched" {
		t.Fatalf("phase %q, want Matched", info.Phase)
	}
}

func podWithLabels(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels}}
}
