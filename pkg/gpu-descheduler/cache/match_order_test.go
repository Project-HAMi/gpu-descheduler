package cache

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

// When several placements select a Pod, the oldest one is used every time.
func TestOldestMatchingPlacementComesFirst(t *testing.T) {
	pc := NewPlacementCache()
	base := time.Now()
	ages := map[string]time.Duration{"a-oldest": 0, "b-middle": time.Minute, "c-newest": 2 * time.Minute}
	for _, name := range []string{"c-newest", "a-oldest", "b-middle"} {
		pc.AddOrUpdate(&placementv1.HamiPodPlacement{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(base.Add(ages[name]))},
			Spec: placementv1.HamiPodPlacementSpec{
				ReqID:       name + "-req",
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			},
		})
	}
	for i := 0; i < 50; i++ {
		got := pc.FindMatchingPlacements("default", map[string]string{"app": "x"})
		if len(got) != 3 || got[0].Name != "a-oldest" || got[1].Name != "b-middle" || got[2].Name != "c-newest" {
			t.Fatalf("order %v", []string{got[0].Name, got[1].Name, got[2].Name})
		}
	}
}
