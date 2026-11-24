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
	"fmt"
	"math/rand/v2"
	"testing"
)

// model is a cluster reduced to numbers, for an exhaustive search that does
// not share code with the planner.
type model struct {
	gpus    []modelGPU
	pods    []modelPod
	demand  modelPod
	nodeIdx map[string]int
}

type modelGPU struct {
	node       string
	index      int
	mem, share int64
}

type modelPod struct {
	name       string
	gpu        int
	mem, cores int64
	movable    bool
}

func (m *model) usage(where []int) []usage {
	u := make([]usage, len(m.gpus))
	for i, p := range m.pods {
		u[where[i]].mem += p.mem
		u[where[i]].cores += p.cores
		u[where[i]].count++
	}
	return u
}

// fits mirrors HAMi's rules for a GPU with 100 cores.
func (m *model) fits(u usage, g modelGPU, mem, cores int64) bool {
	return u.count < g.share && mem <= g.mem-u.mem && cores <= 100-u.cores &&
		(cores != 100 || u.count == 0) && (u.cores < 100 || cores != 0)
}

func (m *model) demandFits(where []int) bool {
	u := m.usage(where)
	for g := range m.gpus {
		if m.fits(u[g], m.gpus[g], m.demand.mem, m.demand.cores) {
			return true
		}
	}
	return false
}

// optimal returns the fewest moves, up to maxMoves, after which the demand
// fits, where every move must fit at the time it runs. It returns -1 when
// there is none.
func (m *model) optimal(maxMoves int) int {
	start := make([]int, len(m.pods))
	for i, p := range m.pods {
		start[i] = p.gpu
	}
	if m.demandFits(start) {
		return 0
	}
	var search func(where []int, used map[int]bool, left int) bool
	search = func(where []int, used map[int]bool, left int) bool {
		if left == 0 {
			return m.demandFits(where)
		}
		u := m.usage(where)
		for i, p := range m.pods {
			if !p.movable || used[i] {
				continue
			}
			for g := range m.gpus {
				if g == where[i] || !m.fits(u[g], m.gpus[g], p.mem, p.cores) {
					continue
				}
				next := append([]int(nil), where...)
				next[i] = g
				used[i] = true
				ok := search(next, used, left-1)
				delete(used, i)
				if ok {
					return true
				}
			}
		}
		return false
	}
	for k := 1; k <= maxMoves; k++ {
		if search(start, map[int]bool{}, k) {
			return k
		}
	}
	return -1
}

// check replays plan on the model: every move must fit when it runs, and the
// demand must fit on the freed GPU at the end.
func (m *model) check(p *Plan) error {
	where := make([]int, len(m.pods))
	byName := map[string]int{}
	for i, mp := range m.pods {
		where[i] = mp.gpu
		byName["default/"+mp.name] = i
	}
	gpuOf := func(r GPURef) int {
		for g, mg := range m.gpus {
			if mg.node == r.Node && mg.index == r.Index {
				return g
			}
		}
		return -1
	}
	for _, mv := range p.Moves {
		i, ok := byName[mv.Pod]
		if !ok || !m.pods[i].movable || where[i] != gpuOf(mv.From) {
			return fmt.Errorf("move %+v does not match the cluster", mv)
		}
		g := gpuOf(mv.To)
		if !m.fits(m.usage(where)[g], m.gpus[g], m.pods[i].mem, m.pods[i].cores) {
			return fmt.Errorf("move %+v has no room when it runs", mv)
		}
		where[i] = g
	}
	t := gpuOf(p.Frees)
	if !m.fits(m.usage(where)[t], m.gpus[t], m.demand.mem, m.demand.cores) {
		return fmt.Errorf("the demand does not fit on %s after the plan", p.Frees)
	}
	return nil
}

func randomModel(r *rand.Rand) *model {
	m := &model{nodeIdx: map[string]int{}}
	mem := []int64{16, 24, 36, 40, 80}[r.IntN(5)] * gb
	for n := range 1 + r.IntN(2) {
		node := fmt.Sprintf("n%d", n)
		for i := range 1 + r.IntN(3) {
			m.gpus = append(m.gpus, modelGPU{node: node, index: i, mem: mem, share: []int64{4, 10}[r.IntN(2)]})
		}
	}
	for g := range m.gpus {
		freeMem, freeCores := m.gpus[g].mem, int64(100)
		for range r.IntN(5) {
			pm := (1 + r.Int64N(mem/gb/2)) * gb
			pc := []int64{0, 10, 20, 30}[r.IntN(4)]
			if pm <= freeMem && pc <= freeCores {
				m.pods = append(m.pods, modelPod{name: fmt.Sprintf("p%d", len(m.pods)), gpu: g, mem: pm, cores: pc, movable: r.Float64() < 0.85})
				freeMem -= pm
				freeCores -= pc
			}
		}
	}
	m.demand = modelPod{name: "demand", mem: (mem/gb/3 + r.Int64N(mem/gb-mem/gb/3+1)) * gb, cores: []int64{0, 20, 50}[r.IntN(3)]}
	return m
}

func (m *model) inputs() Inputs {
	var in Inputs
	byNode := map[string][]gpuSpec{}
	var order []string
	for _, g := range m.gpus {
		if _, ok := byNode[g.node]; !ok {
			order = append(order, g.node)
		}
		byNode[g.node] = append(byNode[g.node], gpuSpec{memMB: g.mem, share: g.share})
	}
	for _, n := range order {
		in.Nodes = append(in.Nodes, testNode(n, byNode[n]...))
	}
	for _, p := range m.pods {
		pod := runningPod(p.name, m.gpus[p.gpu].node, m.gpus[p.gpu].index, p.mem, p.cores)
		if !p.movable {
			delete(pod.Labels, OptInLabel)
		}
		in.Pods = append(in.Pods, pod)
	}
	in.Pods = append(in.Pods, pendingPodFor(m.demand.name, m.demand.mem, m.demand.cores))
	return in
}

// TestPlannerMatchesExhaustiveSearch checks, on random clusters, that the
// planner finds a plan whenever one of up to two moves exists, that it needs
// no more moves than the best plan, and that every plan it returns works.
func TestPlannerMatchesExhaustiveSearch(t *testing.T) {
	r := rand.New(rand.NewPCG(3483, 1))
	opts := testOptions()
	solvable := 0
	for i := range 4000 {
		m := randomModel(r)
		want := m.optimal(opts.MaxMoves)
		_, results := plan(m.inputs(), opts)
		if len(results) != 1 {
			t.Fatalf("case %d: %d results", i, len(results))
		}
		res := results[0]
		switch {
		case want == 0:
			if res.Outcome != OutcomeFits {
				t.Fatalf("case %d: outcome %q, want fits", i, res.Outcome)
			}
		case want < 0:
			if res.Outcome != OutcomeNoPlan {
				t.Fatalf("case %d: planned %v, but no plan exists", i, movesOf(res.Recommended))
			}
		default:
			solvable++
			if res.Outcome != OutcomePlanned {
				t.Fatalf("case %d: no plan found, but %d moves suffice; model %+v", i, want, m)
			}
			if len(res.Recommended.Moves) != want {
				t.Fatalf("case %d: %d moves, best is %d", i, len(res.Recommended.Moves), want)
			}
			for _, p := range append([]Plan{*res.Recommended}, res.Alternatives...) {
				if err := m.check(&p); err != nil {
					t.Fatalf("case %d: invalid plan %v: %v", i, movesOf(&p), err)
				}
			}
		}
	}
	if solvable < 300 {
		t.Fatalf("only %d solvable cases; the generator is too easy or too hard", solvable)
	}
	t.Logf("%d solvable cases, all optimal", solvable)
}

// BenchmarkPlan plans for a pod on a fragmented cluster of 1,000 GPUs.
func BenchmarkPlan(b *testing.B) {
	r := rand.New(rand.NewPCG(1, 2))
	var in Inputs
	for n := range 125 {
		node := fmt.Sprintf("n%03d", n)
		specs := make([]gpuSpec, 8)
		for i := range specs {
			specs[i] = gpuSpec{memMB: 80 * gb}
		}
		in.Nodes = append(in.Nodes, testNode(node, specs...))
		for i := range 8 {
			free := int64(80 * gb)
			for k := range 1 + r.IntN(4) {
				mem := (4 + r.Int64N(26)) * gb
				if mem > free-6*gb {
					break
				}
				in.Pods = append(in.Pods, runningPod(fmt.Sprintf("%s-%d-%d", node, i, k), node, i, mem, 10))
				free -= mem
			}
		}
	}
	in.Pods = append(in.Pods, pendingPodFor("demand", 79*gb, 50))
	in.Now = testNow
	opts := testOptions()
	b.ResetTimer()
	for b.Loop() {
		s := BuildSnapshot(in, opts)
		if res := PlanAll(s, opts); len(res) != 1 {
			b.Fatal("no result")
		}
	}
}
