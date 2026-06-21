//go:build go1.24

package quamina

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The lazy DFA cache is bounded by the working set of distinct state-sets a
// stream traverses, not by value cardinality. When that working set fits the 8MB
// budget the cache simply fills and serves everything. When it EXCEEDS the budget
// AND the stream rolls (a sliding window of hot values, old ones retired) the
// cache must evict, or it freezes full of stale state-sets and degrades to NFA
// speed. These tests/benchmarks exercise that regime with a giant wildcard rule
// set over a rolling IPv6/timestamp event stream.

// subseqRule turns a short hex signature into a multi-wildcard rule on a field,
// e.g. "dead" -> shellstyle "*d*e*a*d*". A set of these has an eager-infeasible
// DFA, so each distinct value materializes fresh lazy-DFA state-sets.
func subseqRule(field, sig string) string {
	return fmt.Sprintf(`{%q: [ {"shellstyle": "*%s*"} ] }`, field, strings.Join(strings.Split(sig, ""), "*"))
}

// buildTelemetryMatcher builds a matcher whose lazy-DFA working set exceeds the
// cache budget (130 distinct wildcard rules on "src").
func buildTelemetryMatcher(tb testing.TB) *Quamina { return buildTelemetryMatcherN(tb, 130) }

// buildTelemetryMatcherN builds a matcher with n distinct wildcard rules on
// "src". At n=130 the lazy-DFA working set exceeds the 8MB budget (eviction
// engages); at small n it fits (eviction never fires).
func buildTelemetryMatcherN(tb testing.TB, n int) *Quamina {
	q, _ := New()
	for i := 0; i < n; i++ {
		sig := fmt.Sprintf("%04x", (i*2654435761)&0xffff)
		if err := q.AddPattern(fmt.Sprintf("p%d", i), subseqRule("src", sig)); err != nil {
			tb.Fatal(err)
		}
	}
	return q
}

// rollingSnapshot reads cumulative eviction and hit/miss counters across the
// per-goroutine lazy DFA caches.
func rollingSnapshot(q *Quamina) (evictions, hits, misses int) {
	for _, ld := range q.bufs.lazyDFACaches {
		evictions += ld.evictions
	}
	_, _, _, h, m, _ := q.bufs.lazyDFAStats()
	return evictions, h, m
}

// telemetryEvent is the d-th distinct {src: ipv6, ts: timestamp} event.
func telemetryEvent(d int) []byte {
	ip := fmt.Sprintf("2001:db8:%04x:%04x:%04x:%04x:%04x:%04x",
		d&0xffff, (d*7)&0xffff, (d*13)&0xffff, (d*17)&0xffff, (d*19)&0xffff, (d*23)&0xffff)
	ts := fmt.Sprintf("2026-06-21T%02d:%02d:%02d.%06d", (d/3600)%24, (d/60)%60, d%60, (d*991)%1000000)
	return []byte(fmt.Sprintf(`{"src": %q, "ts": %q}`, ip, ts))
}

func totalEvictions(q *Quamina) (ev, cacheBytes int) {
	for _, ld := range q.bufs.lazyDFACaches {
		ev += ld.evictions
	}
	_, _, _, _, _, cb := q.bufs.lazyDFAStats() // bytes, not KB
	return ev, cb
}

func matchKeys(xs []X) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprintf("%v", x)
	}
	sort.Strings(s)
	return fmt.Sprint(s)
}

// TestLazyDFAEvictionCorrect drives a matcher through a long rolling stream that
// forces many CLOCK evictions, then verifies it returns EXACTLY the matches a
// fresh matcher does — i.e. eviction's edge-cutting left no dangling transitions.
func TestLazyDFAEvictionCorrect(t *testing.T) {
	churned := buildTelemetryMatcher(t)
	fresh := buildTelemetryMatcher(t)
	for d := 0; d < 80000; d++ {
		churned.MatchesForEvent(telemetryEvent(d))
	}
	for _, d := range []int{0, 1, 7, 99, 1000, 4321, 50000, 79999, 123456, 999999} {
		ev := telemetryEvent(d)
		mc, err := churned.MatchesForEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		mf, err := fresh.MatchesForEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		if matchKeys(mc) != matchKeys(mf) {
			t.Fatalf("event %d: churned %v != fresh %v", d, matchKeys(mc), matchKeys(mf))
		}
	}
}

// TestLazyDFAEvictionBounded confirms a long rolling stream keeps the cache
// within (approximately) the budget by evicting, rather than growing unbounded
// or freezing.
func TestLazyDFAEvictionBounded(t *testing.T) {
	q := buildTelemetryMatcher(t)
	for d := 0; d < 100000; d++ {
		q.MatchesForEvent(telemetryEvent(d))
	}
	ev, cb := totalEvictions(q)
	if ev == 0 {
		t.Fatal("expected the rolling stream to trigger evictions")
	}
	if cb > 3*maxLazyDFACacheBytes/2 { // allow slack for approximate accounting
		t.Fatalf("cache not bounded: %d bytes (budget %d)", cb, maxLazyDFACacheBytes)
	}
	t.Logf("100k rolling events: %d KB cached (budget %d KB), %d evictions", cb/1024, maxLazyDFACacheBytes/1024, ev)
}

// BenchmarkLazyDFARolling feeds a sliding window over a value space far larger
// than the cache, with locality (each value recurs a few times before the window
// slides). It reports throughput plus the eviction rate and cache hit rate, in
// two regimes:
//
//   - fits_budget: few rules — the working set fits, so the cache never evicts
//     (0 evict/op, ~full hit rate); eviction is inert here.
//   - over_budget: many rules — the working set overflows, so CLOCK-LRU keeps the
//     live window cached while reclaiming the retired one (evict/op > 0).
//
// To compare against the old freeze-at-budget behavior, benchstat this against a
// build before the CLOCK commit (the eviction policy isn't a runtime toggle).
func BenchmarkLazyDFARolling(b *testing.B) {
	for _, tc := range []struct {
		name     string
		patterns int
	}{
		{"fits_budget", 8},
		{"over_budget", 130},
	} {
		b.Run(tc.name, func(b *testing.B) {
			q := buildTelemetryMatcherN(b, tc.patterns)
			const W, dwell = 256, 4 * 256
			for d := 0; d < 60000; d++ { // pre-saturate the cache
				q.MatchesForEvent(telemetryEvent(d))
			}
			ev0, h0, m0 := rollingSnapshot(q)
			b.ResetTimer()
			b.ReportAllocs()
			n := 0
			for b.Loop() {
				cursor := n / dwell
				q.MatchesForEvent(telemetryEvent(cursor + (n % W)))
				n++
			}
			b.StopTimer()
			ev1, h1, m1 := rollingSnapshot(q)
			if n > 0 {
				b.ReportMetric(float64(ev1-ev0)/float64(n), "evict/op")
			}
			if d := (h1 - h0) + (m1 - m0); d > 0 {
				b.ReportMetric(100*float64(h1-h0)/float64(d), "hit%")
			}
		})
	}
}
