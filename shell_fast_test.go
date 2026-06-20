package quamina

import (
	"testing"
)

func TestShellFastMatcher(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		// Basic patterns
		{"*foo*", "foo", true},
		{"*foo*", "xfoobar", true},
		{"*foo*", "bar", false},

		// Multiple literals
		{"*foo*bar*", "foobar", true},
		{"*foo*bar*", "xxfooxxbarxx", true},
		{"*foo*bar*", "barfoo", false}, // order matters
		{"*foo*bar*", "foo", false},

		// Prefix patterns (no leading *)
		{"foo*", "foobar", true},
		{"foo*", "foo", true},
		{"foo*", "xfoo", false},

		// Suffix patterns (no trailing *)
		{"*bar", "foobar", true},
		{"*bar", "bar", true},
		{"*bar", "barx", false},

		// Exact match (no wildcards)
		{"foo", "foo", true},
		{"foo", "foobar", false},
		{"foo", "xfoo", false},

		// Prefix + suffix
		{"foo*bar", "foobar", true},
		{"foo*bar", "fooxyzbar", true},
		{"foo*bar", "foobarx", false},
		{"foo*bar", "xfoobar", false},

		// Complex patterns
		{"*a*b*c*", "abc", true},
		{"*a*b*c*", "xaxbxcx", true},
		{"*a*b*c*", "cab", false}, // 'c' before 'a'
		{"*a*b*c*d*", "abcd", true},
		{"*a*b*c*d*", "xxaxxbxxcxxdxx", true},

		// Edge cases
		{"*", "anything", true},
		{"*", "", true},
		{"**", "anything", true}, // adjacent wildcards collapsed
		{"***", "", true},

		// Empty input
		{"*foo*", "", false},
		{"*", "", true},

		// Single character patterns
		{"*x*", "x", true},
		{"*x*", "axb", true},
		{"x*", "xyz", true},
		{"*x", "abx", true},
	}

	for _, tc := range tests {
		pattern := []byte(`"` + tc.pattern + `"`)
		sfm := newShellFastMatcher(pattern, nil)
		if sfm == nil {
			t.Fatalf("pattern %q: newShellFastMatcher returned nil", tc.pattern)
		}
		got := sfm.match([]byte(tc.input))
		if got != tc.want {
			t.Errorf("pattern=%q input=%q: got %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestShellFastMatcherReusable(t *testing.T) {
	// Verify matcher can be used multiple times
	pattern := []byte(`"*foo*bar*"`)
	sfm := newShellFastMatcher(pattern, nil)

	inputs := []struct {
		input string
		want  bool
	}{
		{"foobar", true},
		{"xfoobar", true},
		{"fooxbar", true},
		{"barfoo", false},
		{"foobar", true}, // repeat to ensure state isn't corrupted
	}

	for _, tc := range inputs {
		got := sfm.match([]byte(tc.input))
		if got != tc.want {
			t.Errorf("input=%q: got %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestShellFastMatcherUnsupported(t *testing.T) {
	// Patterns with unsupported features should return nil
	unsupported := []string{
		`"foo?bar"`,  // ? wildcard
		`"foo[abc]"`, // character class
		`"foo\\bar"`, // backslash
		`foo`,        // no quotes
	}

	for _, p := range unsupported {
		sfm := newShellFastMatcher([]byte(p), nil)
		if sfm != nil {
			t.Errorf("pattern %q should return nil (unsupported)", p)
		}
	}
}

// TestShellFastSinglePatternVsNFA verifies that the fast path (single shellstyle pattern)
// agrees with the NFA for a representative set of inputs.
func TestShellFastSinglePatternVsNFA(t *testing.T) {
	patterns := []string{
		`*foo*`,
		`a*z`,
		`foo*`,
		`*bar`,
		`*foo*bar*`,
		`hello*world`,
		`*`,
	}
	inputs := []string{
		"foo", "xfoox", "afoo", "foox", "bar", "xbar", "barx",
		"az", "axz", "xaz", "a", "z",
		"foobar", "xxfooxxbarxx", "barfoo",
		"helloworld", "helloxworld", "xhelloworld",
		"anything", "", "x",
	}

	for _, pat := range patterns {
		jsonPat := `{"f": [{"shellstyle": "` + pat + `"}]}`

		// Build fast-path quamina instance (single pattern → shellFastMatcher)
		qFast, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if err := qFast.AddPattern("p1", jsonPat); err != nil {
			t.Fatalf("pattern %q: AddPattern error: %v", pat, err)
		}

		// Build NFA-path instance (two patterns force NFA; we only care about p1 results)
		qNFA, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if err := qNFA.AddPattern("p1", jsonPat); err != nil {
			t.Fatalf("pattern %q: AddPattern NFA error: %v", pat, err)
		}
		// Add a second distinct pattern to force migration to NFA
		if err := qNFA.AddPattern("p2", `{"f": [{"shellstyle": "*ZZZZ_sentinel_ZZZZ*"}]}`); err != nil {
			t.Fatalf("pattern %q: AddPattern sentinel error: %v", pat, err)
		}

		for _, inp := range inputs {
			event := `{"f": "` + inp + `"}`

			fastMatches, err := qFast.MatchesForEvent([]byte(event))
			if err != nil {
				t.Fatalf("pattern=%q input=%q: fast MatchesForEvent error: %v", pat, inp, err)
			}
			nfaMatches, err := qNFA.MatchesForEvent([]byte(event))
			if err != nil {
				t.Fatalf("pattern=%q input=%q: NFA MatchesForEvent error: %v", pat, inp, err)
			}

			fastHit := false
			for _, m := range fastMatches {
				if m == "p1" {
					fastHit = true
				}
			}
			nfaHit := false
			for _, m := range nfaMatches {
				if m == "p1" {
					nfaHit = true
				}
			}

			if fastHit != nfaHit {
				t.Errorf("pattern=%q input=%q: fast=%v NFA=%v (MISMATCH)", pat, inp, fastHit, nfaHit)
			}
		}
	}
}

// Benchmarks comparing shell fast matcher to NFA traversal

func BenchmarkShellFastMatch(b *testing.B) {
	pattern := []byte(`"*foo*bar*baz*"`)
	sfm := newShellFastMatcher(pattern, nil)
	input := []byte("prefix_foo_middle_bar_more_baz_suffix")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sfm.match(input)
	}
}

func BenchmarkShellFastSinglePattern(b *testing.B) {
	q, _ := New()
	_ = q.AddPattern("p1", `{"f": [{"shellstyle": "*foo*bar*baz*"}]}`)
	event := []byte(`{"f": "prefix_foo_middle_bar_more_baz_suffix"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.MatchesForEvent(event)
	}
}

func BenchmarkShellStyleTwoPatterns(b *testing.B) {
	q, _ := New()
	_ = q.AddPattern("p1", `{"f": [{"shellstyle": "*foo*bar*baz*"}]}`)
	_ = q.AddPattern("p2", `{"f": [{"shellstyle": "*other*"}]}`)
	event := []byte(`{"f": "prefix_foo_middle_bar_more_baz_suffix"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.MatchesForEvent(event)
	}
}

func BenchmarkShellFastLong(b *testing.B) {
	q, _ := New()
	_ = q.AddPattern("p1", `{"f": [{"shellstyle": "*needle*"}]}`)

	// Create a long value with the needle near the end
	value := make([]byte, 10000)
	for i := range value {
		value[i] = byte('a' + (i % 26))
	}
	copy(value[9990:], []byte("needle"))
	event := append([]byte(`{"f": "`), value...)
	event = append(event, []byte(`"}`)...)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.MatchesForEvent(event)
	}
}
