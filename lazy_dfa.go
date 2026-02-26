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

// maxLazyDFAStates is the maximum number of cached DFA states.
// If exceeded, we stop caching new states (existing cache still works).
const maxLazyDFAStates = 3400

// lazyDFA is the cache for lazy DFA construction.
// It maps sets of NFA states to their corresponding lazyDFAState.
// This is stored per-goroutine in nfaBuffers, so no synchronization is needed.
type lazyDFA struct {
	cache      map[string]*lazyDFAState // key is sorted pointer addresses
	startState *lazyDFAState            // cached start state (avoids key computation on hot path)

	// Scratch space reused across calls to avoid per-call allocations.
	seenStates map[*faState]bool
	seenFields map[*fieldMatcher]bool
	stepResult stepOut
	sortBuf    []*faState // reused by computeKey
	keyBuf     []byte     // reused by computeKey

	// Stats for understanding cache behavior (not used in production)
	stateCreates   int // number of states created
	transitionHits int // cache hits on transition lookup
	transitionMiss int // cache misses on transition lookup
}

func newLazyDFA() *lazyDFA {
	return &lazyDFA{
		cache:      make(map[string]*lazyDFAState),
		seenStates: make(map[*faState]bool),
		seenFields: make(map[*fieldMatcher]bool),
	}
}

// stats returns cache statistics for analysis
func (ld *lazyDFA) stats() (stateCount, stateCreates, hits, misses int) {
	return len(ld.cache), ld.stateCreates, ld.transitionHits, ld.transitionMiss
}

// makeState creates a lazyDFAState for the given NFA states and collects
// their field transitions. The state is not added to the cache.
func (ld *lazyDFA) makeState(nfaStates []*faState) *lazyDFAState {
	state := &lazyDFAState{nfaStates: nfaStates}
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
	if len(ld.cache) >= maxLazyDFAStates {
		return nil
	}

	// Cache miss — allocate the string key for map storage.
	key := string(keyBytes)
	state := ld.makeState(nfaStates)
	state.cached = true
	ld.cache[key] = state
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
	// Compute next NFA states by stepping each current NFA state on byte b
	var nextNFAStates []*faState
	clear(ld.seenStates)

	for _, nfaState := range state.nfaStates {
		nfaState.table.step(b, &ld.stepResult)
		if ld.stepResult.step != nil {
			// Expand epsilon closure of the result
			for _, ecState := range ld.stepResult.step.epsilonClosure {
				if !ld.seenStates[ecState] {
					ld.seenStates[ecState] = true
					nextNFAStates = append(nextNFAStates, ecState)
				}
			}
		}
	}

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
			return nextState
		}
	}

	return ld.makeState(nextNFAStates)
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
			// Cache full at start — create temporary start state
			currentState = ld.makeState(startStates)
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
