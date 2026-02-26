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
}

// maxLazyDFACacheBytes is the approximate memory budget per lazy DFA cache.
// When exceeded, we stop caching new states (existing cache still works).
// A memory budget naturally adapts: simple-pattern workloads cache more states,
// complex-pattern workloads (with larger NFA state sets) cache fewer.
const maxLazyDFACacheBytes = 4 << 20 // 4 MB

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
	stepResult stepOut
	sortBuf    []*faState // reused by computeKey
	keyBuf     []byte     // reused by computeKey

	// Scratch state and ping-pong NFA buffers for the cache-full path.
	// When the cache is full, we reuse scratchState instead of allocating
	// a new lazyDFAState per byte. scratchNFA provides two buffers that
	// alternate: one holds the current state's nfaStates while the other
	// is used to accumulate the next step's results, avoiding aliasing.
	scratchState  lazyDFAState   // reusable uncached state (value, not pointer)
	scratchNFA    [2][]*faState  // ping-pong NFA state buffers
	scratchNFAIdx int            // which buffer holds current state's nfaStates
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
func (ld *lazyDFA) getOrCreateState(nfaStates []*faState) *lazyDFAState {
	keyBytes := ld.computeKey(nfaStates)

	// Lookup using []byte — the compiler optimizes string(keyBytes) in map
	// index expressions to avoid allocation (see Go spec, composite literals).
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
		nfaState.table.step(b, &ld.stepResult)
		if ld.stepResult.step != nil {
			// Expand epsilon closure of the result
			for _, ecState := range ld.stepResult.step.epsilonClosure {
				if ld.seenStates[ecState] != gen {
					ld.seenStates[ecState] = gen
					nextNFAStates = append(nextNFAStates, ecState)
				}
			}
		}
	}

	// Save the buffer back (append may have grown it).
	ld.scratchNFA[writeIdx] = nextNFAStates

	if len(nextNFAStates) == 0 {
		return nil
	}

	// Only consult the cache when stepping from a cached state.
	// Temporary states arise when the cache is full; further lookups
	// would almost always miss and waste time computing keys.
	if state.cached {
		nextState := ld.getOrCreateState(nextNFAStates)
		if nextState != nil {
			state.transKeys = append(state.transKeys, b)
			state.transValues = append(state.transValues, nextState)
			ld.cacheBytes += 9 // 1 byte key + 8 byte pointer
			return nextState
		}
	}

	return ld.populateScratchState(nextNFAStates, writeIdx)
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

// traverseLazyDFA traverses an NFA using lazy DFA construction.
// On first traversal of a path, it builds and caches DFA states.
// On subsequent traversals, it uses the cached states for near-DFA speed.
// When the cache is full, temporary uncached states are used for cache misses,
// snapping back to the fast path whenever a cached state is hit.
func traverseLazyDFA(table *smallTable, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers) []*fieldMatcher {
	// Get or create the start state (cached for hot path)
	currentState := ld.startState
	if currentState == nil {
		startNFA := bufs.getStartState(table)
		startStates := startNFA.epsilonClosure
		currentState = ld.getOrCreateState(startStates)
		if currentState == nil {
			// Cache full at start — use scratch state to avoid allocation.
			writeIdx := 1 - ld.scratchNFAIdx
			ld.scratchNFA[writeIdx] = append(ld.scratchNFA[writeIdx][:0], startStates...)
			currentState = ld.populateScratchState(ld.scratchNFA[writeIdx], writeIdx)
		} else {
			ld.startState = currentState // cache for next time
		}
	}

	// Collect transitions using the transmap
	newTransitions := bufs.getTransmap()
	newTransitions.push()
	newTransitions.add(transitions)
	newTransitions.add(currentState.fieldTransitions)

	// Process each byte
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

		if len(nextState.fieldTransitions) > 0 {
			newTransitions.add(nextState.fieldTransitions)
		}
		currentState = nextState
	}

	return newTransitions.pop()
}
