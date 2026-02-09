package quamina

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// BenchmarkShellstyleMultiMatch exercises shellstyle pattern matching with wildcards
// across a variety of character sets including ASCII, CJK, and emoji. This benchmark
// is useful for measuring allocation patterns in NFA traversal code paths.
func BenchmarkShellstyleMultiMatch(b *testing.B) {
	q, _ := New()

	// Add multiple shellstyle patterns like in TestBigShellStyle
	for _, letter := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P"} {
		pattern := fmt.Sprintf(`{"STREET": [ {"shellstyle": "%s*"} ]}`, letter)
		if err := q.AddPattern(letter, pattern); err != nil {
			b.Fatal(err)
		}
	}

	// Add some funky patterns with multiple wildcards that trigger more complex NFA traversal
	funkyPatterns := map[string]string{
		"funky1": "*E*E*E*",
		"funky2": "*A*B*",
		"funky3": "*N*P*",
		"funky4": "*O*O*O*",
	}
	for name, shellstyle := range funkyPatterns {
		pattern := fmt.Sprintf(`{"STREET": [ {"shellstyle": "%s"} ]}`, shellstyle)
		if err := q.AddPattern(name, pattern); err != nil {
			b.Fatal(err)
		}
	}

	// Add CJK patterns to test Unicode handling
	cjkPatterns := map[string]string{
		"jp1": "*東京*",
		"jp2": "新*",
		"cn1": "*北京*",
		"cn2": "上海*",
		"kr1": "*서울*",
	}
	for name, shellstyle := range cjkPatterns {
		pattern := fmt.Sprintf(`{"STREET": [ {"shellstyle": "%s"} ]}`, shellstyle)
		if err := q.AddPattern(name, pattern); err != nil {
			b.Fatal(err)
		}
	}

	// Add emoji patterns to test multi-byte UTF-8 sequences
	emojiPatterns := map[string]string{
		"emoji1": "*🎉*",
		"emoji2": "🚀*",
		"emoji3": "*❤️*",
		"emoji4": "*🌟*🎯*",
	}
	for name, shellstyle := range emojiPatterns {
		pattern := fmt.Sprintf(`{"STREET": [ {"shellstyle": "%s"} ]}`, shellstyle)
		if err := q.AddPattern(name, pattern); err != nil {
			b.Fatal(err)
		}
	}

	// Events that will match and require NFA traversal
	events := [][]byte{
		// English streets
		[]byte(`{"STREET": "ASHBURY"}`),
		[]byte(`{"STREET": "BELVEDERE"}`),
		[]byte(`{"STREET": "CRANLEIGH"}`),
		[]byte(`{"STREET": "DEER PARK"}`),
		[]byte(`{"STREET": "EMBARCADERO"}`),
		[]byte(`{"STREET": "FULTON"}`),
		[]byte(`{"STREET": "GEARY"}`),
		[]byte(`{"STREET": "HAIGHT"}`),
		[]byte(`{"STREET": "IRVING"}`),
		[]byte(`{"STREET": "JUDAH"}`),
		[]byte(`{"STREET": "KEARNY"}`),
		[]byte(`{"STREET": "LOMBARD"}`),
		[]byte(`{"STREET": "MARKET"}`),
		[]byte(`{"STREET": "NORIEGA"}`),
		[]byte(`{"STREET": "OCTAVIA"}`),
		[]byte(`{"STREET": "POLK"}`),
		// Streets with multiple vowels for funky patterns
		[]byte(`{"STREET": "EMBARCADERO STREET"}`),
		[]byte(`{"STREET": "ALABAMA"}`),
		[]byte(`{"STREET": "NAPOLEON"}`),
		[]byte(`{"STREET": "COLORADO"}`),
		// CJK streets
		[]byte(`{"STREET": "東京タワー通り"}`),
		[]byte(`{"STREET": "新宿駅前"}`),
		[]byte(`{"STREET": "北京路"}`),
		[]byte(`{"STREET": "上海南京路"}`),
		[]byte(`{"STREET": "서울대로"}`),
		// Emoji streets (fun test case!)
		[]byte(`{"STREET": "Party Street 🎉"}`),
		[]byte(`{"STREET": "🚀 Rocket Road"}`),
		[]byte(`{"STREET": "Love ❤️ Lane"}`),
		[]byte(`{"STREET": "Star 🌟 Plaza 🎯"}`),
		// Mixed
		[]byte(`{"STREET": "Tokyo 東京 Street"}`),
		[]byte(`{"STREET": "Happy 😊 Avenue"}`),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, event := range events {
			matches, err := q.MatchesForEvent(event)
			if err != nil {
				b.Fatal(err)
			}
			if len(matches) == 0 {
				b.Fatalf("no matches for event: %s", event)
			}
		}
	}
}

// BenchmarkShellstyleManyMatchers measures transmap dedup performance when
// many patterns all match a single event, producing many field matchers per
// traversal. This exercises the adaptive hash set that activates above
// transmapLinearMax entries.
func BenchmarkShellstyleManyMatchers(b *testing.B) {
	// Each sub-benchmark uses a different number of overlapping patterns.
	// At counts above transmapLinearMax (16), the hash set path is used.
	for _, count := range []int{8, 16, 32, 64, 128, 256, 512, 1024} {
		b.Run(fmt.Sprintf("patterns=%d", count), func(b *testing.B) {
			q, _ := New()

			// Build an event value that contains all the substrings we'll pattern on.
			// Use distinct 3-char tokens so patterns don't accidentally match each other.
			tokens := make([]string, count)
			for i := range tokens {
				tokens[i] = fmt.Sprintf("t%04x", i)
			}
			value := strings.Join(tokens, "-")
			event := []byte(fmt.Sprintf(`{"val": %q}`, value))

			// Add count patterns, each matching a different substring in the value.
			// Every pattern matches the event, so the transmap collects count field matchers.
			for i, tok := range tokens {
				pattern := fmt.Sprintf(`{"val": [{"shellstyle": "*%s*"}]}`, tok)
				if err := q.AddPattern(fmt.Sprintf("p%d", i), pattern); err != nil {
					b.Fatal(err)
				}
			}

			// Verify all patterns match.
			matches, err := q.MatchesForEvent(event)
			if err != nil {
				b.Fatal(err)
			}
			if len(matches) != count {
				b.Fatalf("expected %d matches, got %d", count, len(matches))
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(event)
			}
		})
	}
}

// BenchmarkShellstyleManyMatchersVariedInput is like ManyMatchers but uses
// a pool of distinct events per iteration. Each event contains the same tokens
// in a different random order surrounded by random padding, so the DFA cache
// sees different byte sequences and must handle cache misses realistically.
func BenchmarkShellstyleManyMatchersVariedInput(b *testing.B) {
	for _, count := range []int{8, 16, 32, 64, 128, 256, 512, 1024} {
		b.Run(fmt.Sprintf("patterns=%d", count), func(b *testing.B) {
			q, _ := New()

			tokens := make([]string, count)
			for i := range tokens {
				tokens[i] = fmt.Sprintf("t%04x", i)
			}

			for i, tok := range tokens {
				pattern := fmt.Sprintf(`{"val": [{"shellstyle": "*%s*"}]}`, tok)
				if err := q.AddPattern(fmt.Sprintf("p%d", i), pattern); err != nil {
					b.Fatal(err)
				}
			}

			// Build a pool of events. Each shuffles the tokens into a different
			// order and adds random padding between them, producing distinct byte
			// sequences that all match every pattern.
			const poolSize = 64
			rng := rand.New(rand.NewSource(42))
			events := make([][]byte, poolSize)
			shuffled := make([]string, count)
			for i := range events {
				copy(shuffled, tokens)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				var buf strings.Builder
				for j, tok := range shuffled {
					if j > 0 {
						// Random separator: 1-4 random lowercase letters
						padLen := rng.Intn(4) + 1
						for k := 0; k < padLen; k++ {
							buf.WriteByte(byte('a' + rng.Intn(26)))
						}
					}
					buf.WriteString(tok)
				}
				events[i] = []byte(fmt.Sprintf(`{"val": %q}`, buf.String()))
			}

			// Verify all events match all patterns.
			for i, event := range events {
				matches, err := q.MatchesForEvent(event)
				if err != nil {
					b.Fatal(err)
				}
				if len(matches) != count {
					b.Fatalf("event %d: expected %d matches, got %d", i, count, len(matches))
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(events[i%poolSize])
			}
		})
	}
}
