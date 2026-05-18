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

// BenchmarkLazyDFA_QuantifiedCharClass — eager nfa2Dfa territory.
func BenchmarkLazyDFA_QuantifiedCharClass(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			for i := 0; i < 5; i++ {
				p := fmt.Sprintf(`{"x": [{"regex": "[a-z]{8,16}sfx%d"}]}`, i)
				_ = q.AddPattern(fmt.Sprintf("p%d", i), p)
			}
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"abcdefghijksfx3"}`)
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

// BenchmarkLazyDFA_ManyAnchoredRegex — 200 anchored regex patterns.
// Cache size scales with event diversity, not pattern count.
func BenchmarkLazyDFA_ManyAnchoredRegex(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			for i := 0; i < 200; i++ {
				p := fmt.Sprintf(`{"x": [{"regex": "PFX[0-9]+SFX%d"}]}`, i)
				_ = q.AddPattern(fmt.Sprintf("p%d", i), p)
			}
			return q
		},
		func(b *testing.B, q *Quamina) {
			events := [][]byte{
				[]byte(`{"x":"PFX42SFX17"}`),
				[]byte(`{"x":"PFX99SFX42"}`),
				[]byte(`{"x":"PFX1SFX199"}`),
				[]byte(`{"x":"PFX9999SFX0"}`),
			}
			for i := 0; i < 100; i++ {
				_, _ = q.MatchesForEvent(events[i%len(events)])
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(events[i%len(events)])
			}
		},
	)
}

// BenchmarkLazyDFA_DeepEpsilonNest — maximum epsilon closure depth.
func BenchmarkLazyDFA_DeepEpsilonNest(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			_ = q.AddPattern("p", `{"x": [{"regex": "((a|b|c)*(d|e|f)*)+"}]}`)
			return q
		},
		func(b *testing.B, q *Quamina) {
			ev := []byte(`{"x":"abcdefabcdefabcdef"}`)
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

// BenchmarkLazyDFA_CacheThrashing — pattern admits huge state space, inputs
// engineered to visit uncached (state, byte) pairs constantly. LazyDFA must
// not regress more than ~10% vs NFAOnly here.
func BenchmarkLazyDFA_CacheThrashing(b *testing.B) {
	runBothModes(b,
		func(opts ...Option) *Quamina {
			q, _ := New(opts...)
			_ = q.AddPattern("p", `{"x": [{"shellstyle": "*X*Y*Z*W*V*"}]}`)
			return q
		},
		func(b *testing.B, q *Quamina) {
			// Permuted events so each visits fresh (state, byte) combos.
			perms := []string{
				`{"x":"XYZWVabcdefghij"}`,
				`{"x":"jihgfedcbaVWZYX"}`,
				`{"x":"aXbYcZdWeVfghij"}`,
				`{"x":"VWZXYjihgfedcba"}`,
				`{"x":"ZYXWVbacdefghij"}`,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent([]byte(perms[i%len(perms)]))
			}
		},
	)
}

// BenchmarkLazyDFA_ParallelMatchers — N goroutines on Copy() instances
// share one coreMatcher → share the lazy DFA cache. Validates the
// "shared on coreFields" architecture choice: one warmup serves all.
func BenchmarkLazyDFA_ParallelMatchers(b *testing.B) {
	for _, gor := range []int{8, 16, 32, 64} {
		b.Run(fmt.Sprintf("G=%d", gor), func(b *testing.B) {
			runBothModes(b,
				func(opts ...Option) *Quamina {
					q, _ := New(opts...)
					// Reuse the ManyOverlapping pattern set at N=64.
					n := 64
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
					// Warm shared cache on one goroutine before parallel section.
					for i := 0; i < 200; i++ {
						_, _ = q.MatchesForEvent(ev)
					}
					b.SetParallelism(gor)
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						cp := q.Copy()
						for pb.Next() {
							_, _ = cp.MatchesForEvent(ev)
						}
					})
				},
			)
		})
	}
}
