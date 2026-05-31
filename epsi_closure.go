package quamina

import "sync"

// tableMark carries the per-smallTable scratch used only during epsilon
// closure computation (lastVisitedGen for NFA walk dedup, and closureGen /
// closureRep for table-pointer dedup). These used to live as fields on
// smallTable itself, but they are purely build-time state and their
// permanent presence on every smallTable was wasted steady-state memory.
// They now live in a side table inside closureBuffers; the buffers are
// pooled and reclaimed by GC, so nothing persists on smallTable.
//
// tableMark is stored by value in closureBuffers.tables so that marking a
// table costs no per-table heap allocation.
type tableMark struct {
	lastVisitedGen uint64
	closureGen     uint64
	closureRep     *faState
}

// closureBuffers carries the scratch for epsilon closure computation. It is
// pooled (see closureBufferPool) and reused across epsilonClosure calls, so
// the maps are allocated once and grown, not rebuilt per call. Visited
// tracking is generation-based: gen only ever increases, so stale map
// entries from a previous use are simply older than the current generation
// and need no clearing.
type closureBuffers struct {
	gen           uint64                    // monotonic counter; bumped by closureForState's two dedup phases
	walkGen       uint64                    // snapshot of gen for the current closureForNfa walk (NFA table dedup)
	closureSetGen uint64                    // snapshot of gen for the current closureForState faState dedup
	closureList   []*faState                // reusable accumulator for the state list before the dedup post-pass
	tables        map[*smallTable]tableMark // per-table scratch (lastVisitedGen, closureGen, closureRep)
	states        map[*faState]uint64       // per-faState last-visited generation, used by traverseEpsilons
}

func newClosureBuffers() *closureBuffers {
	return &closureBuffers{
		tables: make(map[*smallTable]tableMark),
		states: make(map[*faState]uint64),
	}
}

// closureBufferPool reuses closureBuffers (and their maps) across the many
// epsilonClosure calls a build performs, eliminating per-call map allocation.
// The pool is concurrency-safe, and sync.Pool drops its contents on GC, so
// the maps do not become permanent steady-state memory.
var closureBufferPool = sync.Pool{
	New: func() any { return newClosureBuffers() },
}

// epsilonClosure walks the automaton starting from the given table
// and precomputes the epsilon closure for every reachable faState.
func epsilonClosure(table *smallTable) {
	bufs := closureBufferPool.Get().(*closureBuffers)
	// Take a fresh generation for this walk. closureForState bumps bufs.gen
	// for its own dedup phases, but it never touches walkGen, so the table
	// dedup in closureForNfa compares against a value that stays fixed for
	// the whole walk (matching the old snapshot-into-bufs.generation scheme).
	bufs.gen++
	bufs.walkGen = bufs.gen
	closureForNfa(table, bufs)
	closureBufferPool.Put(bufs)
}

func closureForNfa(table *smallTable, bufs *closureBuffers) {
	mark := bufs.tables[table]
	if mark.lastVisitedGen == bufs.walkGen {
		return
	}
	mark.lastVisitedGen = bufs.walkGen
	bufs.tables[table] = mark

	for _, state := range table.steps {
		if state != nil {
			closureForState(state, bufs)
			closureForNfa(state.table, bufs)
		}
	}
	for _, eps := range table.epsilons {
		closureForState(eps, bufs)
		closureForNfa(eps.table, bufs)
	}
}

// closureForStateNoBufs computes the epsilon closure for a single state.
// Used directly in tests; production code uses closureForState.
func closureForStateNoBufs(state *faState) {
	bufs := newClosureBuffers()
	closureForState(state, bufs)
}

func closureForState(state *faState, bufs *closureBuffers) {
	if state.epsilonClosure != nil {
		return
	}

	if len(state.table.epsilons) == 0 {
		state.epsilonClosure = []*faState{state}
		return
	}

	// Generation-based visited tracking: bufs.states records which gen last
	// visited each state, so we never clear the map between traversals.
	bufs.gen++
	bufs.closureSetGen = bufs.gen
	bufs.closureList = bufs.closureList[:0]
	if !state.table.isEpsilonOnly() {
		bufs.states[state] = bufs.closureSetGen
		bufs.closureList = append(bufs.closureList, state)
	}
	traverseEpsilons(state, state.table.epsilons, bufs)

	// Table-pointer dedup: when multiple states in the closure share the
	// same *smallTable, their byte transitions are identical, so only one
	// representative is needed. This is done as a post-pass over the
	// closure list rather than during traversal to keep traverseEpsilons
	// zero-overhead. States with different fieldTransitions are preserved.
	bufs.gen++
	dedupGen := bufs.gen
	closure := make([]*faState, 0, len(bufs.closureList))
	for _, s := range bufs.closureList {
		mark := bufs.tables[s.table]
		if mark.closureGen == dedupGen {
			if sameFieldTransitions(mark.closureRep, s) {
				continue
			}
		} else {
			mark.closureGen = dedupGen
			mark.closureRep = s
			bufs.tables[s.table] = mark
		}
		closure = append(closure, s)
	}
	state.epsilonClosure = closure
}

// traverseEpsilons recursively collects non-epsilon-only states reachable
// via epsilon transitions into bufs.closureList.
func traverseEpsilons(start *faState, epsilons []*faState, bufs *closureBuffers) {
	for _, eps := range epsilons {
		if eps == start || bufs.states[eps] == bufs.closureSetGen {
			continue
		}
		bufs.states[eps] = bufs.closureSetGen
		if !eps.table.isEpsilonOnly() {
			bufs.closureList = append(bufs.closureList, eps)
		}
		traverseEpsilons(start, eps.table.epsilons, bufs)
	}
}

// sameFieldTransitions reports whether two states have identical fieldTransitions.
// This does an order-dependent comparison. If the same field matchers appear in
// different order, we'll miss the dedup — but that just keeps an extra state in
// the closure (a missed optimization, not a correctness bug). In practice,
// fieldTransitions almost always has 0 or 1 element, so ordering doesn't matter.
func sameFieldTransitions(a, b *faState) bool {
	if len(a.fieldTransitions) != len(b.fieldTransitions) {
		return false
	}
	for i, fm := range a.fieldTransitions {
		if fm != b.fieldTransitions[i] {
			return false
		}
	}
	return true
}
