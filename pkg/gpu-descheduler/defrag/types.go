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

// Package defrag finds HAMi pods that stay Pending because GPU memory or
// cores are split across many GPUs, plans the fewest moves of opted-in pods
// that free a large enough block, and carries the plan out through
// HamiPodPlacement objects.
package defrag

import (
	"fmt"
	"strings"
	"time"
)

const (
	// OptInLabel marks a pod, usually through its workload's pod template,
	// as one that may be restarted on another GPU to defragment the cluster.
	OptInLabel = "hami.io/defrag-opt-in"
	// PlanLabel names the plan a HamiPodPlacement was created for.
	PlanLabel = "hami.io/defrag-plan"
	// DoneLabel marks a defrag placement whose move finished. The placement
	// is kept, so that the owner's cooldown survives a restart, but it no
	// longer holds its GPU.
	DoneLabel = "hami.io/defrag-done"
	// OwnerLabel holds the UID of the controller of the moved pod, so that a
	// cooldown survives a restart of gpu-descheduler.
	OwnerLabel = "hami.io/defrag-owner"

	// Annotations and resources written or read by HAMi for NVIDIA GPUs.
	nodeRegisterAnnotation = "hami.io/node-nvidia-register"
	podAllocatedAnnotation = "hami.io/vgpu-devices-allocated"
	useGPUTypeAnnotation   = "nvidia.com/use-gputype"
	noUseGPUTypeAnnotation = "nvidia.com/nouse-gputype"
	useGPUUUIDAnnotation   = "nvidia.com/use-gpuuuid"
	noUseGPUUUIDAnnotation = "nvidia.com/nouse-gpuuuid"
	vgpuModeAnnotation     = "nvidia.com/vgpu-mode"
	numaBindAnnotation     = "nvidia.com/numa-bind"
	resourceGPU            = "nvidia.com/gpu"
	resourceGPUMem         = "nvidia.com/gpumem"
	resourceGPUMemPercent  = "nvidia.com/gpumem-percentage"
	resourceGPUCores       = "nvidia.com/gpucores"

	hamiSchedulerName = "hami-scheduler"
	migMode           = "mig"
)

// Mode says whether plans are only reported or also carried out.
type Mode string

const (
	ModeDryRun  Mode = "dryRun"
	ModeExecute Mode = "execute"
)

// Scope limits where a moved pod may go.
type Scope string

const (
	ScopeSameNode  Scope = "sameNode"
	ScopeCrossNode Scope = "crossNode"
)

// Options configure planning and execution.
type Options struct {
	Mode  Mode
	Scope Scope
	// Interval between two runs.
	Interval time.Duration
	// MaxMoves is the largest number of moves one plan may have.
	MaxMoves int
	// MaxPlans is the largest number of Pending pods planned for per run.
	MaxPlans int
	// Alternatives is how many plans are reported besides the recommended one.
	Alternatives int
	// MinPendingAge is how long a pod must be unschedulable before it is
	// planned for, so pods that kube-scheduler is about to place are left alone.
	MinPendingAge time.Duration
	// Cooldown is how long a workload is not moved again after a move.
	Cooldown time.Duration
	// MoveTimeout bounds each move, and the wait for the Pending pod after
	// the last move.
	MoveTimeout time.Duration
	// Namespaces, when not empty, are the only namespaces whose pods may be
	// moved.
	Namespaces []string
	// DefaultMemoryMB, DefaultCores and MemoryFactor mirror the HAMi
	// scheduler's nvidia.defaultMemory, nvidia.defaultCores and
	// nvidia.memoryFactor, so requests are read the way HAMi reads them.
	DefaultMemoryMB int64
	DefaultCores    int64
	MemoryFactor    int64
}

// DefaultOptions returns the defaults used by the command-line flags.
func DefaultOptions() Options {
	return Options{
		Mode:          ModeDryRun,
		Scope:         ScopeSameNode,
		Interval:      5 * time.Minute,
		MaxMoves:      2,
		MaxPlans:      5,
		Alternatives:  3,
		MinPendingAge: time.Minute,
		Cooldown:      30 * time.Minute,
		MoveTimeout:   5 * time.Minute,
		MemoryFactor:  1,
	}
}

// Validate reports the first invalid option.
func (o Options) Validate() error {
	switch {
	case o.Mode != ModeDryRun && o.Mode != ModeExecute:
		return fmt.Errorf("defrag mode must be %q or %q, got %q", ModeDryRun, ModeExecute, o.Mode)
	case o.Scope != ScopeSameNode && o.Scope != ScopeCrossNode:
		return fmt.Errorf("defrag scope must be %q or %q, got %q", ScopeSameNode, ScopeCrossNode, o.Scope)
	case o.Interval <= 0:
		return fmt.Errorf("defrag interval must be positive")
	case o.MaxMoves < 1:
		return fmt.Errorf("defrag max moves must be at least 1")
	case o.MaxPlans < 1:
		return fmt.Errorf("defrag max plans must be at least 1")
	case o.Alternatives < 0:
		return fmt.Errorf("defrag alternatives must not be negative")
	case o.MinPendingAge < 0 || o.Cooldown < 0:
		return fmt.Errorf("defrag min pending age and cooldown must not be negative")
	case o.MoveTimeout <= 0:
		return fmt.Errorf("defrag move timeout must be positive")
	case o.DefaultMemoryMB < 0 || o.DefaultCores < 0 || o.DefaultCores > 100 || o.MemoryFactor < 1:
		return fmt.Errorf("defrag default memory must be >= 0, default cores 0-100, memory factor >= 1")
	}
	return nil
}

// ParseNamespaces splits a comma-separated namespace list.
func ParseNamespaces(list string) []string {
	var out []string
	for ns := range strings.SplitSeq(list, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out
}

// GPURef identifies one GPU.
type GPURef struct {
	Node  string `json:"node"`
	UUID  string `json:"uuid"`
	Index int    `json:"index"`
}

func (r GPURef) String() string { return fmt.Sprintf("%s/gpu-%d", r.Node, r.Index) }

// Move moves one pod to another GPU.
type Move struct {
	Pod       string `json:"pod"`
	PodUID    string `json:"podUID"`
	OwnerUID  string `json:"ownerUID"`
	From      GPURef `json:"from"`
	To        GPURef `json:"to"`
	MemoryMB  int64  `json:"memoryMB"`
	Cores     int64  `json:"cores"`
	CrossNode bool   `json:"crossNode"`
}

// Demand is the GPU request of a Pending pod, as HAMi reads it.
type Demand struct {
	MemoryMB      int64 `json:"memoryMB,omitempty"`
	MemoryPercent int64 `json:"memoryPercent,omitempty"`
	Cores         int64 `json:"cores"`
}

// Plan lists the moves, in order, that let one Pending pod fit on one GPU.
type Plan struct {
	ID             string `json:"id"`
	Frees          GPURef `json:"frees"`
	Moves          []Move `json:"moves"`
	CrossNodeMoves int    `json:"crossNodeMoves"`
	MemoryMovedMB  int64  `json:"memoryMovedMB"`
	FreeAfterMB    int64  `json:"freeMemoryAfterMB"`
	Reason         string `json:"reason"`
}

// Outcome of planning for one Pending pod.
const (
	OutcomePlanned = "planned"
	OutcomeFits    = "fits"
	OutcomeNoPlan  = "no_plan"
)

// Result is what the planner found for one Pending pod.
type Result struct {
	Pod          string `json:"pod"`
	PodUID       string `json:"podUID"`
	Demand       Demand `json:"demand"`
	Outcome      string `json:"outcome"`
	Message      string `json:"message,omitempty"`
	Recommended  *Plan  `json:"recommended,omitempty"`
	Alternatives []Plan `json:"alternatives,omitempty"`
}

// Skipped explains why a pod was left out of planning.
type Skipped struct {
	Pod    string `json:"pod"`
	Reason string `json:"reason"`
}

// Execution result values.
const (
	ExecRunning   = "running"
	ExecSucceeded = "succeeded"
	ExecStale     = "stale"
	ExecAborted   = "aborted"
	ExecRaced     = "raced"
	ExecTimeout   = "timeout"
)

// Execution reports how carrying out a plan went.
type Execution struct {
	PlanID     string     `json:"planID"`
	Pod        string     `json:"pod"`
	Result     string     `json:"result"`
	Message    string     `json:"message,omitempty"`
	MovesDone  int        `json:"movesDone"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Report is the outcome of the latest run.
type Report struct {
	Mode       Mode       `json:"mode"`
	Scope      Scope      `json:"scope"`
	ComputedAt time.Time  `json:"computedAt"`
	Error      string     `json:"error,omitempty"`
	Results    []Result   `json:"results"`
	Skipped    []Skipped  `json:"skipped,omitempty"`
	Execution  *Execution `json:"lastExecution,omitempty"`
}
