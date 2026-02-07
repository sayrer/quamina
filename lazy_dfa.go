package quamina

import (
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
	// transitions maps byte values to the next lazyDFAState.
	// nil means not yet computed for that byte.
	transitions [256]*lazyDFAState

	// fieldTransitions are the combined field transitions from all NFA states.
	fieldTransitions []*fieldMatcher

	// nfaStates are the underlying NFA states (after epsilon closure).
	// Used to compute transitions on cache miss.
	nfaStates []*faState
}

// maxLazyDFAStates is the maximum number of cached DFA states.
// If exceeded, we stop caching new states (existing cache still works).
const maxLazyDFAStates = 1000

// lazyDFA is the cache for lazy DFA construction.
// It maps sets of NFA states to their corresponding lazyDFAState.
// This is stored per-goroutine in nfaBuffers, so no synchronization is needed.
type lazyDFA struct {
	cache      map[string]*lazyDFAState // key is sorted pointer addresses
	startState *lazyDFAState            // cached start state (avoids key computation on hot path)
	disabled   bool                     // set when cache limit exceeded

	// Stats for understanding cache behavior (not used in production)
	stateCreates   int // number of states created
	transitionHits int // cache hits on transition lookup
	transitionMiss int // cache misses on transition lookup
}

func newLazyDFA() *lazyDFA {
	return &lazyDFA{
		cache: make(map[string]*lazyDFAState),
	}
}

// stats returns cache statistics for analysis
func (ld *lazyDFA) stats() (stateCount, stateCreates, hits, misses int) {
	return len(ld.cache), ld.stateCreates, ld.transitionHits, ld.transitionMiss
}

// getOrCreateState returns the lazyDFAState for the given set of NFA states.
// Creates a new state if one doesn't exist.
// Returns nil if the cache is disabled (limit exceeded).
// No synchronization needed - this is called from per-goroutine nfaBuffers.
func (ld *lazyDFA) getOrCreateState(nfaStates []*faState) *lazyDFAState {
	if ld.disabled {
		return nil
	}

	key := makeStateSetKey(nfaStates)

	if state, exists := ld.cache[key]; exists {
		return state
	}

	// Check cache limit
	if len(ld.cache) >= maxLazyDFAStates {
		ld.disabled = true
		return nil
	}

	// Create new state
	state := &lazyDFAState{
		nfaStates: nfaStates,
	}
	ld.stateCreates++

	// Collect field transitions from all NFA states
	seen := make(map[*fieldMatcher]bool)
	for _, nfaState := range nfaStates {
		for _, ft := range nfaState.fieldTransitions {
			if !seen[ft] {
				seen[ft] = true
				state.fieldTransitions = append(state.fieldTransitions, ft)
			}
		}
	}

	ld.cache[key] = state
	return state
}

// step returns the next lazyDFAState for the given byte.
// Computes and caches the result if not already cached.
// Returns (nextState, true) on success, (nil, false) if cache limit exceeded.
func (ld *lazyDFA) step(state *lazyDFAState, b byte) (*lazyDFAState, bool) {
	// Fast path: already cached (no lock needed for reading array element)
	if next := state.transitions[b]; next != nil {
		ld.transitionHits++
		return next, true
	}

	// Slow path: compute the next state
	ld.transitionMiss++
	return ld.computeStep(state, b)
}

func (ld *lazyDFA) computeStep(state *lazyDFAState, b byte) (*lazyDFAState, bool) {
	// Compute next NFA states by stepping each current NFA state on byte b
	var nextNFAStates []*faState
	seen := make(map[*faState]bool)
	stepResult := &stepOut{}

	for _, nfaState := range state.nfaStates {
		nfaState.table.step(b, stepResult)
		if stepResult.step != nil {
			// Expand epsilon closure of the result
			for _, ecState := range stepResult.step.epsilonClosure {
				if !seen[ecState] {
					seen[ecState] = true
					nextNFAStates = append(nextNFAStates, ecState)
				}
			}
		}
	}

	if len(nextNFAStates) == 0 {
		return nil, true // no next state, but cache is OK
	}

	// Get or create the lazyDFAState for the next NFA state set
	nextState := ld.getOrCreateState(nextNFAStates)
	if nextState == nil {
		return nil, false // cache limit exceeded
	}

	// Cache the transition (racy write is OK - worst case we recompute)
	state.transitions[b] = nextState

	return nextState, true
}

// makeStateSetKey creates a cache key from a set of NFA states.
// Uses sorted pointer addresses as bytes (same approach as stateLists).
func makeStateSetKey(states []*faState) string {
	if len(states) == 0 {
		return ""
	}

	// Sort by pointer address
	sorted := make([]*faState, len(states))
	copy(sorted, states)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if uintptr(unsafe.Pointer(sorted[i])) > uintptr(unsafe.Pointer(sorted[j])) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Convert to bytes
	keyBytes := make([]byte, 0, len(sorted)*8)
	for _, state := range sorted {
		addr := uintptr(unsafe.Pointer(state))
		for i := 0; i < 8; i++ {
			keyBytes = append(keyBytes, byte(addr>>(i*8)))
		}
	}
	return string(keyBytes)
}

// traverseLazyDFA traverses an NFA using lazy DFA construction.
// On first traversal of a path, it builds and caches DFA states.
// On subsequent traversals, it uses the cached states for near-DFA speed.
// Falls back to NFA traversal if the cache limit is exceeded.
func traverseLazyDFA(table *smallTable, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers, pp printer) []*fieldMatcher {
	// Get or create the start state (cached for hot path)
	currentState := ld.startState
	if currentState == nil {
		startNFA := bufs.getStartState(table)
		startStates := startNFA.epsilonClosure
		currentState = ld.getOrCreateState(startStates)
		if currentState == nil {
			// Cache limit exceeded, fall back to NFA
			return traverseNFA(table, val, transitions, bufs, pp)
		}
		ld.startState = currentState // cache for next time
	}

	// Collect transitions using the transmap
	newTransitions := bufs.getTransmap()
	newTransitions.reset()
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

		nextState, ok := ld.step(currentState, utf8Byte)
		if !ok {
			// Cache limit exceeded, fall back to NFA for remaining bytes
			// Continue from current position using NFA traversal
			return traverseNFA(table, val, transitions, bufs, pp)
		}
		if nextState == nil {
			break
		}

		newTransitions.add(nextState.fieldTransitions)
		currentState = nextState
	}

	return newTransitions.all()
}
