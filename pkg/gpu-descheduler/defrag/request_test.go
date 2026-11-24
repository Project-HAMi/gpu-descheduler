/*
Copyright 2026 The HAMi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package defrag

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func resourceQuantity(v string) *resource.Quantity {
	q := resource.MustParse(v)
	return &q
}

func TestParseRequest(t *testing.T) {
	limits := func(kv ...string) corev1.ResourceList {
		l := corev1.ResourceList{}
		for i := 0; i < len(kv); i += 2 {
			l[corev1.ResourceName(kv[i])] = *resourceQuantity(kv[i+1])
		}
		return l
	}
	cases := map[string]struct {
		limits corev1.ResourceList
		opts   func(*Options)
		want   request
		err    error
	}{
		"memory and cores": {limits: limits(resourceGPU, "1", resourceGPUMem, "4000", resourceGPUCores, "30"), want: request{memMB: 4000, cores: 30}},
		"percentage":       {limits: limits(resourceGPU, "1", resourceGPUMemPercent, "50"), want: request{memPercent: 50}},
		"memory wins":      {limits: limits(resourceGPU, "1", resourceGPUMem, "100", resourceGPUMemPercent, "50"), want: request{memMB: 100}},
		"whole GPU":        {limits: limits(resourceGPU, "1"), want: request{memPercent: 100}},
		"default memory": {
			limits: limits(resourceGPU, "1"), opts: func(o *Options) { o.DefaultMemoryMB = 2000; o.DefaultCores = 10 },
			want: request{memMB: 2000, cores: 10},
		},
		"memory factor": {limits: limits(resourceGPU, "1", resourceGPUMem, "4"), opts: func(o *Options) { o.MemoryFactor = 1024 }, want: request{memMB: 4096}},
		"two GPUs":      {limits: limits(resourceGPU, "2"), err: errNotSingleGPU},
		"no GPU":        {limits: limits("cpu", "1"), err: errNoGPU},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Limits: tc.limits}}}}}
			opts := DefaultOptions()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			got, err := parseRequest(pod, opts)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestParseRequestTwoGPUContainers(t *testing.T) {
	ctr := corev1.Container{Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{resourceGPU: *resourceQuantity("1")}}}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{ctr, ctr}}}
	if _, err := parseRequest(pod, DefaultOptions()); !errors.Is(err, errNotSingleGPU) {
		t.Fatalf("got %v, want errNotSingleGPU", err)
	}
}

func TestParseAllocations(t *testing.T) {
	allocs, containers, err := parseAllocations("GPU-a,NVIDIA,4000,30:GPU-b,NVIDIA,2000,0,2:;GPU-c,NVIDIA,100,10:;")
	if err != nil || containers != 2 || len(allocs) != 3 || allocs[1] != (allocation{uuid: "GPU-b", memMB: 2000}) {
		t.Fatalf("got %+v, %d, %v", allocs, containers, err)
	}
	if _, _, err := parseAllocations("GPU-a,NVIDIA,x,1:;"); err == nil {
		t.Fatal("accepted a malformed allocation")
	}
}

func TestDeviceRules(t *testing.T) {
	annos := map[string]string{useGPUTypeAnnotation: "a100, h100", noUseGPUUUIDAnnotation: "GPU-1"}
	if !typeAllowed(annos, "NVIDIA-A100-SXM4") || typeAllowed(annos, "Tesla-T4") {
		t.Fatal("type rule")
	}
	if uuidAllowed(annos, "GPU-1") || !uuidAllowed(annos, "GPU-2") {
		t.Fatal("UUID rule")
	}
	if !modeAllowed(map[string]string{}, "hami-core") || modeAllowed(map[string]string{vgpuModeAnnotation: "mig"}, "hami-core") {
		t.Fatal("mode rule")
	}
}

func TestOptionsValidate(t *testing.T) {
	if err := DefaultOptions().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := DefaultOptions()
	bad.Mode = "now"
	if bad.Validate() == nil {
		t.Fatal("accepted an unknown mode")
	}
	if got := ParseNamespaces(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ParseNamespaces: %v", got)
	}
}
