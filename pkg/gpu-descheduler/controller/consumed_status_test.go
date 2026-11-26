package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"
)

func reconcileTwice(t *testing.T, pl *placementv1.HamiPodPlacement) (client.Client, *cache.PlacementCache) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := placementv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pl).WithStatusSubresource(pl).Build()
	pc := cache.NewPlacementCache()
	r := &PlacementReconciler{Client: c, Cache: pc, ClusterState: clusterstate.NewFakeClusterState()}
	key := types.NamespacedName{Namespace: pl.Namespace, Name: pl.Name}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	return c, pc
}

// After a restart or the record TTL, only the status shows that a Pod used
// the placement. It must not be offered to the next matching Pod.
func TestConsumedPlacementIsNotOfferedAgainWithoutRecord(t *testing.T) {
	consumedAt := metav1.Now()
	for _, status := range []placementv1.HamiPodPlacementStatus{
		{Phase: "Matched", MatchedPodName: "rearm-1"},
		{Phase: "Pending", ConsumedAt: &consumedAt},
	} {
		t.Run(status.Phase, func(t *testing.T) {
			pl := &placementv1.HamiPodPlacement{
				ObjectMeta: metav1.ObjectMeta{Name: "rearm", Namespace: "default", Generation: 1},
				Spec: placementv1.HamiPodPlacementSpec{
					ReqID: "rearm-req", NodeName: "k8s-node01", GPUDevices: []string{"gpu-0"},
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "rearm"}},
				},
				Status: status,
			}
			t.Cleanup(func() { cache.UnmarkReqIDConsumed("rearm-req") })
			c, pc := reconcileTwice(t, pl)

			var got placementv1.HamiPodPlacement
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(pl), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != status.Phase {
				t.Fatalf("phase %q, want %q", got.Status.Phase, status.Phase)
			}
			if n := len(pc.FindMatchingPlacements("default", map[string]string{"app": "rearm"})); n != 0 {
				t.Fatalf("placement is matchable again (%d matches)", n)
			}
			if !cache.IsReqIDConsumed("rearm-req") {
				t.Fatal("consumed record was not rebuilt from the status")
			}
		})
	}
}

// A consumption the webhook recorded in memory is written to the status, so
// it survives a restart.
func TestConsumptionIsWrittenToStatus(t *testing.T) {
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "observed", Namespace: "default", Generation: 1},
		Spec: placementv1.HamiPodPlacementSpec{
			ReqID: "observed-req", NodeName: "k8s-node01", GPUDevices: []string{"gpu-0"},
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "observed"}},
		},
		Status: placementv1.HamiPodPlacementStatus{Phase: "Pending"},
	}
	cache.MarkReqIDConsumed("observed-req", "default", "observed", "default", "", "Observing")
	t.Cleanup(func() { cache.UnmarkReqIDConsumed("observed-req") })
	c, _ := reconcileTwice(t, pl)

	var got placementv1.HamiPodPlacement
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pl), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ConsumedAt == nil {
		t.Fatal("consumedAt not written")
	}
}
