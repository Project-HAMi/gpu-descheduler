package cache

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

func TestMatchedMessageDescribesPodLocation(t *testing.T) {
	for _, tc := range []struct {
		node, want string
	}{
		{node: "", want: "detected before scheduling with phase Pending"},
		{node: "gpu-node", want: "detected on node gpu-node with phase Pending"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			uid := "admission-" + tc.node
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "replacement",
					Namespace:   "default",
					Annotations: map[string]string{GetAdmissionAnnotationKey(): uid},
				},
				Spec:   corev1.PodSpec{NodeName: tc.node},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			pl := &placementv1.HamiPodPlacement{ObjectMeta: metav1.ObjectMeta{Name: "placement", Namespace: "default"}}
			RecordPendingAdmission(uid, pl, "default", "replacement")

			obs := NewObservationCache(fake.NewSimpleClientset(pod))
			defer obs.Stop()
			obs.Add(pl, "default", "replacement")

			var message string
			obs.checkPodStatus(3, func(_, _, phase, msg string, _ ...MatchedPodInfo) error {
				if phase != "Matched" {
					t.Fatalf("phase %q, want Matched", phase)
				}
				message = msg
				return nil
			})
			if !strings.Contains(message, tc.want) {
				t.Fatalf("message %q does not contain %q", message, tc.want)
			}
		})
	}
}
