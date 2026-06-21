package quamina

import (
	"bytes"
	"slices"
	"unsafe"
)

// lazyDFA implements lazy (on-demand) DFA construction during NFA traversal.
// Instead of converting the entire NFA to a DFA upfront, we cache DFA states
// as they are discovered during matching. This gives near-DFA speed on hot
// paths while avoiding exponential state explosion.
//
// A lazyDFAState represents a "DFA state" which is really a set of NFA states.
// Transitions are cached: (lazyDFAState, byte) → next lazyDFAState.
//
// The cache is stored per-goroutine in nfaBuffers, so no synchronization is needed.

// lazyDFAState represents a set of NFA states (a DFA state in the subset construction).
type lazyDFAState struct {
	// transKeys and transValues form a sparse transition map.
	// transKeys[i] is a byte value; transValues[i] is the corresponding next state.
	// Lookup uses bytes.IndexByte (SIMD intrinsic on amd64/arm64).
	transKeys   []byte
	transValues []*lazyDFAState

	// fieldTransitions are the combined field transitions from all NFA states.
	fieldTransitions []*fieldMatcher

	// nfaStates are the underlying NFA states (after epsilon closure).
	// Used to compute transitions on cache miss.
	nfaStates []*faState

	// cached is true if this state lives in the lazyDFA cache.
	// Temporary (uncached) states skip cache lookups on transitions
	// to avoid expensive key computation that almost always misses.
	cached bool

	// pendingKeys holds bytes whose transition out of this (cached) state has
	// been computed exactly once. A transition is only promoted into the cache
	// (transKeys/transValues) the second time it is taken, so a high-cardinality
	// event stream doesn't fill the cache with one-shot states it never reuses.
	pendingKeys []byte
}

// maxLazyDFACacheBytes is the approximate memory budget per lazy DFA cache.
// When exceeded, we stop caching new states (existing cache still works).
// A memory budget naturally adapts: simple-pattern workloads cache more states,
// complex-pattern workloads (with larger NFA state sets) cache fewer.
const maxLazyDFACacheBytes = 8 << 20 // 8 MB (matches RE2's default)

// lazyDFA is the cache for lazy DFA construction.
// It maps sets of NFA states to their corresponding lazyDFAState.
// This is stored per-goroutine in nfaBuffers, so no synchronization is needed.
type lazyDFA struct {
	cache      map[string]*lazyDFAState // key is sorted pointer addresses
	cacheBytes int                       // approximate bytes consumed by cached states
	startState *lazyDFAState            // cached start state (avoids key computation on hot path)

	// Scratch space reused across calls to avoid per-call allocations.
	stepGen    uint64                 // generation counter for computeStep dedup
	seenStates map[*faState]uint64   // maps faState to the generation it was last seen (never cleared)
	seenFields map[*fieldMatcher]bool
	sortBuf    []*faState // reused by computeKey
	keyBuf     []byte     // reused by computeKey

	// Scratch state and ping-pong NFA buffers for the cache-full path.
	// When the cache is full, we reuse scratchState instead of allocating
	// a new lazyDFAState per byte. scratchNFA provides two buffers that
	// alternate: one holds the current state's nfaStates while the other
	// is used to accumulate the next step's results, avoiding aliasing.
	scratchState  lazyDFAState    // reusable uncached state (value, not pointer)
	scratchNFA    [2][]*faState   // ping-pong NFA state buffers
	scratchNFAIdx int             // which buffer holds current state's nfaStates
	scratchFT     []*fieldMatcher // reusable fieldTransitions buffer

	// Stats for understanding cache behavior (not used in production)
	stateCreates   int // number of states created
	transitionHits int // cache hits on transition lookup
	transitionMiss int // cache misses on transition lookup
}

func newLazyDFA() *lazyDFA {
	return &lazyDFA{
		cache:      make(map[string]*lazyDFAState),
		seenStates: make(map[*faState]uint64),
		seenFields: make(map[*fieldMatcher]bool),
	}
}

// stats returns cache statistics for analysis
func (ld *lazyDFA) stats() (stateCount, stateCreates, hits, misses, cacheBytes int) {
	return len(ld.cache), ld.stateCreates, ld.transitionHits, ld.transitionMiss, ld.cacheBytes
}

// makeState creates a lazyDFAState for the given NFA states and collects
// their field transitions. The state is not added to the cache.
func (ld *lazyDFA) makeState(nfaStates []*faState) *lazyDFAState {
	// Copy nfaStates since the caller may pass a scratch buffer alias.
	copied := make([]*faState, len(nfaStates))
	copy(copied, nfaStates)
	state := &lazyDFAState{nfaStates: copied}
	ld.stateCreates++
	clear(ld.seenFields)
	for _, nfaState := range nfaStates {
		for _, ft := range nfaState.fieldTransitions {
			if !ld.seenFields[ft] {
				ld.seenFields[ft] = true
				state.fieldTransitions = append(state.fieldTransitions, ft)
			}
		}
	}
	return state
}

// populateScratchState populates the scratch state with the given NFA states
// (which must be in scratchNFA[writeIdx]) and flips the ping-pong index.
// This is the zero-allocation path used when the cache is full.
func (ld *lazyDFA) populateScratchState(nextNFAStates []*faState, writeIdx int) *lazyDFAState {
	ld.stateCreates++

	// Collect unique field transitions into scratchFT (reused slice).
	ld.scratchFT = ld.scratchFT[:0]
	clear(ld.seenFields)
	for _, nfaState := range nextNFAStates {
		for _, ft := range nfaState.fieldTransitions {
			if !ld.seenFields[ft] {
				ld.seenFields[ft] = true
				ld.scratchFT = append(ld.scratchFT, ft)
			}
		}
	}

	// Populate the scratch state (value type, no heap allocation).
	ld.scratchState = lazyDFAState{
		nfaStates:        nextNFAStates,
		fieldTransitions: ld.scratchFT,
	}

	// Flip the ping-pong index so the next step writes to the other buffer.
	ld.scratchNFAIdx = writeIdx

	return &ld.scratchState
}

// getOrCreateState returns the lazyDFAState for the given set of NFA states.
// Creates and caches a new state if one doesn't exist and the cache isn't full.
// Returns nil if the cache is full (caller creates a temporary uncached state).
// No synchronization needed - this is called from per-goroutine nfaBuffers.
//
// INVARIANT: this method must NOT mutate its nfaStates argument or any element
// of scratchNFA — it only reads them (copying via computeKey/makeState). Callers
// may safely pass a scratchNFA slice as the argument.
func (ld *lazyDFA) getOrCreateState(nfaStates []*faState) *lazyDFAState {
	keyBytes := ld.computeKey(nfaStates)

	// Lookup using []byte — the Go compiler optimizes string(keyBytes) in map
	// index expressions to avoid the allocation.
	if state, exists := ld.cache[string(keyBytes)]; exists {
		return state
	}

	// Cache full — return nil so caller creates a temporary state
	if ld.cacheBytes >= maxLazyDFACacheBytes {
		return nil
	}

	// Cache miss — allocate the string key for map storage.
	key := string(keyBytes)
	state := ld.makeState(nfaStates)
	state.cached = true
	ld.cache[key] = state

	// Approximate memory cost of this cached state:
	//   map key string:              len(nfaStates) * 8
	//   lazyDFAState struct + slices: 96 bytes
	//   nfaStates backing array:     len(nfaStates) * 8
	//   fieldTransitions backing:    len(fieldTransitions) * 8
	//   Go map entry overhead:       ~64 bytes
	ld.cacheBytes += 96 + 64 + len(nfaStates)*16 + len(state.fieldTransitions)*8

	return state
}

// step returns the next lazyDFAState for the given byte.
// Computes and caches the result if not already cached.
// Returns nil if no transition exists for the byte.
func (ld *lazyDFA) step(state *lazyDFAState, b byte) *lazyDFAState {
	// Fast path: already cached (bytes.IndexByte is a SIMD intrinsic on amd64/arm64)
	if idx := bytes.IndexByte(state.transKeys, b); idx >= 0 {
		ld.transitionHits++
		return state.transValues[idx]
	}

	// Slow path: compute the next state
	ld.transitionMiss++
	return ld.computeStep(state, b)
}

func (ld *lazyDFA) computeStep(state *lazyDFAState, b byte) *lazyDFAState {
	// Use the ping-pong buffer that is NOT holding the current state's nfaStates.
	writeIdx := 1 - ld.scratchNFAIdx
	nextNFAStates := ld.scratchNFA[writeIdx][:0]
	ld.stepGen++
	gen := ld.stepGen

	for _, nfaState := range state.nfaStates {
		nextStep := nfaState.table.step(b)
		if nextStep == nil {
			continue
		}
		// Expand the epsilon closure of the result. An empty closure is the
		// self-only sentinel (main's encoding): the closure is {nextStep}.
		if len(nextStep.epsilonClosure) == 0 {
			if ld.seenStates[nextStep] != gen {
				ld.seenStates[nextStep] = gen
				nextNFAStates = append(nextNFAStates, nextStep)
			}
			continue
		}
		for _, ecState := range nextStep.epsilonClosure {
			if ld.seenStates[ecState] != gen {
				ld.seenStates[ecState] = gen
				nextNFAStates = append(nextNFAStates, ecState)
			}
		}
	}

	// Save the buffer back (append may have grown it).
	ld.scratchNFA[writeIdx] = nextNFAStates

	if len(nextNFAStates) == 0 {
		return nil
	}

	if state.cached {
		// Cache a transition only the second time it is taken. The next
		// state-set for (state, b) is deterministic, so a repeated (state, b)
		// always yields the same result — safe to promote on the second sight.
		if bytes.IndexByte(state.pendingKeys, b) < 0 {
			// First sight: remember the byte, return a transient scratch state
			// (no cached-state allocation). The pending byte is tiny and
			// transient, so it isn't charged against the cache budget.
			state.pendingKeys = append(state.pendingKeys, b)
			return ld.populateScratchState(nextNFAStates, writeIdx)
		}
		// Second sight: promote into the cache.
		nextState := ld.getOrCreateState(nextNFAStates)
		if nextState != nil {
			state.transKeys = append(state.transKeys, b)
			state.transValues = append(state.transValues, nextState)
			if i := bytes.IndexByte(state.pendingKeys, b); i >= 0 {
				last := len(state.pendingKeys) - 1
				state.pendingKeys[i] = state.pendingKeys[last]
				state.pendingKeys = state.pendingKeys[:last]
			}
			ld.cacheBytes += 9 // 1 byte key + 8 byte pointer
			return nextState
		}
	}

	return ld.populateScratchState(nextNFAStates, writeIdx)
}

// traverseLazyDFA traverses an NFA from start using lazy DFA construction,
// materializing and caching DFA states on demand. Match semantics are identical
// to traverseNFA; only the state representation differs. Follows main's transmap
// contract: the caller (tryToMatch) has already push()ed a transmap level, and
// dedup happens via bufs.fieldSet — this function never push()/pop()s.
func traverseLazyDFA(start *faState, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers) []*fieldMatcher {
	// Get or create the start lazyDFAState (cached on the lazyDFA for the hot path).
	currentState := ld.startState
	if currentState == nil {
		startStates := start.epsilonClosure
		if len(startStates) == 0 {
			// self-only sentinel: the start closure is {start}.
			writeIdx := 1 - ld.scratchNFAIdx
			ld.scratchNFA[writeIdx] = append(ld.scratchNFA[writeIdx][:0], start)
			startStates = ld.scratchNFA[writeIdx]
		}
		currentState = ld.getOrCreateState(startStates)
		if currentState == nil {
			// Cache full at start — use a scratch state to avoid allocation.
			// When startStates aliases scratchNFA[writeIdx] (sentinel path above),
			// the append below is a safe no-op: getOrCreateState does not mutate
			// scratchNFA or its slice argument, and scratchNFAIdx is unchanged so
			// writeIdx resolves to the same buffer index in both computations.
			writeIdx := 1 - ld.scratchNFAIdx
			ld.scratchNFA[writeIdx] = append(ld.scratchNFA[writeIdx][:0], startStates...)
			currentState = ld.populateScratchState(ld.scratchNFA[writeIdx], writeIdx)
		} else {
			ld.startState = currentState // cache for next time
		}
	}

	// Dedup transitions via the flat fieldSet, matching traverseNFA.
	fieldSet := bufs.getFieldSet()
	clear(fieldSet)
	for _, fm := range transitions {
		fieldSet[fm] = true
	}
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
		nextState := ld.step(currentState, utf8Byte)
		if nextState == nil {
			break
		}
		for _, fm := range nextState.fieldTransitions {
			fieldSet[fm] = true
		}
		currentState = nextState
	}
	// No post-loop scan needed: each nextState.fieldTransitions is folded into
	// fieldSet inside the loop before currentState advances, so nothing is missed.

	if len(fieldSet) == 0 {
		return nil
	}
	tm := bufs.getTransmap()
	buf := tm.levels[tm.depth] // already [:0] from the caller's push()
	for fm := range fieldSet {
		buf = append(buf, fm)
	}
	tm.levels[tm.depth] = buf
	return buf
}

// computeKey builds a cache key from a set of NFA states into ld.keyBuf,
// reusing scratch buffers to avoid allocation. The returned slice is only
// valid until the next computeKey call.
func (ld *lazyDFA) computeKey(states []*faState) []byte {
	if len(states) == 0 {
		return ld.keyBuf[:0]
	}

	// Reuse sortBuf for the sorted copy
	if cap(ld.sortBuf) < len(states) {
		ld.sortBuf = make([]*faState, len(states))
	}
	ld.sortBuf = ld.sortBuf[:len(states)]
	copy(ld.sortBuf, states)
	slices.SortFunc(ld.sortBuf, func(a, b *faState) int {
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

	// Encode sorted pointer addresses into keyBuf
	needed := len(states) * 8
	if cap(ld.keyBuf) < needed {
		ld.keyBuf = make([]byte, needed)
	}
	ld.keyBuf = ld.keyBuf[:needed]
	for i, state := range ld.sortBuf {
		addr := uintptr(unsafe.Pointer(state))
		off := i * 8
		ld.keyBuf[off] = byte(addr)
		ld.keyBuf[off+1] = byte(addr >> 8)
		ld.keyBuf[off+2] = byte(addr >> 16)
		ld.keyBuf[off+3] = byte(addr >> 24)
		ld.keyBuf[off+4] = byte(addr >> 32)
		ld.keyBuf[off+5] = byte(addr >> 40)
		ld.keyBuf[off+6] = byte(addr >> 48)
		ld.keyBuf[off+7] = byte(addr >> 56)
	}
	return ld.keyBuf
}
