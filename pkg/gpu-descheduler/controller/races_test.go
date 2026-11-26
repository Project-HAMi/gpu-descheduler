package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"
)

// A PodDisruptionBudget refusal fails the placement once; later reconciles
// must not evict again.
func TestRefusedEvictionIsNotRetried(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := placementv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "pdb", Namespace: "default"},
		Spec:       placementv1.HamiPodPlacementSpec{ReqID: "pdb-req", NodeName: "k8s-node01", GPUDevices: []string{"gpu-0"}, EvictPod: "victim"},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "victim", Namespace: "default"}}
	evictions := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pl, pod).WithStatusSubresource(pl).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceCreate: func(ctx context.Context, c client.Client, sub string, obj client.Object, subObj client.Object, opts ...client.SubResourceCreateOption) error {
				if sub == "eviction" {
					evictions++
					return apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)
				}
				return c.SubResource(sub).Create(ctx, obj, subObj, opts...)
			},
		}).Build()
	r := &PlacementReconciler{Client: c, Cache: cache.NewPlacementCache(), ClusterState: clusterstate.NewFakeClusterState()}
	key := types.NamespacedName{Namespace: "default", Name: "pdb"}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	var got placementv1.HamiPodPlacement
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Failed" || evictions != 1 {
		t.Fatalf("phase %q after %d evictions, want Failed after 1", got.Status.Phase, evictions)
	}
}

// The evicted Pod still matches the selector while it terminates. The Pod
// watch must not record it as the replacement.
func TestTerminatingPodIsNotTakenForReplacement(t *testing.T) {
	obs := cache.InitGlobalObservationCache(nil)
	pl := &placementv1.HamiPodPlacement{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Namespace: "default"},
		Spec: placementv1.HamiPodPlacementSpec{
			ReqID:       "move-req",
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "move"}},
		},
	}
	obs.Add(pl, "default", "")
	t.Cleanup(func() { obs.Remove("default", "move") })
	r := &PlacementReconciler{}

	now := metav1.Now()
	old := podWithLabels("move-old", map[string]string{"app": "move"})
	old.DeletionTimestamp = &now
	r.findPlacementsForPod(context.Background(), old)
	if o, _ := obs.Get("default", "move"); o.PodName != "" {
		t.Fatalf("terminating Pod recorded as replacement: %q", o.PodName)
	}

	r.findPlacementsForPod(context.Background(), podWithLabels("move-new", map[string]string{"app": "move"}))
	if o, _ := obs.Get("default", "move"); o.PodName != "move-new" {
		t.Fatalf("replacement not recorded: %q", o.PodName)
	}
}
