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

func TestPlacementWithMissingTargetDoesNotEvict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := placementv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "victim", Namespace: "default"}}
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Namespace: "default"},
		Spec: placementv1.HamiPodPlacementSpec{
			ReqID:      "missing-target",
			NodeName:   "k8s-node01",
			GPUDevices: []string{"gpu-7"},
			EvictPod:   "victim",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, pl).WithStatusSubresource(pl).Build()
	r := &PlacementReconciler{Client: c, Cache: cache.NewPlacementCache(), ClusterState: clusterstate.NewFakeClusterState()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "move"}}); err != nil {
		t.Fatal(err)
	}

	var got placementv1.HamiPodPlacement
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "move"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Failed" || !strings.Contains(got.Status.Message, "Not evicting pod victim: gpu-7 not found") {
		t.Fatalf("status %+v", got.Status)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "victim"}, &corev1.Pod{}); err != nil {
		t.Fatalf("victim was evicted: %v", err)
	}
}
