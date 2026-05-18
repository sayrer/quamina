package quamina

import (
	"testing"
)

// runBothModes runs fn under both NFA-only and lazy-DFA-enabled matchers
// as subbenchmarks. Reports cache hit rate and cacheBytes via ReportMetric.
func runBothModes(b *testing.B, build func(opts ...Option) *Quamina, fn func(b *testing.B, q *Quamina)) {
	b.Helper()
	for _, mode := range []struct {
		name string
		opt  Option
	}{
		{"NFAOnly", WithLazyDFACacheBytes(0)},
		{"LazyDFA", WithLazyDFACacheBytes(8 << 20)},
	} {
		b.Run(mode.name, func(b *testing.B) {
			q := build(mode.opt)
			fn(b, q)
			s := q.LazyDFAStats()
			if s.Enabled {
				total := float64(s.TransitionHits + s.TransitionMiss)
				if total > 0 {
					b.ReportMetric(float64(s.TransitionHits)/total, "hit_ratio")
				}
				b.ReportMetric(float64(s.CacheBytes), "cache_bytes")
			}
		})
	}
}

// BenchmarkLazyDFA_ExactString — sanity: 1 exact pattern, uniform event.
// Lazy DFA should not regress more than 2% vs NFA-only.
func BenchmarkLazyDFA_ExactString(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			_ = q.AddPattern("p", `{"x": ["foobar"]}`)
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"foobar"}`)
			// Warm-up so the cache is hot before measuring.
			for i := 0; i < 100; i++ {
				_, _ = q.MatchesForEvent(ev)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(ev)
			}
		},
	)
}

// BenchmarkLazyDFA_SingleShellstyle — sanity: 1 wildcard pattern,
// uniform matching event. Prototype showed ~8x speedup.
func BenchmarkLazyDFA_SingleShellstyle(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			_ = q.AddPattern("p", `{"x": [{"shellstyle": "*foo*"}]}`)
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"abcdefoobarghi"}`)
			for i := 0; i < 100; i++ {
				_, _ = q.MatchesForEvent(ev)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(ev)
			}
		},
	)
}
