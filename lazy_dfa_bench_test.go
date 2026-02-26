//go:build go1.24

package quamina

import (
	"fmt"
	"math/rand"
	"testing"
)

// BenchmarkLazyDFAHotPath exercises the lazy DFA on a workload designed to
// favor cached DFA traversal over NFA traversal:
//
//   - Moderate number of shellstyle patterns (enough active NFA states per byte
//     to make NFA traversal expensive, but small enough DFA state space to stay
//     cached under maxLazyDFACacheBytes).
//   - Many events over the same value space, so the cache is warm after the
//     first pass and subsequent iterations do O(1)-per-byte SIMD lookups.
//
// On main (which uses traverseNFA), each byte step iterates over all active
// NFA states. With N shellstyle patterns, that's ~N states per byte. The lazy
// DFA collapses these into a single cached state with an O(1) transition lookup.
//
// Run on both branches to compare:
//
//	git stash && git checkout main && go test -bench=BenchmarkLazyDFAHotPath -benchmem -count=3 -run='^$'
//	git checkout lazy_dfa && git stash pop && go test -bench=BenchmarkLazyDFAHotPath -benchmem -count=3 -run='^$'
func BenchmarkLazyDFAHotPath(b *testing.B) {
	// 50 shellstyle patterns with wildcards at varied positions.
	// This creates ~50 active NFA states per byte step, but the DFA state
	// space stays well under 3400 because the patterns share structure.
	words := readWWords(b, 50)
	source := rand.NewSource(42)

	patterns := make([]string, 0, len(words))
	for _, word := range words {
		starAt := source.Int63() % int64(len(word))
		starWord := string(word[:starAt]) + "*" + string(word[starAt:])
		pattern := fmt.Sprintf(`{"x": [{"shellstyle": "%s"}]}`, starWord)
		patterns = append(patterns, pattern)
	}

	q, _ := New()
	for i, p := range patterns {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), p); err != nil {
			b.Fatal(err)
		}
	}
	b.Log(matcherStats(q.matcher.(*coreMatcher)))

	// Build a pool of events: the original words plus some that won't match.
	events := make([][]byte, 0, len(words)*2)
	for _, word := range words {
		events = append(events, []byte(fmt.Sprintf(`{"x": "%s"}`, string(word))))
	}
	// Add non-matching events to exercise transitions that lead to dead ends.
	nonWords := []string{"zzzzz", "qqqqq", "xxxxx", "jjjjj", "wwwww"}
	for _, nw := range nonWords {
		events = append(events, []byte(fmt.Sprintf(`{"x": "%s"}`, nw)))
	}

	// Warm the cache with one full pass.
	for _, ev := range events {
		_, _ = q.MatchesForEvent(ev)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		for _, ev := range events {
			_, _ = q.MatchesForEvent(ev)
		}
	}

	// Report events/sec for easy comparison.
	elapsed := b.Elapsed()
	eventsPerSec := float64(b.N) * float64(len(events)) / elapsed.Seconds()
	b.Logf("%.0f events/sec", eventsPerSec)
}
