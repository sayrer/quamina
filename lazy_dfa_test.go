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
