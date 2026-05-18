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
