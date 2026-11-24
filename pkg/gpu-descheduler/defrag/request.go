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
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// request is the GPU request of one pod that asks for a single NVIDIA GPU.
type request struct {
	memMB      int64 // set when the pod asks for a fixed amount
	memPercent int64 // set instead when the pod asks for a share of the GPU
	cores      int64
}

// memOn returns the memory the request takes on g, in MB.
func (r request) memOn(g *GPU) int64 {
	if r.memMB > 0 {
		return r.memMB
	}
	return g.MemTotal * r.memPercent / 100
}

func (r request) demand() Demand {
	return Demand{MemoryMB: r.memMB, MemoryPercent: r.memPercent, Cores: r.cores}
}

var (
	// errNoGPU marks pods that do not ask for an NVIDIA GPU at all.
	errNoGPU = errors.New("does not request an NVIDIA GPU")
	// errNotSingleGPU marks pods that ask for more than one NVIDIA GPU.
	errNotSingleGPU = errors.New("requests more than one NVIDIA GPU, which is never split")
)

// parseRequest reads pod's NVIDIA GPU request the way HAMi's NVIDIA device
// does (GenerateResourceRequests). Only pods with one container asking for
// one GPU are supported, since a pod is never split across moves.
func parseRequest(pod *corev1.Pod, opts Options) (request, error) {
	for i := range pod.Spec.InitContainers {
		if _, ok := quantity(&pod.Spec.InitContainers[i], resourceGPU); ok {
			return request{}, fmt.Errorf("an init container requests a GPU")
		}
	}
	var ctr *corev1.Container
	for i := range pod.Spec.Containers {
		if n, ok := quantity(&pod.Spec.Containers[i], resourceGPU); ok && n > 0 {
			if ctr != nil || n != 1 {
				return request{}, errNotSingleGPU
			}
			ctr = &pod.Spec.Containers[i]
		}
	}
	if ctr == nil {
		return request{}, errNoGPU
	}
	var r request
	if mem, ok := quantity(ctr, resourceGPUMem); ok {
		if mem < 0 {
			return request{}, fmt.Errorf("negative %s", resourceGPUMem)
		}
		r.memMB = mem * opts.MemoryFactor
	}
	if r.memMB == 0 {
		if pct, ok := quantity(ctr, resourceGPUMemPercent); ok && pct > 0 {
			if pct > 100 {
				return request{}, fmt.Errorf("%s above 100", resourceGPUMemPercent)
			}
			r.memPercent = pct
		}
	}
	if r.memMB == 0 && r.memPercent == 0 {
		if opts.DefaultMemoryMB > 0 {
			r.memMB = opts.DefaultMemoryMB
		} else {
			r.memPercent = 100
		}
	}
	r.cores = opts.DefaultCores
	if cores, ok := quantity(ctr, resourceGPUCores); ok {
		if cores < 0 || cores > 100 {
			return request{}, fmt.Errorf("%s must be 0-100", resourceGPUCores)
		}
		r.cores = cores
	}
	return r, nil
}

// quantity returns a container's integer limit for name, or its request when
// no limit is set, as HAMi does.
func quantity(ctr *corev1.Container, name corev1.ResourceName) (int64, bool) {
	q, ok := ctr.Resources.Limits[name]
	if !ok {
		q, ok = ctr.Resources.Requests[name]
	}
	if !ok {
		return 0, false
	}
	return q.Value(), true
}

// allocation is one GPU a pod holds, read from HAMi's allocation annotation.
type allocation struct {
	uuid  string
	memMB int64
	cores int64
}

// parseAllocations reads the hami.io/vgpu-devices-allocated annotation:
// containers separated by ";", devices by ":", and each device as
// "uuid,type,memMB,cores[,slots]".
func parseAllocations(value string) ([]allocation, int, error) {
	var out []allocation
	containers := 0
	for ctr := range strings.SplitSeq(value, ";") {
		devs := 0
		for dev := range strings.SplitSeq(ctr, ":") {
			if dev == "" {
				continue
			}
			fields := strings.Split(dev, ",")
			if len(fields) < 4 || fields[0] == "" {
				return nil, 0, fmt.Errorf("malformed device %q", dev)
			}
			mem, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("malformed memory in %q", dev)
			}
			cores, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("malformed cores in %q", dev)
			}
			out = append(out, allocation{uuid: fields[0], memMB: mem, cores: cores})
			devs++
		}
		if devs > 0 {
			containers++
		}
	}
	return out, containers, nil
}

// registeredGPU is one entry of HAMi's hami.io/node-nvidia-register node
// annotation.
type registeredGPU struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Count   int64  `json:"count"`
	Devmem  int64  `json:"devmem"`
	Devcore int64  `json:"devcore"`
	Type    string `json:"type"`
	Mode    string `json:"mode"`
	Health  bool   `json:"health"`
}

func parseRegisteredGPUs(value string) ([]registeredGPU, error) {
	var gpus []registeredGPU
	if err := json.Unmarshal([]byte(value), &gpus); err != nil {
		return nil, err
	}
	return gpus, nil
}

// typeAllowed mirrors HAMi's CheckType: a case-insensitive substring match
// of the GPU model against the use and nouse lists. An empty list is no
// constraint.
func typeAllowed(annos map[string]string, gpuType string) bool {
	gpuType = strings.ToUpper(gpuType)
	match := func(list string) bool {
		for t := range strings.SplitSeq(list, ",") {
			if t = strings.TrimSpace(t); t != "" && strings.Contains(gpuType, strings.ToUpper(t)) {
				return true
			}
		}
		return false
	}
	if use := strings.TrimSpace(annos[useGPUTypeAnnotation]); use != "" && !match(use) {
		return false
	}
	if noUse := strings.TrimSpace(annos[noUseGPUTypeAnnotation]); noUse != "" && match(noUse) {
		return false
	}
	return true
}

// uuidAllowed mirrors HAMi's CheckUUID: an exact match of the GPU UUID.
func uuidAllowed(annos map[string]string, uuid string) bool {
	match := func(list string) bool {
		for u := range strings.SplitSeq(list, ",") {
			if strings.TrimSpace(u) == uuid {
				return true
			}
		}
		return false
	}
	if use := strings.TrimSpace(annos[useGPUUUIDAnnotation]); use != "" && !match(use) {
		return false
	}
	if noUse := strings.TrimSpace(annos[noUseGPUUUIDAnnotation]); noUse != "" && match(noUse) {
		return false
	}
	return true
}

// modeAllowed mirrors HAMi's nvidia.com/vgpu-mode check.
func modeAllowed(annos map[string]string, mode string) bool {
	want, ok := annos[vgpuModeAnnotation]
	return !ok || strings.Contains(want, mode)
}

func numaBound(annos map[string]string) bool {
	v, err := strconv.ParseBool(annos[numaBindAnnotation])
	return err == nil && v
}
