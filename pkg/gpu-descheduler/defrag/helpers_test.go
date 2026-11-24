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
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

var testNow = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

const gb = 1024

type gpuSpec struct {
	memMB int64
	share int64
}

// testNode returns a ready node whose GPUs are named <node>-gpu-<index>.
func testNode(name string, gpus ...gpuSpec) corev1.Node {
	var reg []registeredGPU
	for i, g := range gpus {
		share := g.share
		if share == 0 {
			share = 10
		}
		reg = append(reg, registeredGPU{
			ID: gpuUUID(name, i), Index: i, Count: share, Devmem: g.memMB, Devcore: 100,
			Type: "NVIDIA-A100", Health: true,
		})
	}
	data, _ := json.Marshal(reg)
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{nodeRegisterAnnotation: string(data)}},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
}

func gpuUUID(node string, index int) string { return fmt.Sprintf("GPU-%s-%d", node, index) }

func gpuPod(name string, memMB, cores int64) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: k8stypes.UID("uid-" + name),
			Labels:      map[string]string{"app": name},
			Annotations: map[string]string{},
		},
		Spec: corev1.PodSpec{
			SchedulerName: hamiSchedulerName,
			Containers: []corev1.Container{{
				Name: "main",
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					resourceGPU:      resource.MustParse("1"),
					resourceGPUMem:   resource.MustParse(strconv.FormatInt(memMB, 10)),
					resourceGPUCores: resource.MustParse(strconv.FormatInt(cores, 10)),
				}},
			}},
		},
	}
}

// runningPod returns an opted-in pod owned by a ReplicaSet that holds memMB
// and cores on the GPU with the given index of node.
func runningPod(name, node string, index int, memMB, cores int64) corev1.Pod {
	pod := gpuPod(name, memMB, cores)
	pod.Labels[OptInLabel] = "true"
	pod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-rs",
		UID: k8stypes.UID("owner-" + name), Controller: ptr.To(true),
	}}
	pod.Spec.NodeName = node
	// HAMi labels a pod once it is scheduled; its replacement starts without it.
	pod.Labels["hami.io/vgpu-node"] = node
	pod.Annotations[podAllocatedAnnotation] = fmt.Sprintf("%s,NVIDIA,%d,%d:;", gpuUUID(node, index), memMB, cores)
	pod.Status = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
	}
	return pod
}

// pendingPodFor returns a pod kube-scheduler reported as unschedulable two
// minutes ago.
func pendingPodFor(name string, memMB, cores int64) corev1.Pod {
	pod := gpuPod(name, memMB, cores)
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			LastTransitionTime: metav1.NewTime(testNow.Add(-2 * time.Minute)),
		}},
	}
	return pod
}

func testOptions() Options {
	opts := DefaultOptions()
	opts.Scope = ScopeCrossNode
	return opts
}

func plan(in Inputs, opts Options) (*Snapshot, []Result) {
	if in.Now.IsZero() {
		in.Now = testNow
	}
	s := BuildSnapshot(in, opts)
	return s, PlanAll(s, opts)
}
