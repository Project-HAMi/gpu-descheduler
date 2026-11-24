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
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
)

// Runner plans on a timer and on demand, and in execute mode carries out one
// plan at a time. It runs only on the leader.
type Runner struct {
	client  client.Client
	opts    Options
	exec    *Executor
	trigger chan struct{}
	now     func() time.Time

	mu        sync.Mutex
	started   bool
	executing bool
	report    *Report
	lastExec  *Execution
}

// NewRunner returns a runner that reads and writes through c, and reads the
// controllers of moved pods through owners.
func NewRunner(c client.Client, owners client.Reader, opts Options) *Runner {
	r := &Runner{client: c, opts: opts, trigger: make(chan struct{}, 1), now: time.Now}
	r.exec = &Executor{Client: c, Owners: owners, Opts: opts, Refresh: r.snapshot, Poll: 2 * time.Second, Now: r.now}
	return r
}

// NeedLeaderElection makes the manager start the runner on the leader only.
func (r *Runner) NeedLeaderElection() bool { return true }

// Start runs until ctx is done.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	klog.InfoS("Starting GPU defragmentation", "mode", r.opts.Mode, "scope", r.opts.Scope, "interval", r.opts.Interval)
	ticker := time.NewTicker(r.opts.Interval)
	defer ticker.Stop()
	for {
		r.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-r.trigger:
		}
	}
}

// TriggerResult is the answer to a manual run request.
type TriggerResult int

const (
	TriggerAccepted TriggerResult = iota
	TriggerNotLeader
	TriggerBusy
)

// Trigger asks for a run now.
func (r *Runner) Trigger() TriggerResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case !r.started:
		return TriggerNotLeader
	case r.executing:
		return TriggerBusy
	}
	select {
	case r.trigger <- struct{}{}:
	default:
	}
	return TriggerAccepted
}

// Report returns the latest report, or nil before the first run or when
// this replica is not the leader.
func (r *Runner) Report() *Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.report == nil {
		return nil
	}
	out := *r.report
	if r.lastExec != nil {
		e := *r.lastExec
		out.Execution = &e
	}
	return &out
}

// RunOnce plans once, and in execute mode carries out the first plan.
func (r *Runner) RunOnce(ctx context.Context) {
	report := &Report{Mode: r.opts.Mode, Scope: r.opts.Scope, ComputedAt: r.now()}
	snap, err := r.snapshot(ctx)
	if err != nil {
		report.Error = err.Error()
		klog.ErrorS(err, "Could not read the cluster for GPU defragmentation")
		r.setReport(report)
		return
	}
	report.Results = PlanAll(snap, r.opts)
	report.Skipped = snap.Skipped()
	recordResults(report.Results)
	r.setReport(report)
	for _, res := range report.Results {
		if res.Outcome == OutcomePlanned {
			klog.InfoS("Planned GPU defragmentation", "pod", res.Pod, "plan", res.Recommended.ID,
				"reason", res.Recommended.Reason, "moves", len(res.Recommended.Moves), "mode", r.opts.Mode)
		}
	}
	if r.opts.Mode != ModeExecute {
		return
	}
	if snap.InFlight() {
		klog.V(2).InfoS("A defrag placement is still in progress, not starting another plan")
		return
	}
	for _, res := range report.Results {
		if res.Outcome != OutcomePlanned {
			continue
		}
		r.mu.Lock()
		r.executing = true
		r.lastExec = &Execution{PlanID: res.Recommended.ID, Pod: res.Pod, Result: ExecRunning, StartedAt: r.now()}
		r.mu.Unlock()
		exec := r.exec.Execute(ctx, res, func(e Execution) { r.setExecution(&e) })
		r.mu.Lock()
		r.executing = false
		r.lastExec = &exec
		r.mu.Unlock()
		return
	}
}

func (r *Runner) setReport(report *Report) {
	r.mu.Lock()
	r.report = report
	r.mu.Unlock()
}

func (r *Runner) setExecution(e *Execution) {
	r.mu.Lock()
	r.lastExec = e
	r.mu.Unlock()
}

// snapshot reads the cluster from the manager's cache.
func (r *Runner) snapshot(ctx context.Context) (*Snapshot, error) {
	var (
		nodes      corev1.NodeList
		pods       corev1.PodList
		pdbs       policyv1.PodDisruptionBudgetList
		placements placementv1.HamiPodPlacementList
	)
	for _, list := range []client.ObjectList{&nodes, &pods, &pdbs, &placements} {
		if err := r.client.List(ctx, list); err != nil {
			return nil, err
		}
	}
	return BuildSnapshot(Inputs{
		Nodes: nodes.Items, Pods: pods.Items, PDBs: pdbs.Items, Placements: placements.Items, Now: r.now(),
	}, r.opts), nil
}
