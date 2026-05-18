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
