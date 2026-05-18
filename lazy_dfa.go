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
