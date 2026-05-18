package quamina

import (
	"bytes"
	"slices"
	"sync"
	"sync/atomic"
	"unsafe"
)

// lazyDFA is a shared, lock-free-on-hit cache of DFA states materialised
// on demand during NFA traversal. Its lifetime equals the lifetime of the
// coreFields snapshot it hangs off, so AddPattern's CoW swap invalidates
// the cache for free.
type lazyDFA struct {
	cache      sync.Map // stateKey (string) → *lazyDFAState
	cacheBytes atomic.Uint64
	budget     uint64 // immutable, set at construction
	insertMu   sync.Mutex // held only during cache-miss insert
	stats      lazyDFAStats
}

// cacheLineBytes is the standard x86/arm64 cache line size. On Apple M-series
// (arm64) the physical line is 128 bytes, but the coherency granule (the unit
// that triggers false-sharing) is 64 bytes, so 64-byte padding is sufficient
// on all common architectures.
const cacheLineBytes = 64

// lazyDFAStats keeps each atomic counter on its own cache line to avoid
// false sharing under concurrent matches. The hits counter is incremented
// on every cache hit (i.e., almost every step on hot patterns); with all
// counters sharing one cache line, parallel matchers contend on the line
// and throughput collapses.
type lazyDFAStats struct {
	hits            atomic.Uint64
	_               [cacheLineBytes - 8]byte
	misses          atomic.Uint64
	_               [cacheLineBytes - 8]byte
	scratchFallback atomic.Uint64
	_               [cacheLineBytes - 8]byte
	stateCount      atomic.Uint64
	_               [cacheLineBytes - 8]byte
}

// lazyDFAState is a DFA state — a set of simultaneously-active NFA states
// — published to the cache. After publication, only transitions changes
// (via atomic.Pointer + transMu serialization for appends).
type lazyDFAState struct {
	transitions      atomic.Pointer[lazyTransitions]
	transMu          sync.Mutex
	fieldTransitions []*fieldMatcher
	nfaStates        []*faState
	cached           bool // true when published into the cache
}

// lazyTransitions is immutable once stored. Updates allocate a new struct
// and atomic.Store it on the owning lazyDFAState.
type lazyTransitions struct {
	keys   []byte
	values []*lazyDFAState
}

func newLazyDFA(budget uint64) *lazyDFA {
	return &lazyDFA{budget: budget}
}

// LazyDFAStats is a snapshot of the lazy DFA cache's behavior.
// Safe to read concurrently with MatchesForEvent.
type LazyDFAStats struct {
	Enabled         bool
	CacheBytes      uint64
	Budget          uint64
	StateCount      uint64
	TransitionHits  uint64
	TransitionMiss  uint64
	ScratchFallback uint64
}

// snapshot reads counters from ld (which may be nil if caching is disabled
// or not yet initialised) into a stats struct for return to the caller.
func (ld *lazyDFA) snapshot() LazyDFAStats {
	if ld == nil {
		return LazyDFAStats{}
	}
	return LazyDFAStats{
		Enabled:         true,
		CacheBytes:      ld.cacheBytes.Load(),
		Budget:          ld.budget,
		StateCount:      ld.stats.stateCount.Load(),
		TransitionHits:  ld.stats.hits.Load(),
		TransitionMiss:  ld.stats.misses.Load(),
		ScratchFallback: ld.stats.scratchFallback.Load(),
	}
}

// computeKey builds a cache key from a set of NFA states into bufs.lazyKeyBuf,
// reusing scratch buffers to avoid allocation. The returned slice is only
// valid until the next computeKey call on the same bufs.
func computeKey(states []*faState, bufs *nfaBuffers) []byte {
	if len(states) == 0 {
		return bufs.lazyKeyBuf[:0]
	}

	if cap(bufs.lazySortBuf) < len(states) {
		bufs.lazySortBuf = make([]*faState, len(states))
	}
	bufs.lazySortBuf = bufs.lazySortBuf[:len(states)]
	copy(bufs.lazySortBuf, states)
	slices.SortFunc(bufs.lazySortBuf, func(a, b *faState) int {
		addrA := uintptr(unsafe.Pointer(a))
		addrB := uintptr(unsafe.Pointer(b))
		if addrA < addrB {
			return -1
		}
		if addrA > addrB {
			return 1
		}
		return 0
	})

	needed := len(states) * 8
	if cap(bufs.lazyKeyBuf) < needed {
		bufs.lazyKeyBuf = make([]byte, needed)
	}
	bufs.lazyKeyBuf = bufs.lazyKeyBuf[:needed]
	for i, s := range bufs.lazySortBuf {
		addr := uintptr(unsafe.Pointer(s))
		off := i * 8
		bufs.lazyKeyBuf[off] = byte(addr)
		bufs.lazyKeyBuf[off+1] = byte(addr >> 8)
		bufs.lazyKeyBuf[off+2] = byte(addr >> 16)
		bufs.lazyKeyBuf[off+3] = byte(addr >> 24)
		bufs.lazyKeyBuf[off+4] = byte(addr >> 32)
		bufs.lazyKeyBuf[off+5] = byte(addr >> 40)
		bufs.lazyKeyBuf[off+6] = byte(addr >> 48)
		bufs.lazyKeyBuf[off+7] = byte(addr >> 56)
	}
	return bufs.lazyKeyBuf
}

// makeState builds a fresh *lazyDFAState from the given NFA state set.
// nfaStates is copied (caller may pass scratch). fieldTransitions are
// deduplicated using bufs.lazySeenFields. The returned state has
// cached=false; the caller (lookupOrInsert) sets it to true on publish.
func makeState(nfaStates []*faState, bufs *nfaBuffers) *lazyDFAState {
	if bufs.lazySeenFields == nil {
		bufs.lazySeenFields = make(map[*fieldMatcher]bool)
	}
	copied := make([]*faState, len(nfaStates))
	copy(copied, nfaStates)
	state := &lazyDFAState{nfaStates: copied}

	clear(bufs.lazySeenFields)
	for _, n := range nfaStates {
		for _, ft := range n.fieldTransitions {
			if !bufs.lazySeenFields[ft] {
				bufs.lazySeenFields[ft] = true
				state.fieldTransitions = append(state.fieldTransitions, ft)
			}
		}
	}
	return state
}

// estimateBytes returns an approximate retained cost (in bytes) of a
// *lazyDFAState that's about to be published into the cache. Covers:
//   - the lazyDFAState struct itself + map entry overhead
//   - the map key string (len(nfaStates)*8 bytes)
//   - the backing arrays for nfaStates and fieldTransitions
//
// Does NOT cover the lazyTransitions struct (initially nil) — that grows
// during use, but the prototype data shows transitions average <10 bytes
// of overhead per state. Acceptable approximation per the design spec.
func estimateBytes(s *lazyDFAState) uint64 {
	const stateOverhead = 96 // lazyDFAState struct + map entry overhead
	const keyByteCost = 8    // each state pointer in the key string
	const ptrSize = 8
	return uint64(stateOverhead +
		len(s.nfaStates)*(keyByteCost+ptrSize) +
		len(s.fieldTransitions)*ptrSize)
}

// lookupOrInsertStart is like lookupOrInsert but for the NFA table's start
// state. It uses the *smallTable pointer as the cache key (via computeStartKey)
// and constructs a stable, heap-allocated *faState{table: table} for the cached
// lazyDFAState.nfaStates — never bufs.startState, which is a mutable per-goroutine
// scratch pointer that would cause a data race if stored in the shared cache.
func (ld *lazyDFA) lookupOrInsertStart(key []byte, table *smallTable, bufs *nfaBuffers) *lazyDFAState {
	if existing, ok := ld.cache.Load(string(key)); ok {
		return existing.(*lazyDFAState)
	}

	ld.insertMu.Lock()
	defer ld.insertMu.Unlock()

	if existing, ok := ld.cache.Load(string(key)); ok {
		return existing.(*lazyDFAState)
	}

	// Build a stable start faState for this table — allocated once, then
	// immutable, so computeStep can read .table without races.
	stable := &faState{table: table}
	stable.epsilonClosure = []*faState{stable}
	nfaStates := stable.epsilonClosure

	newState := makeState(nfaStates, bufs)
	cost := estimateBytes(newState)
	if ld.cacheBytes.Load()+cost > ld.budget {
		return nil // caller falls back to scratch state
	}
	newState.cached = true
	ld.cache.Store(string(key), newState)
	ld.cacheBytes.Add(cost)
	ld.stats.stateCount.Add(1)
	return newState
}

// lookupOrInsert returns the *lazyDFAState for the given NFA state set.
// On cache hit, returns the existing entry. On cache miss, inserts a fresh
// state (built via makeState) and returns it. If the new state would push
// cacheBytes past the budget, returns nil — caller falls back to scratch.
//
// The key bytes come from computeKey(...) and must not be mutated until
// this call returns; the function may store a copy into the cache map.
func (ld *lazyDFA) lookupOrInsert(key []byte, nfaStates []*faState, bufs *nfaBuffers) *lazyDFAState {
	if existing, ok := ld.cache.Load(string(key)); ok {
		return existing.(*lazyDFAState)
	}

	ld.insertMu.Lock()
	defer ld.insertMu.Unlock()

	// Re-check under lock — another goroutine may have inserted.
	if existing, ok := ld.cache.Load(string(key)); ok {
		return existing.(*lazyDFAState)
	}

	newState := makeState(nfaStates, bufs)
	cost := estimateBytes(newState)
	if ld.cacheBytes.Load()+cost > ld.budget {
		return nil // caller falls back to scratch state
	}
	newState.cached = true
	ld.cache.Store(string(key), newState)
	ld.cacheBytes.Add(cost)
	ld.stats.stateCount.Add(1)
	return newState
}

// appendTransition publishes (b → nextState) on state. Hot-path readers
// observe either the old transitions or the new one — never a torn write
// — because we always atomic.Store a fresh immutable lazyTransitions.
//
// state.transMu serializes appends to this state's transitions, so two
// goroutines computing different bytes on the same state can both publish
// without a CAS retry loop. Mutex is dead after warmup.
func appendTransition(state *lazyDFAState, b byte, nextState *lazyDFAState) {
	state.transMu.Lock()
	defer state.transMu.Unlock()

	// cur may be nil if this is the first transition on the state.
	cur := state.transitions.Load()
	var curKeys []byte
	var curValues []*lazyDFAState
	if cur != nil {
		if idx := bytes.IndexByte(cur.keys, b); idx >= 0 {
			return // someone else added it; their nextState is identical
		}
		curKeys = cur.keys
		curValues = cur.values
	}

	newKeys := make([]byte, 0, len(curKeys)+1)
	newKeys = append(newKeys, curKeys...)
	newKeys = append(newKeys, b)

	newValues := make([]*lazyDFAState, 0, len(curValues)+1)
	newValues = append(newValues, curValues...)
	newValues = append(newValues, nextState)

	state.transitions.Store(&lazyTransitions{keys: newKeys, values: newValues})
}

// computeStep is the slow path of step(). Computes the next NFA state set
// from state.nfaStates under byte b (expanding epsilon closure), then either:
//   - returns nil if the step is a dead end
//   - returns &bufs.lazyScratchState if the current state isn't cached
//   - returns the cached *lazyDFAState (creating + appending to state.transitions
//     if needed), or scratch state if the cache is full
func (ld *lazyDFA) computeStep(state *lazyDFAState, b byte, bufs *nfaBuffers) *lazyDFAState {
	ld.stats.misses.Add(1)

	// Ping-pong: write next NFA states into the slot NOT holding state.nfaStates.
	writeIdx := 1 - bufs.lazyScratchNFAIdx
	next := bufs.lazyScratchNFA[writeIdx][:0]
	bufs.lazyStepGen++
	gen := bufs.lazyStepGen
	if bufs.lazySeenStates == nil {
		bufs.lazySeenStates = make(map[*faState]uint64)
	}

	for _, n := range state.nfaStates {
		if n.table == nil {
			continue
		}
		nextStep := n.table.step(b)
		if nextStep == nil {
			continue
		}
		for _, ec := range nextStep.epsilonClosure {
			if bufs.lazySeenStates[ec] != gen {
				bufs.lazySeenStates[ec] = gen
				next = append(next, ec)
			}
		}
	}
	bufs.lazyScratchNFA[writeIdx] = next

	if len(next) == 0 {
		return nil
	}

	// If the current state isn't cached, we're already in scratch mode —
	// stay there. Re-using the same buffer is safe because the caller is
	// about to overwrite currentState with our return value.
	if !state.cached {
		return populateScratchState(bufs, writeIdx)
	}

	key := computeKey(next, bufs)
	nextState := ld.lookupOrInsert(key, next, bufs)
	if nextState == nil {
		// Cache full — fall back to scratch for this step.
		ld.stats.scratchFallback.Add(1)
		return populateScratchState(bufs, writeIdx)
	}

	appendTransition(state, b, nextState)
	return nextState
}

// populateScratchState fills bufs.lazyScratchState in place (no allocation)
// with whatever NFA states are currently in bufs.lazyScratchNFA[writeIdx],
// then flips bufs.lazyScratchNFAIdx so the next computeStep writes to the
// other buffer. Used when the cache is full or when we're already in a
// scratch state from a prior step.
func populateScratchState(bufs *nfaBuffers, writeIdx int) *lazyDFAState {
	if bufs.lazySeenFields == nil {
		bufs.lazySeenFields = make(map[*fieldMatcher]bool)
	}
	clear(bufs.lazySeenFields)

	nfaStates := bufs.lazyScratchNFA[writeIdx]
	bufs.lazyScratchState = lazyDFAState{
		nfaStates: nfaStates,
		cached:    false,
	}
	for _, n := range nfaStates {
		for _, ft := range n.fieldTransitions {
			if !bufs.lazySeenFields[ft] {
				bufs.lazySeenFields[ft] = true
				bufs.lazyScratchState.fieldTransitions = append(bufs.lazyScratchState.fieldTransitions, ft)
			}
		}
	}
	bufs.lazyScratchNFAIdx = writeIdx
	return &bufs.lazyScratchState
}

// step is the hot path. Returns the next *lazyDFAState for byte b.
// Lock-free when the transition is already cached on the state.
// Cache hits are counted locally in bufs.lazyLocalHits and flushed to
// ld.stats.hits once per traverseLazyDFA call to avoid shared-atomic
// contention on the hot path.
func (ld *lazyDFA) step(state *lazyDFAState, b byte, bufs *nfaBuffers) *lazyDFAState {
	trans := state.transitions.Load()
	if trans != nil {
		if idx := bytes.IndexByte(trans.keys, b); idx >= 0 {
			bufs.lazyLocalHits++
			return trans.values[idx]
		}
	}
	return ld.computeStep(state, b, bufs)
}

// computeStartKey builds a cache key for the start state of a given NFA table.
// The start state's epsilon closure is always [synthetic_start], where
// synthetic_start is a per-goroutine scratch *faState (nb.startState) that
// carries the table pointer — its own address never changes across calls,
// so we key on the *smallTable pointer instead of the faState pointer.
// The returned slice is only valid until the next computeStartKey or
// computeKey call on the same bufs.
func computeStartKey(table *smallTable, bufs *nfaBuffers) []byte {
	const needed = 8
	if cap(bufs.lazyKeyBuf) < needed {
		bufs.lazyKeyBuf = make([]byte, needed)
	}
	bufs.lazyKeyBuf = bufs.lazyKeyBuf[:needed]
	addr := uintptr(unsafe.Pointer(table))
	bufs.lazyKeyBuf[0] = byte(addr)
	bufs.lazyKeyBuf[1] = byte(addr >> 8)
	bufs.lazyKeyBuf[2] = byte(addr >> 16)
	bufs.lazyKeyBuf[3] = byte(addr >> 24)
	bufs.lazyKeyBuf[4] = byte(addr >> 32)
	bufs.lazyKeyBuf[5] = byte(addr >> 40)
	bufs.lazyKeyBuf[6] = byte(addr >> 48)
	bufs.lazyKeyBuf[7] = byte(addr >> 56)
	return bufs.lazyKeyBuf
}

// traverseLazyDFA mirrors traverseNFA's signature but routes through the
// lazy DFA cache. Returns the collected fieldMatchers reachable by
// consuming val (with a trailing valueTerminator) from table's start state.
func traverseLazyDFA(table *smallTable, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers) []*fieldMatcher {
	// Compute (or look up cached) start state for THIS table.
	//
	// We key on the *smallTable pointer (not bufs.startState, which is a
	// shared synthetic *faState whose address never changes across calls).
	//
	// We also must NOT pass bufs.startState to lookupOrInsert/makeState: if
	// it were stored in the cached lazyDFAState.nfaStates, another goroutine
	// could later call getStartState(differentTable) and mutate
	// bufs.startState.table concurrently with computeStep reading it — a
	// data race. Instead, we allocate a stable, immutable *faState per
	// cache-miss; the allocation is amortized because the result is cached.
	key := computeStartKey(table, bufs)
	currentState := ld.lookupOrInsertStart(key, table, bufs)
	if currentState == nil {
		// Budget too tight to even cache the start state — use scratch.
		startNFA := bufs.getStartState(table)
		startStates := startNFA.epsilonClosure
		writeIdx := 1 - bufs.lazyScratchNFAIdx
		bufs.lazyScratchNFA[writeIdx] = append(bufs.lazyScratchNFA[writeIdx][:0], startStates...)
		currentState = populateScratchState(bufs, writeIdx)
	}

	// Seed fieldSet with any incoming transitions.
	fieldSet := bufs.getFieldSet()
	clear(fieldSet)
	for _, fm := range transitions {
		fieldSet[fm] = true
	}
	// Collect fieldMatchers from the start state.
	for _, fm := range currentState.fieldTransitions {
		fieldSet[fm] = true
	}

	for index := 0; index <= len(val); index++ {
		var utf8Byte byte
		if index < len(val) {
			utf8Byte = val[index]
		} else {
			utf8Byte = valueTerminator
		}
		next := ld.step(currentState, utf8Byte, bufs)
		if next == nil {
			break
		}
		for _, fm := range next.fieldTransitions {
			fieldSet[fm] = true
		}
		currentState = next
	}

	// Flush per-goroutine hit counter to the shared atomic once per call,
	// instead of once per step. This reduces contention on ld.stats.hits
	// by the average bytes-per-match factor (~10-30x).
	if bufs.lazyLocalHits > 0 {
		ld.stats.hits.Add(bufs.lazyLocalHits)
		bufs.lazyLocalHits = 0
	}

	// Materialize into current transmap buffer.
	if len(fieldSet) == 0 {
		return nil
	}
	tm := bufs.getTransmap()
	buf := tm.levels[tm.depth] // already [:0] from caller's push()
	for fm := range fieldSet {
		buf = append(buf, fm)
	}
	tm.levels[tm.depth] = buf
	return buf
}
