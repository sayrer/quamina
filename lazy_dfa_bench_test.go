package quamina

import (
	"fmt"
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

// BenchmarkLazyDFA_ManyOverlappingWildcards — the classic textbook case.
// Pattern count scales; lazy DFA's hot set saturates ~30-60 states.
func BenchmarkLazyDFA_ManyOverlappingWildcards(b *testing.B) {
	for _, n := range []int{8, 16, 32, 64, 128} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			runBothModes(b,
				func(opts ...Option) *Quamina {
					q, _ := New(opts...)
					for i := 0; i < n; i++ {
						a := byte('a' + (i % 13))
						c := byte('a' + ((i + 1) % 13))
						d := byte('a' + ((i + 2) % 13))
						p := fmt.Sprintf(`{"x": [{"shellstyle": "*%c*%c*%c*"}]}`, a, c, d)
						_ = q.AddPattern(fmt.Sprintf("p%d", i), p)
					}
					return q
				},
				func(b *testing.B, q *Quamina) {
					ev := []byte(`{"x":"abcdefghijklm"}`)
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
		})
	}
}

// BenchmarkLazyDFA_RegexAlternation — regex with dense epsilons.
func BenchmarkLazyDFA_RegexAlternation(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			keywords := []string{"foo", "bar", "baz", "quux", "xyzzy"}
			for i := 0; i < 20; i++ {
				kw := keywords[i%len(keywords)]
				p := fmt.Sprintf(`{"x": [{"regex": "(%s|alt%d)\\d+"}]}`, kw, i)
				_ = q.AddPattern(fmt.Sprintf("p%d", i), p)
			}
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"foo42"}`)
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

// BenchmarkLazyDFA_LiteralInRegex — long literal substring in a regex.
func BenchmarkLazyDFA_LiteralInRegex(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			_ = q.AddPattern("p", `{"x": [{"regex": ".*ERROR.*\\d+.*"}]}`)
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"2026-05-17T10:00:00 ERROR request_id=42 connection refused"}`)
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
