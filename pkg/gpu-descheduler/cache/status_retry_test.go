package cache

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

func TestObserverRetriesFailedStatusWrite(t *testing.T) {
	for name, updateErr := range map[string]error{
		"missing": apierrors.NewServiceUnavailable("temporary outage"),
		"pending": &url.Error{Op: "Put", URL: "https://apiserver", Err: &net.DNSError{Name: "apiserver", IsTemporary: true}},
	} {
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
					return fmt.Errorf("update placement: %w", updateErr)
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

func TestObserverDropsPermanentStatusErrors(t *testing.T) {
	resource := schema.GroupResource{Group: "descheduler.hami.io", Resource: "hamipodplacements"}
	for name, updateErr := range map[string]error{
		"forbidden":     apierrors.NewForbidden(resource, "placement", errors.New("connection reset by peer")),
		"invalid":       apierrors.NewInvalid(schema.GroupKind{Group: resource.Group, Kind: "HamiPodPlacement"}, "placement", nil),
		"not found":     apierrors.NewNotFound(resource, "placement"),
		"permanent DNS": &url.Error{Op: "Put", URL: "https://apiserver", Err: &net.DNSError{Name: "apiserver", IsNotFound: true}},
		"unknown":       errors.New("unexpected status error"),
	} {
		t.Run(name, func(t *testing.T) {
			obs := NewObservationCache(fake.NewSimpleClientset())
			defer obs.Stop()
			pl := &placementv1.HamiPodPlacement{ObjectMeta: metav1.ObjectMeta{Name: "placement", Namespace: "default"}}
			obs.Add(pl, "default", "missing")
			attempts := 0
			update := func(_, _, _, _ string, _ ...MatchedPodInfo) error {
				attempts++
				return fmt.Errorf("update placement: %w", updateErr)
			}
			obs.checkPodStatus(1, update)
			obs.checkPodStatus(1, update)
			if attempts != 1 || obs.Count() != 0 {
				t.Fatalf("attempts %d, remaining observations %d", attempts, obs.Count())
			}
		})
	}
}
