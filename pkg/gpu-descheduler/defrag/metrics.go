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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pendingPods = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hami_defrag_pending_pods",
		Help: "Unschedulable HAMi pods in the latest defrag run, by what the planner found: planned, fits or no_plan.",
	}, []string{"outcome"})
	executionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hami_defrag_executions_total",
		Help: "Defrag plans carried out, by result: succeeded, stale, aborted, raced or timeout.",
	}, []string{"result"})
	movesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hami_defrag_moves_total",
		Help: "Pods moved by defrag plans, by result: succeeded or failed.",
	}, []string{"result"})
)

func init() {
	metrics.Registry.MustRegister(pendingPods, executionsTotal, movesTotal)
}

func recordResults(results []Result) {
	counts := map[string]float64{OutcomePlanned: 0, OutcomeFits: 0, OutcomeNoPlan: 0}
	for _, r := range results {
		counts[r.Outcome]++
	}
	for outcome, n := range counts {
		pendingPods.WithLabelValues(outcome).Set(n)
	}
}
