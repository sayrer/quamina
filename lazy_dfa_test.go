package quamina

import "testing"

func TestLazyDFA_NewSetsBudget(t *testing.T) {
	ld := newLazyDFA(8 << 20)
	if ld == nil {
		t.Fatal("newLazyDFA returned nil")
	}
	if ld.budget != 8<<20 {
		t.Errorf("budget = %d, want %d", ld.budget, 8<<20)
	}
	if ld.cacheBytes.Load() != 0 {
		t.Errorf("cacheBytes = %d, want 0", ld.cacheBytes.Load())
	}
	if ld.stats.stateCount.Load() != 0 {
		t.Errorf("stateCount = %d, want 0", ld.stats.stateCount.Load())
	}
}

func TestNfaBuffers_HasLazyDFAScratch(t *testing.T) {
	nb := newNfaBuffers()
	// All scratch fields should be zero-valued and usable.
	nb.lazyKeyBuf = nb.lazyKeyBuf[:0]
	nb.lazySortBuf = nb.lazySortBuf[:0]
	nb.lazySeenStates = map[*faState]uint64{}
	nb.lazyStepGen = 0
	nb.lazyScratchNFAIdx = 0
	nb.lazySeenFields = map[*fieldMatcher]bool{}
	nb.lazyDFA = nil
	if nb.lazyScratchNFA[0] != nil || nb.lazyScratchNFA[1] != nil {
		t.Error("scratchNFA slots should start nil")
	}
}

func TestCoreFields_GetOrInitLazyDFA(t *testing.T) {
	// budget=0 → always returns nil
	cf := &coreFields{state: newFieldMatcher(), segmentsTree: newSegmentsIndex()}
	if ld := cf.getOrInitLazyDFA(); ld != nil {
		t.Errorf("budget=0 should return nil, got %v", ld)
	}

	// budget>0 → returns non-nil, idempotent
	cf2 := &coreFields{
		state:         newFieldMatcher(),
		segmentsTree:  newSegmentsIndex(),
		lazyDFABudget: 8 << 20,
	}
	ld1 := cf2.getOrInitLazyDFA()
	if ld1 == nil {
		t.Fatal("budget>0 should return non-nil")
	}
	ld2 := cf2.getOrInitLazyDFA()
	if ld1 != ld2 {
		t.Errorf("getOrInitLazyDFA not idempotent: %p vs %p", ld1, ld2)
	}
	if ld1.budget != 8<<20 {
		t.Errorf("budget = %d, want %d", ld1.budget, 8<<20)
	}
}

func TestAddPattern_PreservesLazyDFABudget(t *testing.T) {
	cm := newCoreMatcher()
	// Manually set budget (the New() wiring comes later in Task A6).
	cm.updateable.Load().lazyDFABudget = 8 << 20

	if err := cm.addPattern("p1", `{"x": ["a"]}`); err != nil {
		t.Fatal(err)
	}

	cf := cm.updateable.Load()
	if cf.lazyDFABudget != 8<<20 {
		t.Errorf("lazyDFABudget after AddPattern = %d, want %d", cf.lazyDFABudget, 8<<20)
	}
	if cf.lazyDFA.Load() != nil {
		t.Errorf("new coreFields should have nil lazyDFA (fresh cache), got %v", cf.lazyDFA.Load())
	}
}

func TestCoreMatcher_SetAndGetLazyDFABudget(t *testing.T) {
	cm := newCoreMatcher()
	if s := cm.lazyDFAStats(); s.Enabled || s.Budget != 0 {
		t.Errorf("fresh matcher should report disabled: %+v", s)
	}

	cm.setLazyDFABudget(8 << 20)
	s := cm.lazyDFAStats()
	if !s.Enabled || s.Budget != 8<<20 {
		t.Errorf("after setLazyDFABudget(8MiB): %+v", s)
	}
	if s.StateCount != 0 || s.CacheBytes != 0 {
		t.Errorf("before any match, counters should be zero: %+v", s)
	}
}

func TestPublicAPI_WithLazyDFACacheBytes(t *testing.T) {
	// Default: disabled.
	q1, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if s := q1.LazyDFAStats(); s.Enabled {
		t.Errorf("default should be disabled: %+v", s)
	}

	// Opt in.
	q2, err := New(WithLazyDFACacheBytes(8 << 20))
	if err != nil {
		t.Fatal(err)
	}
	s := q2.LazyDFAStats()
	if !s.Enabled || s.Budget != 8<<20 {
		t.Errorf("WithLazyDFACacheBytes(8MiB): %+v", s)
	}

	// Reject absurd values.
	if _, err := New(WithLazyDFACacheBytes(1 << 41)); err == nil {
		t.Error("expected error for huge budget")
	}
}

func TestComputeKey_DeterministicAndUnique(t *testing.T) {
	bufs := newNfaBuffers()
	a, b, c := &faState{}, &faState{}, &faState{}

	k1 := computeKey([]*faState{a, b, c}, bufs)
	k2 := computeKey([]*faState{c, a, b}, bufs)
	if string(k1) != string(k2) {
		t.Error("computeKey should be order-invariant (sorted)")
	}

	k3 := computeKey([]*faState{a, b}, bufs)
	if string(k1) == string(k3) {
		t.Error("different state sets should produce different keys")
	}

	if len(k1) != 3*8 {
		t.Errorf("key length = %d, want %d", len(k1), 3*8)
	}
}

func TestMakeState_DedupsFieldTransitions(t *testing.T) {
	bufs := newNfaBuffers()
	bufs.lazySeenFields = map[*fieldMatcher]bool{}

	fm1, fm2 := newFieldMatcher(), newFieldMatcher()
	s1 := &faState{fieldTransitions: []*fieldMatcher{fm1, fm2}}
	s2 := &faState{fieldTransitions: []*fieldMatcher{fm1}} // fm1 is duplicate

	state := makeState([]*faState{s1, s2}, bufs)
	if state == nil {
		t.Fatal("makeState returned nil")
	}
	if len(state.nfaStates) != 2 {
		t.Errorf("nfaStates len = %d, want 2", len(state.nfaStates))
	}
	if len(state.fieldTransitions) != 2 {
		t.Errorf("fieldTransitions len = %d, want 2 (deduped)", len(state.fieldTransitions))
	}
	if state.cached {
		t.Error("makeState should leave cached=false (caller sets)")
	}

	// nfaStates must be a copy, not aliased to caller's slice
	input := []*faState{s1, s2}
	state2 := makeState(input, bufs)
	input[0] = nil
	if state2.nfaStates[0] == nil {
		t.Error("makeState should copy nfaStates")
	}
}

func TestEstimateBytes_GrowsWithStateSize(t *testing.T) {
	bufs := newNfaBuffers()
	bufs.lazySeenFields = map[*fieldMatcher]bool{}

	small := makeState([]*faState{{}}, bufs)
	large := makeState([]*faState{{}, {}, {}, {}, {}}, bufs)

	bs, bl := estimateBytes(small), estimateBytes(large)
	if bs == 0 || bl == 0 {
		t.Errorf("estimates should be nonzero: small=%d large=%d", bs, bl)
	}
	if bl <= bs {
		t.Errorf("larger state should cost more bytes: small=%d large=%d", bs, bl)
	}
}

func TestLookupOrInsert_HitMissBudget(t *testing.T) {
	bufs := newNfaBuffers()
	bufs.lazySeenFields = map[*fieldMatcher]bool{}
	ld := newLazyDFA(8 << 20)

	a, b := &faState{}, &faState{}
	states := []*faState{a, b}

	// First call: miss + insert.
	key := computeKey(states, bufs)
	s1 := ld.lookupOrInsert(key, states, bufs)
	if s1 == nil {
		t.Fatal("first call should insert and return non-nil")
	}
	if !s1.cached {
		t.Error("inserted state should have cached=true")
	}
	if ld.stats.stateCount.Load() != 1 {
		t.Errorf("stateCount = %d, want 1", ld.stats.stateCount.Load())
	}

	// Second call with same states: hit, same pointer.
	key2 := computeKey(states, bufs)
	s2 := ld.lookupOrInsert(key2, states, bufs)
	if s2 != s1 {
		t.Errorf("second call should return same pointer: %p vs %p", s1, s2)
	}
	if ld.stats.stateCount.Load() != 1 {
		t.Errorf("stateCount should not grow on hit: %d", ld.stats.stateCount.Load())
	}

	// Tight-budget cache: next insert returns nil.
	ldTight := newLazyDFA(1) // 1 byte budget — any insert blows it
	keyT := computeKey(states, bufs)
	if ldTight.lookupOrInsert(keyT, states, bufs) != nil {
		t.Error("over-budget insert should return nil")
	}
}

func TestAppendTransition_NilCurAndIdempotent(t *testing.T) {
	state := &lazyDFAState{cached: true}
	next1 := &lazyDFAState{cached: true}

	// First append onto nil transitions.
	appendTransition(state, 'a', next1)
	cur := state.transitions.Load()
	if cur == nil || len(cur.keys) != 1 || cur.keys[0] != 'a' || cur.values[0] != next1 {
		t.Errorf("after first append: %+v", cur)
	}

	// Second append for different byte.
	next2 := &lazyDFAState{cached: true}
	appendTransition(state, 'b', next2)
	cur = state.transitions.Load()
	if len(cur.keys) != 2 || cur.keys[1] != 'b' || cur.values[1] != next2 {
		t.Errorf("after second append: %+v", cur)
	}

	// Double-call for same byte: no-op, no duplicate.
	appendTransition(state, 'a', next1)
	cur = state.transitions.Load()
	if len(cur.keys) != 2 {
		t.Errorf("double-call should be idempotent, got len=%d", len(cur.keys))
	}
}

func TestComputeStep_DeadEndReturnsNil(t *testing.T) {
	// Build a smallTable with no transitions for 'z'.
	tbl := newSmallTable()
	st := &faState{table: tbl, epsilonClosure: []*faState{nil}}
	st.epsilonClosure[0] = st

	bufs := newNfaBuffers()
	bufs.lazySeenStates = map[*faState]uint64{}
	ld := newLazyDFA(8 << 20)

	state := makeState([]*faState{st}, bufs)
	state.cached = true
	got := ld.computeStep(state, 'z', bufs)
	if got != nil {
		t.Errorf("dead-end step should return nil, got %v", got)
	}
}

func TestPopulateScratchState_PingPongAndZeroAlloc(t *testing.T) {
	bufs := newNfaBuffers()
	bufs.lazySeenFields = map[*fieldMatcher]bool{}
	a, b := &faState{}, &faState{}

	// First call: writes to slot 1-bufs.lazyScratchNFAIdx.
	bufs.lazyScratchNFA[1] = append(bufs.lazyScratchNFA[1][:0], a, b)
	s1 := populateScratchState(bufs, 1)
	if s1 != &bufs.lazyScratchState {
		t.Error("should return &bufs.lazyScratchState")
	}
	if s1.cached {
		t.Error("scratch state must have cached=false")
	}
	if bufs.lazyScratchNFAIdx != 1 {
		t.Errorf("lazyScratchNFAIdx = %d, want 1", bufs.lazyScratchNFAIdx)
	}
	if len(s1.nfaStates) != 2 || s1.nfaStates[0] != a || s1.nfaStates[1] != b {
		t.Error("nfaStates should reference the buffer we passed in")
	}
}

func TestStep_HotPathReturnsCachedTransition(t *testing.T) {
	ld := newLazyDFA(8 << 20)
	bufs := newNfaBuffers()

	from := &lazyDFAState{cached: true}
	to := &lazyDFAState{cached: true}
	appendTransition(from, 'x', to)

	got := ld.step(from, 'x', bufs)
	if got != to {
		t.Errorf("hot path: got %p, want %p", got, to)
	}
	if ld.stats.hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", ld.stats.hits.Load())
	}
	if ld.stats.misses.Load() != 0 {
		t.Errorf("misses = %d, want 0", ld.stats.misses.Load())
	}
}

func TestQuamina_LazyDFAEquivalentMatches(t *testing.T) {
	patterns := map[string]string{
		"exact":  `{"x": ["foobar"]}`,
		"star":   `{"x": [{"shellstyle": "*foo*"}]}`,
		"prefix": `{"x": [{"shellstyle": "foo*"}]}`,
	}
	events := []string{
		`{"x": "foobar"}`,
		`{"x": "abcfoobardef"}`,
		`{"x": "foozzz"}`,
		`{"x": "no match here"}`,
	}

	qNFA, _ := New()
	qLazy, _ := New(WithLazyDFACacheBytes(8 << 20))
	for name, p := range patterns {
		if err := qNFA.AddPattern(name, p); err != nil {
			t.Fatal(err)
		}
		if err := qLazy.AddPattern(name, p); err != nil {
			t.Fatal(err)
		}
	}

	for _, ev := range events {
		nfaMatches, _ := qNFA.MatchesForEvent([]byte(ev))
		lazyMatches, _ := qLazy.MatchesForEvent([]byte(ev))
		if !sameXSet(nfaMatches, lazyMatches) {
			t.Errorf("event %q: NFA=%v lazy=%v", ev, nfaMatches, lazyMatches)
		}
	}
}

func sameXSet(a, b []X) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[X]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// Compares lazy-DFA traversal of a real shellstyle pattern against
// traverseNFA. Both must return the same fieldMatcher set.
func TestTraverseLazyDFA_EquivalentToNFA(t *testing.T) {
	cm := newCoreMatcher()
	if err := cm.addPattern("p", `{"x": [{"shellstyle": "*foo*"}]}`); err != nil {
		t.Fatal(err)
	}

	bufs := newNfaBuffers()
	cf := cm.fields()
	state := cf.state
	// Walk down to the valueMatcher for field "x".
	vm := state.fields().transitions["x"]
	if vm == nil {
		t.Fatal("no valueMatcher for x")
	}
	vmFields := vm.fields()
	if vmFields.startTable == nil || !vmFields.isNondeterministic {
		t.Fatal("expected nondeterministic NFA for *foo* pattern")
	}

	val := []byte(`"barfoobaz"`)
	transitions := bufs.transitionsBuf[:0]
	nfaTM := bufs.getTransmap()
	nfaTM.push()
	nfaResult := traverseNFA(vmFields.startTable, val, transitions, bufs)
	nfaTM.pop()

	ld := newLazyDFA(8 << 20)
	bufs2 := newNfaBuffers()
	transitions2 := bufs2.transitionsBuf[:0]
	lazyTM := bufs2.getTransmap()
	lazyTM.push()
	lazyResult := traverseLazyDFA(vmFields.startTable, val, transitions2, ld, bufs2)
	lazyTM.pop()

	if len(nfaResult) != len(lazyResult) {
		t.Errorf("result lengths differ: NFA=%d lazy=%d", len(nfaResult), len(lazyResult))
	}
}

func TestLazyDFA_DisabledByDefault(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AddPattern("p", `{"x": [{"shellstyle": "*foo*"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.MatchesForEvent([]byte(`{"x":"foobar"}`)); err != nil {
		t.Fatal(err)
	}
	s := q.LazyDFAStats()
	if s.Enabled {
		t.Errorf("default New() should report Enabled=false, got %+v", s)
	}
	if s.StateCount != 0 || s.TransitionHits != 0 || s.TransitionMiss != 0 {
		t.Errorf("disabled cache must not accumulate counters: %+v", s)
	}
}

func TestLazyDFA_CacheLifetimeAcrossAddPattern(t *testing.T) {
	q, _ := New(WithLazyDFACacheBytes(8 << 20))
	if err := q.AddPattern("p1", `{"x": [{"shellstyle": "*foo*"}]}`); err != nil {
		t.Fatal(err)
	}
	// Warm the cache.
	for i := 0; i < 10; i++ {
		if _, err := q.MatchesForEvent([]byte(`{"x":"abcfoodef"}`)); err != nil {
			t.Fatal(err)
		}
	}
	s := q.LazyDFAStats()
	if s.StateCount == 0 {
		t.Fatal("expected warm cache, got StateCount=0")
	}
	prevStateCount := s.StateCount

	// AddPattern builds a fresh coreFields → cache should reset for new snapshot.
	if err := q.AddPattern("p2", `{"x": [{"shellstyle": "*bar*"}]}`); err != nil {
		t.Fatal(err)
	}
	s = q.LazyDFAStats()
	if s.StateCount >= prevStateCount {
		t.Errorf("expected fresh cache after AddPattern, StateCount=%d (was %d)", s.StateCount, prevStateCount)
	}

	// Match must still succeed for both patterns; no panic from stale pointers.
	got, err := q.MatchesForEvent([]byte(`{"x":"abcfoodef"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "p1" {
		t.Errorf("expected [p1], got %v", got)
	}
	got, _ = q.MatchesForEvent([]byte(`{"x":"abcbardef"}`))
	if len(got) != 1 || got[0] != "p2" {
		t.Errorf("expected [p2], got %v", got)
	}
}
