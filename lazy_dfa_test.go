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
