package quamina

import (
	"fmt"
	"testing"
)

// TestSingleByteTransition verifies that singleByteTransition correctly
// extracts the unique byte and target from a smallTable, or returns false
// when the table has 0, 2+, or range transitions.
func TestSingleByteTransition(t *testing.T) {
	// Single transition on byte 'a'
	target := &faState{table: newSmallTable()}
	tbl := makeSmallTable(nil, []byte{'a'}, []*faState{target})
	b, s, ok := singleByteTransition(tbl)
	if !ok || b != 'a' || s != target {
		t.Errorf("single 'a': got byte=%c, ok=%v, want 'a', true", b, ok)
	}

	// No transitions (all nil)
	empty := newSmallTable()
	_, _, ok = singleByteTransition(empty)
	if ok {
		t.Error("empty table: expected false")
	}

	// Two transitions
	target2 := &faState{table: newSmallTable()}
	tbl2 := makeSmallTable(nil, []byte{'a', 'b'}, []*faState{target, target2})
	_, _, ok = singleByteTransition(tbl2)
	if ok {
		t.Error("two transitions: expected false")
	}

	// Range transition (multiple bytes map to same non-nil state) - build
	// a table where bytes 'a' through 'c' all transition to target.
	var u unpackedTable
	for b := byte('a'); b <= 'c'; b++ {
		u[b] = target
	}
	rangeTbl := newSmallTable()
	rangeTbl.pack(&u)
	_, _, ok = singleByteTransition(rangeTbl)
	if ok {
		t.Error("range transition: expected false")
	}
}

// TestIsCleanChainState verifies the clean chain state predicate.
func TestIsCleanChainState(t *testing.T) {
	clean := &faState{table: newSmallTable()}
	if !isCleanChainState(clean) {
		t.Error("basic state should be clean")
	}

	spinner := &faState{table: newSmallTable(), isSpinner: true}
	if isCleanChainState(spinner) {
		t.Error("spinner should not be clean")
	}

	withEps := &faState{table: newSmallTable()}
	withEps.table.epsilons = []*faState{clean}
	if isCleanChainState(withEps) {
		t.Error("state with epsilons should not be clean")
	}

	withFT := &faState{table: newSmallTable(), fieldTransitions: []*fieldMatcher{{}}}
	if isCleanChainState(withFT) {
		t.Error("state with fieldTransitions should not be clean")
	}
}

// TestLiteralSeqFoobar verifies that *foobar* gets a literalSeq annotation
// on the spinEscape state (the state after the spinner exits on 'f').
func TestLiteralSeqFoobar(t *testing.T) {
	// makeShellStyleFA sets literalSeq during construction
	nfa, _ := makeShellStyleFA([]byte(`"*foobar*"`), sharedNullPrinter)

	quoteState := nfa.dStep('"')
	if quoteState == nil {
		t.Fatal("no transition on '\"'")
	}
	spinEscape := quoteState.table.dStep('f')
	if spinEscape == nil {
		t.Fatal("no transition on 'f' from spinner")
	}
	// "oobar" = 5 bytes in literal sequence
	if spinEscape.literal == nil {
		t.Fatal("literal is nil")
	}
	if string(spinEscape.literal.seq) != "oobar" {
		t.Errorf("literal.seq = %q, want %q", spinEscape.literal.seq, "oobar")
	}
	if spinEscape.literal.end == nil {
		t.Fatal("literal.end is nil")
	}
	if !spinEscape.literal.end.isSpinner {
		t.Error("literal.end should be the second spinner")
	}
}

// TestLiteralSeqTooShort verifies that *ab* doesn't get annotated
// because the chain after the escape byte is only 1 byte long.
func TestLiteralSeqTooShort(t *testing.T) {
	nfa, _ := makeShellStyleFA([]byte(`"*ab*"`), sharedNullPrinter)

	quoteState := nfa.dStep('"')
	if quoteState == nil {
		t.Fatal("no transition on '\"'")
	}
	spinEscape := quoteState.table.dStep('a')
	if spinEscape == nil {
		t.Fatal("no transition on 'a' from spinner")
	}
	if spinEscape.literal != nil {
		t.Errorf("expected nil literal for *ab*, got %q", spinEscape.literal.seq)
	}
}

// TestLiteralSeqMinimum verifies that *abc* gets a 2-byte literalSeq.
func TestLiteralSeqMinimum(t *testing.T) {
	nfa, _ := makeShellStyleFA([]byte(`"*abc*"`), sharedNullPrinter)

	quoteState := nfa.dStep('"')
	if quoteState == nil {
		t.Fatal("no transition on '\"'")
	}
	spinEscape := quoteState.table.dStep('a')
	if spinEscape == nil {
		t.Fatal("no transition on 'a' from spinner")
	}
	// "bc" = 2 bytes
	if spinEscape.literal == nil {
		t.Fatal("literal is nil")
	}
	if string(spinEscape.literal.seq) != "bc" {
		t.Errorf("literal.seq = %q, want %q", spinEscape.literal.seq, "bc")
	}
}

// TestLiteralSeqTwoChains verifies that *foo*bar* gets two separate
// literal sequence annotations.
func TestLiteralSeqTwoChains(t *testing.T) {
	nfa, _ := makeShellStyleFA([]byte(`"*foo*bar*"`), sharedNullPrinter)

	quoteState := nfa.dStep('"')
	if quoteState == nil {
		t.Fatal("no transition on '\"'")
	}

	// First chain: spinner1 exits on 'f' -> spinEscape1 with literal.seq "oo"
	spinEscape1 := quoteState.table.dStep('f')
	if spinEscape1 == nil {
		t.Fatal("no transition on 'f'")
	}
	if spinEscape1.literal == nil {
		t.Fatal("first literal is nil")
	}
	if string(spinEscape1.literal.seq) != "oo" {
		t.Errorf("first literal.seq = %q, want %q", spinEscape1.literal.seq, "oo")
	}

	// Follow the chain to find the second spinner: dStep through 'o', 'o'
	o1 := spinEscape1.table.dStep('o')
	if o1 == nil {
		t.Fatal("no transition on first 'o'")
	}
	spinner2 := o1.table.dStep('o')
	if spinner2 == nil {
		t.Fatal("no transition on second 'o'")
	}
	if !spinner2.isSpinner {
		t.Error("expected second spinner")
	}

	// Second chain: spinner2 exits on 'b' -> spinEscape2 with literal.seq "ar"
	spinEscape2 := spinner2.table.dStep('b')
	if spinEscape2 == nil {
		t.Fatal("no transition on 'b' from second spinner")
	}
	if spinEscape2.literal == nil {
		t.Fatal("second literal is nil")
	}
	if string(spinEscape2.literal.seq) != "ar" {
		t.Errorf("second literal.seq = %q, want %q", spinEscape2.literal.seq, "ar")
	}
}

// TestLiteralSeqTraversal verifies end-to-end matching with literal sequence
// optimization through the full Quamina API.
func TestLiteralSeqTraversal(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		matches []string
		nopes   []string
	}{
		{
			name:    "foobar",
			pattern: `{"val": [{"shellstyle": "*foobar*"}]}`,
			matches: []string{
				`{"val": "foobar"}`,
				`{"val": "xfoobar"}`,
				`{"val": "foobarx"}`,
				`{"val": "xxfoobarxx"}`,
				`{"val": "foobarfoobar"}`,
			},
			nopes: []string{
				`{"val": "fooba"}`,
				`{"val": "fooar"}`,
				`{"val": "obar"}`,
				`{"val": ""}`,
			},
		},
		{
			name:    "two-wildcards",
			pattern: `{"val": [{"shellstyle": "*foo*bar*"}]}`,
			matches: []string{
				`{"val": "foobar"}`,
				`{"val": "fooxbar"}`,
				`{"val": "xxfooxxbarxx"}`,
				`{"val": "foofoofoobar"}`,
			},
			nopes: []string{
				`{"val": "fooba"}`,
				`{"val": "barfoo"}`,
				`{"val": "fo"}`,
			},
		},
		{
			name:    "prefix-glob",
			pattern: `{"val": [{"shellstyle": "hello*"}]}`,
			matches: []string{
				`{"val": "hello"}`,
				`{"val": "helloworld"}`,
				`{"val": "hello there"}`,
			},
			nopes: []string{
				`{"val": "hell"}`,
				`{"val": "xhello"}`,
			},
		},
		{
			name:    "suffix-glob",
			pattern: `{"val": [{"shellstyle": "*world"}]}`,
			matches: []string{
				`{"val": "world"}`,
				`{"val": "helloworld"}`,
				`{"val": "the whole world"}`,
			},
			nopes: []string{
				`{"val": "worlds"}`,
				`{"val": "worl"}`,
			},
		},
		{
			name:    "overlapping-literal",
			pattern: `{"val": [{"shellstyle": "*abab*"}]}`,
			matches: []string{
				`{"val": "abab"}`,
				`{"val": "xabab"}`,
				`{"val": "ababab"}`,
				`{"val": "aababx"}`,
			},
			nopes: []string{
				`{"val": "aba"}`,
				`{"val": "abba"}`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, err := New()
			if err != nil {
				t.Fatal(err)
			}
			err = q.AddPattern("P1", tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range tc.matches {
				matches, err := q.MatchesForEvent([]byte(event))
				if err != nil {
					t.Fatalf("MatchesForEvent(%s): %v", event, err)
				}
				if len(matches) != 1 {
					t.Errorf("should match %s, got %d matches", event, len(matches))
				}
			}
			for _, event := range tc.nopes {
				matches, err := q.MatchesForEvent([]byte(event))
				if err != nil {
					t.Fatalf("MatchesForEvent(%s): %v", event, err)
				}
				if len(matches) != 0 {
					t.Errorf("should NOT match %s, got %d matches", event, len(matches))
				}
			}
		})
	}
}

// TestLiteralSeqMergedPatterns verifies that chain optimization
// works correctly when multiple patterns are merged together.
func TestLiteralSeqMergedPatterns(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}
	err = q.AddPattern("long", `{"val": [{"shellstyle": "*foobar*"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.AddPattern("short", `{"val": [{"shellstyle": "*foo*"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.AddPattern("other", `{"val": [{"shellstyle": "*baz*"}]}`)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		event string
		want  map[string]bool
	}{
		{`{"val": "foobar"}`, map[string]bool{"long": true, "short": true}},
		{`{"val": "foo"}`, map[string]bool{"short": true}},
		{`{"val": "baz"}`, map[string]bool{"other": true}},
		{`{"val": "foobarbaz"}`, map[string]bool{"long": true, "short": true, "other": true}},
		{`{"val": "nothing"}`, map[string]bool{}},
	}

	for _, tc := range tests {
		matches, err := q.MatchesForEvent([]byte(tc.event))
		if err != nil {
			t.Fatalf("MatchesForEvent(%s): %v", tc.event, err)
		}
		got := make(map[string]bool, len(matches))
		for _, m := range matches {
			got[m.(string)] = true
		}
		for name := range tc.want {
			if !got[name] {
				t.Errorf("event %s: missing expected match %s, got %v", tc.event, name, matches)
			}
		}
		for name := range got {
			if !tc.want[name] {
				t.Errorf("event %s: unexpected match %s", tc.event, name)
			}
		}
	}
}

// TestLiteralSeqWildcard verifies chain optimization with the
// wildcard pattern type (which supports backslash escaping).
func TestLiteralSeqWildcard(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}
	err = q.AddPattern("P1", `{"val": [{"wildcard": "*hello*"}]}`)
	if err != nil {
		t.Fatal(err)
	}

	matches, err := q.MatchesForEvent([]byte(`{"val": "say hello world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match, got %d", len(matches))
	}

	matches, err = q.MatchesForEvent([]byte(`{"val": "no match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matches))
	}
}

// TestLiteralSeqWithNfaTest exercises the chain optimization through
// the same test paths as TestNfa2Dfa, verifying NFA traversal with chains.
func TestLiteralSeqWithNfaTest(t *testing.T) {
	type n2dtest struct {
		pattern string
		shoulds []string
		nopes   []string
	}
	tests := []n2dtest{
		{
			pattern: "*abcdef",
			shoulds: []string{"abcdef", "xabcdef", "abcabcdef"},
			nopes:   []string{"abcde", "abcdex", "bcdef"},
		},
		{
			pattern: "a*bcdef",
			shoulds: []string{"abcdef", "axybcdef", "abcbcdef"},
			nopes:   []string{"bcdef", "abcde"},
		},
		{
			pattern: "abcde*f",
			shoulds: []string{"abcdef", "abcdexf", "abcdexyf"},
			nopes:   []string{"abcde", "abcdeg"},
		},
		{
			pattern: "abcdef*",
			shoulds: []string{"abcdef", "abcdefghi"},
			nopes:   []string{"xabcdef", "abcdeg"},
		},
	}
	pp := sharedNullPrinter
	transitions := []*fieldMatcher{}
	bufs := newNfaBuffers()
	for _, test := range tests {
		// makeShellStyleFA sets literalSeq during construction
		nfa, _ := makeShellStyleFA(asQuotedBytes(t, test.pattern), pp)

		for _, should := range test.shoulds {
			matched := testTraverseNFA(nfa, asQuotedBytes(t, should), transitions, bufs)
			if len(matched) != 1 {
				t.Errorf("NFA %s didn't match %s", test.pattern, should)
			}
		}
		for _, nope := range test.nopes {
			matched := testTraverseNFA(nfa, asQuotedBytes(t, nope), transitions, bufs)
			if len(matched) != 0 {
				t.Errorf("NFA %s matched %s", test.pattern, nope)
			}
		}
	}
}

// BenchmarkLiteralSeqTraversal benchmarks traversal of shell-style patterns
// with long literals, comparing performance with the chain optimization.
func BenchmarkLiteralSeqTraversal(b *testing.B) {
	patterns := []struct {
		name    string
		pattern string
		event   string
	}{
		{
			name:    "long-literal",
			pattern: `{"val": [{"shellstyle": "*abcdefghij*"}]}`,
			event:   `{"val": "xxxabcdefghijxxx"}`,
		},
		{
			name:    "two-long-literals",
			pattern: `{"val": [{"shellstyle": "*abcde*fghij*"}]}`,
			event:   `{"val": "xxxabcdexxxfghijxxx"}`,
		},
		{
			name:    "short-value-long-literal",
			pattern: `{"val": [{"shellstyle": "*abcdefghij*"}]}`,
			event:   `{"val": "abcdefghij"}`,
		},
	}

	for _, tc := range patterns {
		b.Run(tc.name, func(b *testing.B) {
			q, _ := New()
			_ = q.AddPattern("P1", tc.pattern)
			eventBytes := []byte(tc.event)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = q.MatchesForEvent(eventBytes)
			}
		})
	}
}

// TestLiteralSeqStress runs many patterns with varying literal lengths
// to stress-test the optimization under merging.
func TestLiteralSeqStress(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add 20 patterns with varying literal lengths
	literals := []string{
		"abc", "defgh", "ijklmn", "opqrst", "uvwxyz",
		"hello", "world", "foobar", "bazqux", "testing",
		"alpha", "bravo", "charlie", "delta", "echo",
		"foxtrot", "golf", "hotel", "india", "juliet",
	}
	for i, lit := range literals {
		pattern := fmt.Sprintf(`{"val": [{"shellstyle": "*%s*"}]}`, lit)
		err = q.AddPattern(fmt.Sprintf("P%d", i), pattern)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test that each literal matches
	for i, lit := range literals {
		event := fmt.Sprintf(`{"val": "xx%sxx"}`, lit)
		matches, err := q.MatchesForEvent([]byte(event))
		if err != nil {
			t.Fatalf("event %s: %v", event, err)
		}
		found := false
		for _, m := range matches {
			if m.(string) == fmt.Sprintf("P%d", i) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("event %s: expected P%d in matches %v", event, i, matches)
		}
	}

	// Test that non-matching doesn't match
	matches, err := q.MatchesForEvent([]byte(`{"val": "zzzzz"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for 'zzzzz', got %d: %v", len(matches), matches)
	}
}
