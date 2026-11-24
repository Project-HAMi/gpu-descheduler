package webhook

import (
	"bytes"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"
)

func TestDryRunLeavesPlacementForRealCreate(t *testing.T) {
	active := cache.InitGlobalCache()
	observations := cache.InitGlobalObservationCache(nil)
	pl := &placementv1.HamiPodPlacement{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "dry-run-test"}}
	pl.Spec.ReqID = pl.Name
	pl.Spec.NodeName = "k8s-gpu-3080x2"
	pl.Spec.GPUDevices = []string{"gpu-0"}
	active.AddOrUpdate(pl)
	t.Cleanup(func() {
		active.Delete(pl.Namespace, pl.Name)
		observations.Remove(pl.Namespace, pl.Name)
		cache.UnmarkReqIDConsumed(pl.Spec.ReqID)
		cache.ConsumeAdmissionMapping("preview")
		cache.ConsumeAdmissionMapping("real")
	})
	dryRun := true
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID: "preview", Operation: admissionv1.Create, DryRun: &dryRun,
		Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"worker","namespace":"default"},"spec":{"schedulerName":"hami-scheduler"}}`)},
	}}
	m := PodMutator{Decoder: admission.NewDecoder(scheme.Scheme), ClusterState: clusterstate.NewFakeClusterState()}
	preview := m.Handle(t.Context(), req)
	if !preview.Allowed || !bytes.Contains(preview.Patch, []byte("GPU-7e6e20c7")) {
		t.Fatalf("missing preview GPU patch: %+v", preview)
	}
	if _, ok := active.Get(pl.Namespace, pl.Name); !ok || cache.IsReqIDConsumed(pl.Spec.ReqID) {
		t.Fatal("preview consumed placement")
	}
	if _, ok := observations.Get(pl.Namespace, pl.Name); ok {
		t.Fatal("preview started pod observation")
	}
	if _, ok := cache.FindPlacementByAdmissionUID("preview"); ok {
		t.Fatal("preview recorded admission UID")
	}

	req.UID = "real"
	req.DryRun = nil
	result := m.Handle(t.Context(), req)
	if !result.Allowed || !bytes.Contains(result.Patch, []byte("GPU-7e6e20c7")) || !cache.IsReqIDConsumed(pl.Spec.ReqID) {
		t.Fatalf("real CREATE did not consume and patch placement: %+v", result)
	}
}

func TestRejectedPodDoesNotConsumePlacement(t *testing.T) {
	active := cache.InitGlobalCache()
	observations := cache.InitGlobalObservationCache(nil)
	pl := &placementv1.HamiPodPlacement{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "missing-gpu"}}
	pl.Spec.ReqID = pl.Name
	pl.Spec.NodeName = "k8s-gpu-3080x2"
	pl.Spec.GPUDevices = []string{"gpu-7"}
	active.AddOrUpdate(pl)
	t.Cleanup(func() {
		active.Delete(pl.Namespace, pl.Name)
		observations.Remove(pl.Namespace, pl.Name)
		cache.UnmarkReqIDConsumed(pl.Spec.ReqID)
		cache.ConsumeAdmissionMapping("rejected")
	})
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID: "rejected", Operation: admissionv1.Create,
		Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"worker","namespace":"default"},"spec":{"schedulerName":"hami-scheduler"}}`)},
	}}
	m := PodMutator{Decoder: admission.NewDecoder(scheme.Scheme), ClusterState: clusterstate.NewFakeClusterState()}
	if resp := m.Handle(t.Context(), req); resp.Allowed {
		t.Fatalf("Pod with an unknown target GPU was allowed: %+v", resp)
	}
	if _, ok := active.Get(pl.Namespace, pl.Name); !ok || cache.IsReqIDConsumed(pl.Spec.ReqID) {
		t.Fatal("rejected Pod consumed the placement")
	}
	if _, ok := cache.FindPlacementByAdmissionUID("rejected"); ok {
		t.Fatal("rejected Pod recorded an admission UID")
	}
}
