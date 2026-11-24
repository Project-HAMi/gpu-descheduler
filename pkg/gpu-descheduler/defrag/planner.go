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
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
)

// maxExhaustive is the largest number of movable pods on one GPU for which
// every subset is tried. Above it, pods are taken largest first.
const maxExhaustive = 12

// placementAnnotation is set by the gpu-descheduler webhook on a pod it
// pinned. The pin it injected (nvidia.com/use-gpuuuid) is not part of the
// workload, and a replacement pod gets a new one, so a moved pod is not held
// to it.
const placementAnnotation = "hami.placement.com/placement"

// PlanAll plans for the Pending pods in s, oldest first. Each recommended
// plan, and the Pending pod it is for, is applied to the simulated state
// before the next pod is planned, so two plans never count on the same space.
func PlanAll(s *Snapshot, opts Options) []Result {
	p := &planner{s: s, opts: opts, use: slices.Clone(s.base), moved: map[int]bool{}, static: map[*corev1.Pod][]bool{}, bySig: map[string][]bool{}}
	var results []Result
	for _, d := range s.pending {
		if len(results) == opts.MaxPlans {
			break
		}
		results = append(results, p.planFor(d))
	}
	return results
}

type planner struct {
	s    *Snapshot
	opts Options
	use  []usage
	// before is use as it was when planning for the current pod started.
	before []usage
	moved  map[int]bool
	static map[*corev1.Pod][]bool
	bySig  map[string][]bool
}

// candidate is a plan before it is reported.
type candidate struct {
	target int
	moves  []plannedMove
	cross  int
	memMB  int64
	free   int64
}

type plannedMove struct {
	alloc int
	to    int
}

func (p *planner) planFor(d *pendingPod) Result {
	res := Result{Pod: d.key, PodUID: d.uid, Demand: d.req.demand()}
	var targets []int
	for g := range p.s.gpus {
		if p.row(d.req, d.pod.Annotations, d.pod)[g] {
			targets = append(targets, g)
		}
	}
	if len(targets) == 0 {
		res.Outcome = OutcomeNoPlan
		res.Message = "no GPU passes the pod's type, UUID, mode, node and size rules"
		return res
	}
	for _, g := range targets {
		if p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), g) {
			res.Outcome = OutcomeFits
			res.Message = "fits on " + p.s.gpus[g].Ref.String() + " without moves, so it waits for something other than GPU space"
			p.add(g, d.req.memOn(p.s.gpus[g]), d.req.cores)
			return res
		}
	}
	p.before = slices.Clone(p.use)
	var cands []candidate
	for _, t := range targets {
		cands = append(cands, p.direct(d, t)...)
		if p.opts.MaxMoves >= 2 {
			cands = append(cands, p.makeRoom(d, t)...)
		}
	}
	if len(cands) == 0 {
		res.Outcome = OutcomeNoPlan
		res.Message = fmt.Sprintf("no plan of up to %d moves frees enough space", p.opts.MaxMoves)
		return res
	}
	slices.SortFunc(cands, p.compare)
	cands = slices.CompactFunc(cands, func(a, b candidate) bool { return p.compare(a, b) == 0 })
	res.Outcome = OutcomePlanned
	best := p.report(d, cands[0])
	res.Recommended = &best
	for _, c := range cands[1:min(len(cands), 1+p.opts.Alternatives)] {
		res.Alternatives = append(res.Alternatives, p.report(d, c))
	}
	p.commit(d, cands[0])
	return res
}

// direct finds plans that move pods off t only. Subsets are tried in order of
// size, and each moved pod goes to the GPU it fits most tightly.
func (p *planner) direct(d *pendingPod, t int) []candidate {
	movable := p.movableOn(t)
	if len(movable) == 0 || !p.fitsWithout(d, t, movable) {
		return nil
	}
	var out []candidate
	try := func(subset []int) {
		if !p.pdbAllows(subset) {
			return
		}
		for _, a := range subset {
			p.remove(a)
		}
		if p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), t) {
			if moves := p.place(subset, t); moves != nil {
				out = append(out, p.candidate(t, moves))
			}
		}
		for _, a := range subset {
			p.restore(a)
		}
	}
	if len(movable) > maxExhaustive {
		// Largest first: the shortest prefix that makes room.
		for k := 1; k <= min(p.opts.MaxMoves, len(movable)); k++ {
			try(movable[:k])
		}
		return out
	}
	for k := 1; k <= min(p.opts.MaxMoves, len(movable)); k++ {
		combinations(len(movable), k, func(idx []int) {
			subset := make([]int, k)
			for i, j := range idx {
				subset[i] = movable[j]
			}
			try(subset)
		})
	}
	return out
}

// makeRoom finds two-move plans for when a pod p moved off t fits on another
// GPU only after a pod q there moves first. q may go to t itself, a swap,
// when it fits on t while p is still there. q always moves first, so each
// move has room when it runs.
func (p *planner) makeRoom(d *pendingPod, t int) []candidate {
	var out []candidate
	for _, a := range p.movableOn(t) {
		p.remove(a)
		if !p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), t) || p.bestTarget(a, t, -1) >= 0 {
			p.restore(a)
			continue
		}
		pa := p.s.allocs[a]
		for g := range p.s.gpus {
			if g == t || !p.inScope(pa, g) || !p.allocRow(pa)[g] {
				continue
			}
			for _, b := range p.movableOn(g) {
				if !p.pdbAllows([]int{a, b}) {
					continue
				}
				p.remove(b)
				if p.fits(pa.req, p.allocRow(pa), g) {
					if h := p.bestTarget(b, g, t); h >= 0 {
						out = append(out, p.candidate(t, []plannedMove{{b, h}, {a, g}}))
					}
					if swap := p.swap(d, t, a, b); swap != nil {
						out = append(out, *swap)
					}
				}
				p.restore(b)
			}
		}
		p.restore(a)
	}
	return out
}

// swap returns the plan that moves b onto t and then a onto b's GPU, when b
// fits on t while a is still there and d still fits on t with b on it.
// Callers have removed a from t and b from its GPU.
func (p *planner) swap(d *pendingPod, t, a, b int) *candidate {
	pb := p.s.allocs[b]
	if !p.inScope(pb, t) {
		return nil
	}
	p.restore(a)
	fitsFirst := p.fits(pb.req, p.allocRow(pb), t)
	p.remove(a)
	if !fitsFirst {
		return nil
	}
	p.add(t, pb.req.memOn(p.s.gpus[t]), pb.req.cores)
	ok := p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), t)
	p.sub(t, pb.req.memOn(p.s.gpus[t]), pb.req.cores)
	if !ok {
		return nil
	}
	c := p.candidate(t, []plannedMove{{b, t}, {a, pb.gpu}})
	return &c
}

// place puts the pods of subset, largest first, each on the GPU it fits most
// tightly, preferring its own node. When that fails for more than one pod,
// it tries again by tightest fit alone. Placements are undone before it
// returns.
func (p *planner) place(subset []int, t int) []plannedMove {
	order := slices.Clone(subset)
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(p.s.allocs[b].memMB, p.s.allocs[a].memMB) })
	for _, preferSameNode := range []bool{true, false} {
		var moves []plannedMove
		for _, a := range order {
			g := p.bestTargetBy(a, t, -1, preferSameNode)
			if g < 0 {
				break
			}
			pa := p.s.allocs[a]
			p.add(g, pa.req.memOn(p.s.gpus[g]), pa.req.cores)
			moves = append(moves, plannedMove{a, g})
		}
		for _, m := range moves {
			pa := p.s.allocs[m.alloc]
			p.sub(m.to, pa.req.memOn(p.s.gpus[m.to]), pa.req.cores)
		}
		if len(moves) == len(order) {
			return moves
		}
		if len(order) == 1 {
			return nil
		}
	}
	return nil
}

func (p *planner) bestTarget(a, exclude1, exclude2 int) int {
	return p.bestTargetBy(a, exclude1, exclude2, true)
}

// bestTargetBy returns the GPU a fits on with the least memory left over,
// other than its own and the excluded ones, or -1.
func (p *planner) bestTargetBy(a, exclude1, exclude2 int, preferSameNode bool) int {
	pa := p.s.allocs[a]
	row := p.allocRow(pa)
	best, bestCross, bestLeft := -1, false, int64(0)
	for g, gpu := range p.s.gpus {
		if g == pa.gpu || g == exclude1 || g == exclude2 || !p.inScope(pa, g) || !p.fits(pa.req, row, g) {
			continue
		}
		cross := preferSameNode && gpu.Ref.Node != p.s.gpus[pa.gpu].Ref.Node
		left := gpu.MemTotal - p.use[g].mem - pa.req.memOn(gpu)
		if best < 0 || (!cross && bestCross) || (cross == bestCross && left < bestLeft) {
			best, bestCross, bestLeft = g, cross, left
		}
	}
	return best
}

// row reports, for every GPU, whether pod could fit on it when it is empty
// enough: everything except the space in use. The answer only depends on the
// pod's request and scheduling constraints, so it is worked out once for all
// pods that share them.
func (p *planner) row(req request, annos map[string]string, pod *corev1.Pod) []bool {
	ok, cached := p.static[pod]
	if !cached {
		sig := constraintKey(req, annos, pod)
		if ok, cached = p.bySig[sig]; !cached {
			ok = make([]bool, len(p.s.gpus))
			for i, gpu := range p.s.gpus {
				ok[i] = gpu.Healthy && !gpu.busy && !strings.EqualFold(gpu.Mode, migMode) &&
					req.memOn(gpu) <= gpu.MemTotal && req.cores <= gpu.CoreTotal && gpu.ShareLimit > 0 &&
					typeAllowed(annos, gpu.Type) && uuidAllowed(annos, gpu.Ref.UUID) && modeAllowed(annos, gpu.Mode) &&
					p.s.nodeAccepts(pod, gpu.node)
			}
			p.bySig[sig] = ok
		}
		p.static[pod] = ok
	}
	return ok
}

// allocRow is row for the replacement of a moved pod.
func (p *planner) allocRow(pa *podAlloc) []bool {
	if ok, cached := p.static[pa.pod]; cached {
		return ok
	}
	return p.row(pa.req, p.annotations(pa), pa.pod)
}

// constraintKey describes everything row reads from a pod.
func constraintKey(req request, annos map[string]string, pod *corev1.Pod) string {
	data, _ := json.Marshal(struct {
		Req         [3]int64
		Annos       [5]string
		Selector    map[string]string
		Affinity    *corev1.Affinity
		Tolerations []corev1.Toleration
	}{
		[3]int64{req.memMB, req.memPercent, req.cores},
		[5]string{annos[useGPUTypeAnnotation], annos[noUseGPUTypeAnnotation], annos[useGPUUUIDAnnotation], annos[noUseGPUUUIDAnnotation], annos[vgpuModeAnnotation]},
		pod.Spec.NodeSelector, pod.Spec.Affinity, pod.Spec.Tolerations,
	})
	return string(data)
}

// fits mirrors the checks of HAMi's NVIDIA Fit for one GPU. row is the
// pod's row.
func (p *planner) fits(req request, row []bool, g int) bool {
	if !row[g] {
		return false
	}
	gpu, u := p.s.gpus[g], p.use[g]
	switch {
	case u.count >= gpu.ShareLimit:
		return false
	case req.memOn(gpu) > gpu.MemTotal-u.mem:
		return false
	case req.cores > gpu.CoreTotal-u.cores:
		return false
	case gpu.CoreTotal == 100 && req.cores == 100 && u.count > 0:
		// A pod asking for all cores wants the GPU to itself.
		return false
	case gpu.CoreTotal != 0 && u.cores >= gpu.CoreTotal && req.cores == 0:
		return false
	}
	return true
}

// fitsWithout reports whether d would fit on t once all movable pods left,
// which bounds every plan for t.
func (p *planner) fitsWithout(d *pendingPod, t int, movable []int) bool {
	for _, a := range movable {
		p.remove(a)
	}
	ok := p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), t)
	for _, a := range movable {
		p.restore(a)
	}
	return ok
}

// nodeAccepts reports whether pod's node selector, required node affinity
// and tolerations allow node, and whether the node takes new pods.
func (s *Snapshot) nodeAccepts(pod *corev1.Pod, node *corev1.Node) bool {
	key := acceptKey{pod, node}
	if ok, cached := s.accepts[key]; cached {
		return ok
	}
	ok := !node.Spec.Unschedulable && nodeReady(node)
	if ok {
		match, err := nodeaffinity.GetRequiredNodeAffinity(pod).Match(node)
		ok = err == nil && match
	}
	if ok {
		_, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}, false)
		ok = !untolerated
	}
	s.accepts[key] = ok
	return ok
}

func nodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (p *planner) inScope(pa *podAlloc, g int) bool {
	return p.opts.Scope == ScopeCrossNode || p.s.gpus[g].Ref.Node == p.s.gpus[pa.gpu].Ref.Node
}

// annotations returns the annotations a moved pod's replacement is checked
// against: the pod's own, without a GPU pin the webhook injected.
func (p *planner) annotations(pa *podAlloc) map[string]string {
	if _, pinned := pa.pod.Annotations[placementAnnotation]; !pinned {
		return pa.pod.Annotations
	}
	annos := maps.Clone(pa.pod.Annotations)
	delete(annos, useGPUUUIDAnnotation)
	return annos
}

// movableOn returns the movable pods on g, largest first.
func (p *planner) movableOn(g int) []int {
	var out []int
	for _, a := range p.s.onGPU[g] {
		if p.s.allocs[a].movable && !p.moved[a] {
			out = append(out, a)
		}
	}
	slices.SortStableFunc(out, func(a, b int) int {
		pa, pb := p.s.allocs[a], p.s.allocs[b]
		return cmp.Or(cmp.Compare(pb.memMB, pa.memMB), cmp.Compare(pa.key, pb.key))
	})
	return out
}

// pdbAllows reports whether moving allocs stays within every PDB's budget.
func (p *planner) pdbAllows(allocs []int) bool {
	used := map[string]int32{}
	for _, a := range allocs {
		for _, key := range p.s.allocs[a].pdbs {
			used[key]++
			if used[key] > p.s.pdbAllowed[key] {
				return false
			}
		}
	}
	return true
}

// add puts a pod using mem and cores on g, sub takes it off again.
func (p *planner) add(g int, mem, cores int64) {
	p.use[g].mem += mem
	p.use[g].cores += cores
	p.use[g].count++
}

func (p *planner) sub(g int, mem, cores int64) {
	p.use[g].mem -= mem
	p.use[g].cores -= cores
	p.use[g].count--
}

// remove takes alloc a off its GPU, restore puts it back.
func (p *planner) remove(a int) {
	pa := p.s.allocs[a]
	p.use[pa.gpu].mem -= pa.memMB
	p.use[pa.gpu].cores -= pa.cores
	p.use[pa.gpu].count--
}

func (p *planner) restore(a int) {
	pa := p.s.allocs[a]
	p.use[pa.gpu].mem += pa.memMB
	p.use[pa.gpu].cores += pa.cores
	p.use[pa.gpu].count++
}

// candidate measures a plan against the state before planning for this pod.
func (p *planner) candidate(t int, moves []plannedMove) candidate {
	c := candidate{target: t, moves: moves, free: p.s.gpus[t].MemTotal - p.before[t].mem}
	for _, m := range moves {
		pa := p.s.allocs[m.alloc]
		if p.s.gpus[m.to].Ref.Node != p.s.gpus[pa.gpu].Ref.Node {
			c.cross++
		}
		c.memMB += pa.memMB
		if pa.gpu == t {
			c.free += pa.memMB
		}
		if m.to == t {
			c.free -= pa.req.memOn(p.s.gpus[t])
		}
	}
	return c
}

// compare ranks plans: fewest moves, fewest cross-node moves, least memory
// moved, most memory left free on the target, then a stable order.
func (p *planner) compare(a, b candidate) int {
	return cmp.Or(
		cmp.Compare(len(a.moves), len(b.moves)),
		cmp.Compare(a.cross, b.cross),
		cmp.Compare(a.memMB, b.memMB),
		cmp.Compare(b.free, a.free),
		cmp.Compare(p.s.gpus[a.target].Ref.Node, p.s.gpus[b.target].Ref.Node),
		cmp.Compare(p.s.gpus[a.target].Ref.Index, p.s.gpus[b.target].Ref.Index),
		cmp.Compare(p.movesKey(a), p.movesKey(b)),
	)
}

func (p *planner) movesKey(c candidate) string {
	var sb strings.Builder
	for _, m := range c.moves {
		fmt.Fprintf(&sb, "%s>%s;", p.s.allocs[m.alloc].key, p.s.gpus[m.to].Ref.UUID)
	}
	return sb.String()
}

func (p *planner) report(d *pendingPod, c candidate) Plan {
	t := p.s.gpus[c.target]
	plan := Plan{
		Frees:          t.Ref,
		CrossNodeMoves: c.cross,
		MemoryMovedMB:  c.memMB,
		FreeAfterMB:    c.free,
		Reason:         fmt.Sprintf("frees %dMB on %s for %s", c.free, t.Ref, d.key),
	}
	for _, m := range c.moves {
		pa := p.s.allocs[m.alloc]
		from, to := p.s.gpus[pa.gpu].Ref, p.s.gpus[m.to].Ref
		plan.Moves = append(plan.Moves, Move{
			Pod: pa.key, PodUID: pa.uid, OwnerUID: pa.ownerUID,
			From: from, To: to, MemoryMB: pa.memMB, Cores: pa.cores,
			CrossNode: from.Node != to.Node,
		})
	}
	h := fnv.New32a()
	h.Write([]byte(d.uid + "|" + p.movesKey(c)))
	plan.ID = fmt.Sprintf("%08x", h.Sum32())
	return plan
}

// commit applies c's moves, and d on c's target, to the simulated state.
func (p *planner) commit(d *pendingPod, c candidate) {
	for _, m := range c.moves {
		pa := p.s.allocs[m.alloc]
		p.remove(m.alloc)
		p.add(m.to, pa.req.memOn(p.s.gpus[m.to]), pa.req.cores)
		p.moved[m.alloc] = true
	}
	p.add(c.target, d.req.memOn(p.s.gpus[c.target]), d.req.cores)
}

// combinations calls fn with every k-subset of 0..n-1, in lexicographic
// order. fn must not keep idx.
func combinations(n, k int, fn func(idx []int)) {
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for {
		fn(idx)
		i := k - 1
		for i >= 0 && idx[i] == n-k+i {
			i--
		}
		if i < 0 {
			return
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
}

// Verify checks, on a fresh snapshot, that the moves of plan from index from
// on can still be made in order and that the Pending pod with podUID then
// fits on the GPU the plan frees. It returns the pod the next move evicts.
func (s *Snapshot) Verify(plan *Plan, podUID string, from int, opts Options) (*corev1.Pod, error) {
	p := &planner{s: s, opts: opts, use: slices.Clone(s.base), moved: map[int]bool{}, static: map[*corev1.Pod][]bool{}, bySig: map[string][]bool{}}
	var d *pendingPod
	for _, pp := range s.pending {
		if pp.uid == podUID {
			d = pp
		}
	}
	if d == nil {
		return nil, fmt.Errorf("pod is no longer unschedulable")
	}
	t, ok := s.byUUID[plan.Frees.UUID]
	if !ok {
		return nil, fmt.Errorf("GPU %s is gone", plan.Frees)
	}
	var next *corev1.Pod
	for i := from; i < len(plan.Moves); i++ {
		mv := plan.Moves[i]
		a := slices.IndexFunc(s.allocs, func(pa *podAlloc) bool { return pa.uid == mv.PodUID })
		if a < 0 || !s.allocs[a].movable || s.gpus[s.allocs[a].gpu].Ref.UUID != mv.From.UUID {
			return nil, fmt.Errorf("pod %s is gone, moved, or may no longer be moved", mv.Pod)
		}
		pa := s.allocs[a]
		to, ok := s.byUUID[mv.To.UUID]
		if !ok || !p.inScope(pa, to) || !p.fits(pa.req, p.allocRow(pa), to) {
			return nil, fmt.Errorf("pod %s no longer fits on %s", mv.Pod, mv.To)
		}
		p.remove(a)
		p.add(to, pa.req.memOn(s.gpus[to]), pa.req.cores)
		if next == nil {
			next = pa.pod
		}
	}
	if !p.fits(d.req, p.row(d.req, d.pod.Annotations, d.pod), t) {
		return nil, fmt.Errorf("pod would no longer fit on %s after the moves", plan.Frees)
	}
	return next, nil
}
