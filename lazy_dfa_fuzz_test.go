package quamina

import "testing"

// FuzzLazyDFAEquivalence drives a (pattern, event) pair through both an
// NFA-only matcher and a lazy-DFA-enabled matcher. Result sets must match.
// Patterns are constrained to shellstyle so the fuzzer doesn't waste
// cycles on obviously-broken JSON.
func FuzzLazyDFAEquivalence(f *testing.F) {
	f.Add("foo", "foobar")
	f.Add("*x*", "abcxdef")
	f.Add("a*b*c", "azzbqqc")
	f.Fuzz(func(t *testing.T, patVal string, eventVal string) {
		if len(patVal) == 0 || len(patVal) > 32 || len(eventVal) > 64 {
			t.Skip()
		}
		// Sanitize: only printable ASCII to avoid JSON-encoding issues.
		for _, r := range patVal + eventVal {
			if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
				t.Skip()
			}
		}

		pattern := `{"x": [{"shellstyle": "` + patVal + `"}]}`
		event := `{"x": "` + eventVal + `"}`

		qNFA, err := New()
		if err != nil {
			t.Fatal(err)
		}
		qLazy, err := New(WithLazyDFACacheBytes(8 << 20))
		if err != nil {
			t.Fatal(err)
		}
		if err := qNFA.AddPattern("p", pattern); err != nil {
			t.Skip() // malformed input; not our problem
		}
		if err := qLazy.AddPattern("p", pattern); err != nil {
			t.Skip()
		}

		nfaMatches, err := qNFA.MatchesForEvent([]byte(event))
		if err != nil {
			t.Skip()
		}
		lazyMatches, err := qLazy.MatchesForEvent([]byte(event))
		if err != nil {
			t.Skip()
		}
		if !sameXSet(nfaMatches, lazyMatches) {
			t.Errorf("pattern=%q event=%q: NFA=%v lazy=%v", pattern, event, nfaMatches, lazyMatches)
		}
	})
}
