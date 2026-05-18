package quamina

import (
	"sync"
	"sync/atomic"
)

// lazyDFA is a shared, lock-free-on-hit cache of DFA states materialised
// on demand during NFA traversal. Its lifetime equals the lifetime of the
// coreFields snapshot it hangs off, so AddPattern's CoW swap invalidates
// the cache for free.
type lazyDFA struct {
	cache      sync.Map // stateKey (string) → *lazyDFAState
	cacheBytes atomic.Uint64
	budget     uint64 // immutable, set at construction
	startState atomic.Pointer[lazyDFAState]
	insertMu   sync.Mutex // held only during cache-miss insert
	stats      lazyDFAStats
}

type lazyDFAStats struct {
	hits            atomic.Uint64
	misses          atomic.Uint64
	scratchFallback atomic.Uint64
	stateCount      atomic.Uint64
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
