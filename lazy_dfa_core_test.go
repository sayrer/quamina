package quamina

import "testing"

func TestLazyDFACoreKeyDedup(t *testing.T) {
	ld := newLazyDFA()
	a, b, c := &faState{}, &faState{}, &faState{}

	// Same set in different order must map to the same cached state.
	s1 := ld.getOrCreateState([]*faState{a, b, c})
	s2 := ld.getOrCreateState([]*faState{c, a, b})
	if s1 != s2 {
		t.Fatalf("same NFA-state set in different order produced different cache states")
	}
	if !s1.cached {
		t.Fatalf("getOrCreateState must mark the state cached")
	}

	// A different set must map to a different state.
	s3 := ld.getOrCreateState([]*faState{a, b})
	if s3 == s1 {
		t.Fatalf("different NFA-state set collided with an existing cache state")
	}

	if got, _, _, _, _ := ld.stats(); got != 2 {
		t.Fatalf("expected 2 cached states, got %d", got)
	}
}
