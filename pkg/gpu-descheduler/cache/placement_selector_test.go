package cache

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEmptyMatchLabelRequiresKey(t *testing.T) {
	selector := metav1.LabelSelector{MatchLabels: map[string]string{"role": ""}}
	if labelsMatch(selector, map[string]string{"app": "worker"}) {
		t.Error("matched a pod missing the role label")
	}
	if !labelsMatch(selector, map[string]string{"role": ""}) {
		t.Error("rejected a pod with an explicitly empty role label")
	}
}
