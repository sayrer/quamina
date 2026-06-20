//go:build go1.24

package quamina

import (
	"fmt"
	"strings"
	"testing"
)

// This file demonstrates the lazy DFA's core value proposition (cf. issue #437,
// where both Patterns and Events are untrusted): a GIANT pattern set whose full
// (eager) DFA would explode combinatorially can still be matched at near-DFA
// speed, as long as the Event stream is repetitive — because the lazy DFA only
// ever materializes, and then caches, the handful of NFA-state-sets that the
// recurring events actually traverse.
//
// To force eager-DFA explosion we turn each 5-letter word into a subsequence
// rule: "aback" -> shellstyle "*a*b*a*c*k*", which matches any value containing
// those letters in order. Merged over hundreds of words, the determinized state
// (a vector of per-pattern subsequence progress) is exponentially large, so the
// eager DFA is infeasible to build — but the lazy cache stays tiny.

// subsequencePattern turns a word into a multi-wildcard shellstyle rule on "msg".
func subsequencePattern(word string) string {
	return `{"msg": [ {"shellstyle": "*` + strings.Join(strings.Split(word, ""), "*") + `*"} ] }`
}

// buildGiantMatcher builds a Quamina with n subsequence rules and returns it
// plus the start state of the merged "msg" value automaton.
func buildGiantMatcher(tb testing.TB, n int) (*Quamina, *faState) {
	tb.Helper()
	words := readWWords(tb, n)
	q, _ := New()
	for i, w := range words {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), subsequencePattern(string(w))); err != nil {
			tb.Fatal(err)
		}
	}
	start := q.matcher.(*coreMatcher).fields().state.fields().transitions["msg"].fields().start
	if start == nil {
		tb.Fatal("expected a nondeterministic value automaton for msg")
	}
	return q, start
}

// recurringEvents returns k JSON events whose values are words drawn from the
// first n of the list — i.e. the repetitive, low-cardinality stream that a
// lazy DFA caches. Each value matches at least its own subsequence rule.
func recurringEvents(tb testing.TB, n, k int) [][]byte {
	words := readWWords(tb, n)
	out := make([][]byte, k)
	for i := 0; i < k; i++ {
		out[i] = []byte(fmt.Sprintf(`{"msg": %q}`, string(words[i*n/k])))
	}
	return out
}

// TestEagerInfeasibleLazyTractable shows that the full DFA is infeasible to
// build for this pattern set, while the lazy DFA handles a repetitive event
// stream with a tiny cache and a high hit rate.
func TestEagerInfeasibleLazyTractable(t *testing.T) {
	q, start := buildGiantMatcher(t, 400)

	// Eager (full) DFA: subset construction explodes, so quamina abandons it
	// (dfaStart stays nil) and uses the lazy DFA. We assert against quamina's
	// production eager budget (fast); cranking the budget up confirms the true
	// determinized size is enormous — measured to exceed 50,000 states for this
	// 400-pattern set before bailing (each extra multi-wildcard pattern multiplies
	// the per-pattern subsequence-progress state, i.e. exponential blow-up).
	if dfa := nfa2DfaWithBudget(start, maxEagerDFAStates); dfa != nil {
		t.Fatalf("expected the eager DFA to exceed the %d-state budget (infeasible), but it converted", maxEagerDFAStates)
	}
	t.Logf("eager DFA: exceeds quamina's %d-state budget -> abandoned (measured: >50,000 states)", maxEagerDFAStates)

	// Lazy DFA: replay a small recurring event pool many times.
	const reps = 200
	pool := recurringEvents(t, 400, 4)
	var matched int
	for r := 0; r < reps; r++ {
		for _, ev := range pool {
			m, err := q.MatchesForEvent(ev)
			if err != nil {
				t.Fatal(err)
			}
			matched += len(m)
		}
	}
	if matched == 0 {
		t.Fatal("expected the recurring words to match their own subsequence rules")
	}

	_, states, _, hits, misses, cacheBytes := q.bufs.lazyDFAStats()
	rate := 100 * float64(hits) / float64(hits+misses)
	t.Logf("lazy DFA after %d events: %d cached states, %.1f%% hit rate, %d KB",
		reps*len(pool), states, rate, cacheBytes/1024)

	if states >= maxEagerDFAStates {
		t.Fatalf("lazy cache should be far smaller than even the eager budget, got %d states", states)
	}
	if rate < 90 {
		t.Fatalf("expected a high cache hit rate for repetitive events, got %.1f%%", rate)
	}
}

// BenchmarkGiantPatternsRepetitiveEvents matches a repetitive event stream
// against the giant (eager-infeasible) pattern set two ways:
//
//   - warm:  the lazy DFA cache persists across events, so the recurring values
//            hit cached transitions — near-DFA throughput.
//   - cold:  the cache is cleared before every event, so each one re-materializes
//            its state-sets from the NFA — the cost you'd pay WITHOUT the cache.
//
// The eager DFA is not benchmarked because it is infeasible to build (see
// TestEagerInfeasibleLazyTractable). Both sub-benchmarks run the full, real
// MatchesForEvent path so the only difference is lazy-cache reuse.
func BenchmarkGiantPatternsRepetitiveEvents(b *testing.B) {
	q, _ := buildGiantMatcher(b, 400)
	pool := recurringEvents(b, 400, 4)

	b.Run("warm_repetitive_cached", func(b *testing.B) {
		for _, ev := range pool { // warm the cache
			if _, err := q.MatchesForEvent(ev); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		i := 0
		for b.Loop() {
			if _, err := q.MatchesForEvent(pool[i%len(pool)]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})

	b.Run("cold_no_cache_reuse", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		i := 0
		for b.Loop() {
			clear(q.bufs.lazyDFACaches) // force a cold re-materialization each event
			if _, err := q.MatchesForEvent(pool[i%len(pool)]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}
