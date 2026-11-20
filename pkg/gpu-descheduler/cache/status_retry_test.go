package cache

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

func TestObserverRetriesFailedStatusWrite(t *testing.T) {
	for _, name := range []string{"missing", "pending"} {
		t.Run(name, func(t *testing.T) {
			var objects []runtime.Object
			if name == "pending" {
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Status: corev1.PodStatus{Phase: corev1.PodPending}})
			}
			obs := NewObservationCache(fake.NewSimpleClientset(objects...))
			defer obs.Stop()
			pl := &placementv1.HamiPodPlacement{ObjectMeta: metav1.ObjectMeta{Name: "placement", Namespace: "default"}}
			obs.Add(pl, "default", name)
			var attempts []string
			update := func(namespace, placement, phase, message string, _ ...MatchedPodInfo) error {
				if namespace != "default" || placement != "placement" || phase != "Failed" {
					t.Fatalf("unexpected update: %s/%s %s", namespace, placement, phase)
				}
				attempts = append(attempts, message)
				if len(attempts) == 1 {
					return errors.New("temporary status update failure")
				}
				return nil
			}
			obs.checkPodStatus(1, update)
			obs.checkPodStatus(1, update)
			if len(attempts) != 2 || attempts[0] != attempts[1] || obs.Count() != 0 {
				t.Fatalf("messages %v, remaining observations %d", attempts, obs.Count())
			}
		})
	}
}
