package quamina

import (
	"fmt"
	"testing"
	"unsafe"
)

// TestLazyDFAStateStats measures actual DFA state usage for various patterns
func TestLazyDFAStateStats(t *testing.T) {
	patterns := []struct {
		name    string
		pattern string
		inputs  []string
	}{
		{
			name:    "simple *a*",
			pattern: `{"f": [{"shellstyle": "*a*"}]}`,
			inputs:  []string{"a", "xa", "ax", "xax", "aaa", "xxxayyy"},
		},
		{
			name:    "medium *a*b*c*",
			pattern: `{"f": [{"shellstyle": "*a*b*c*"}]}`,
			inputs:  []string{"abc", "xaxbxcx", "aaabbbccc", "abcabcabc"},
		},
		{
			name:    "complex *a*b*c*d*e*",
			pattern: `{"f": [{"shellstyle": "*a*b*c*d*e*"}]}`,
			inputs:  []string{"abcde", "xaxbxcxdxex", "aabbccddee"},
		},
		{
			name:    "prefix foo*",
			pattern: `{"f": [{"shellstyle": "foo*"}]}`,
			inputs:  []string{"foo", "foobar", "foobarbaz"},
		},
		{
			name:    "suffix *bar",
			pattern: `{"f": [{"shellstyle": "*bar"}]}`,
			inputs:  []string{"bar", "foobar", "xxxbar"},
		},
	}

	for _, tc := range patterns {
		t.Run(tc.name, func(t *testing.T) {
			q, err := New()
			if err != nil {
				t.Fatal(err)
			}
			if err := q.AddPattern("p1", tc.pattern); err != nil {
				t.Fatal(err)
			}

			// Match events to populate the cache
			for _, input := range tc.inputs {
				event := fmt.Sprintf(`{"f": "%s"}`, input)
				_, _ = q.MatchesForEvent([]byte(event))
			}

			// Run with more varied inputs
			for i := 0; i < 100; i++ {
				input := fmt.Sprintf("x%dx", i)
				for _, c := range tc.inputs {
					input += c
				}
				event := fmt.Sprintf(`{"f": "%s"}`, input)
				_, _ = q.MatchesForEvent([]byte(event))
			}

			// Get stats from the internal buffers
			caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
			hitRate := float64(0)
			if hits+misses > 0 {
				hitRate = float64(hits) / float64(hits+misses) * 100
			}
			t.Logf("Pattern: %s", tc.name)
			t.Logf("  Caches: %d, States: %d, Creates: %d", caches, states, creates)
			t.Logf("  Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
		})
	}
}

// TestLazyDFAMemoryPerState estimates memory per lazy DFA state
func TestLazyDFAMemoryPerState(t *testing.T) {
	// lazyDFAState has:
	// - transitions [256]*lazyDFAState = 256 * 8 = 2048 bytes
	// - fieldTransitions []*fieldMatcher = 24 bytes (slice header)
	// - nfaStates []*faState = 24 bytes (slice header)
	// Total: ~2096 bytes per state

	stateSize := unsafe.Sizeof(lazyDFAState{})
	t.Logf("lazyDFAState struct size: %d bytes", stateSize)

	// The transitions array dominates
	t.Logf("Transitions array: %d pointers * %d bytes = %d bytes",
		256, unsafe.Sizeof((*lazyDFAState)(nil)), 256*unsafe.Sizeof((*lazyDFAState)(nil)))

	// With maxLazyDFAStates = 1000:
	// Max memory = 1000 * 2096 = ~2 MB per cache
	// Per goroutine (via Copy()), so N goroutines = N * 2 MB
	t.Logf("Max memory per cache (1000 states): ~%d MB", 1000*int(stateSize)/(1024*1024))
	t.Logf("Max memory per cache (100 states): ~%d KB", 100*int(stateSize)/1024)
}

// TestLazyDFACacheHitRate measures cache effectiveness
func TestLazyDFACacheHitRate(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add a pattern that exercises the cache
	pattern := `{"f": [{"shellstyle": "*foo*bar*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Match many events to warm the cache
	events := []string{
		`{"f": "foobar"}`,
		`{"f": "xfooxbarx"}`,
		`{"f": "foofoofoobarbarbar"}`,
		`{"f": "xxxxfooyyyyybarzzz"}`,
		`{"f": "nomatch"}`,
	}

	for i := 0; i < 1000; i++ {
		for _, event := range events {
			_, _ = q.MatchesForEvent([]byte(event))
		}
	}

	caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(hits) / float64(hits+misses) * 100
	t.Logf("Caches: %d, States: %d, Creates: %d", caches, states, creates)
	t.Logf("Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
}

// TestLazyDFAMultiplePatterns tests state explosion with multiple overlapping patterns
func TestLazyDFAMultiplePatterns(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add multiple shell-style patterns on the same field
	patterns := []string{
		`{"f": [{"shellstyle": "*a*"}]}`,
		`{"f": [{"shellstyle": "*b*"}]}`,
		`{"f": [{"shellstyle": "*c*"}]}`,
		`{"f": [{"shellstyle": "*ab*"}]}`,
		`{"f": [{"shellstyle": "*bc*"}]}`,
		`{"f": [{"shellstyle": "*abc*"}]}`,
	}
	for i, p := range patterns {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), p); err != nil {
			t.Fatal(err)
		}
	}

	// Match varied inputs
	inputs := []string{"a", "b", "c", "ab", "bc", "abc", "xyzabc123", "nomatch"}
	for i := 0; i < 100; i++ {
		for _, input := range inputs {
			event := fmt.Sprintf(`{"f": "%s%d"}`, input, i)
			_, _ = q.MatchesForEvent([]byte(event))
		}
	}

	caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}
	t.Logf("Multiple patterns on same field:")
	t.Logf("  Caches: %d, States: %d, Creates: %d", caches, states, creates)
	t.Logf("  Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
	t.Logf("  Memory estimate: %d KB", states*2096/1024)
}

// TestLazyDFAWorstCase tries to create pathological state explosion
func TestLazyDFAWorstCase(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Pattern with many wildcards - potential for state explosion
	pattern := `{"f": [{"shellstyle": "*a*b*c*d*e*f*g*h*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Match with highly varied inputs to stress the cache
	for i := 0; i < 1000; i++ {
		// Create inputs with different orderings and repetitions
		input := fmt.Sprintf("x%da%db%dc%dd%de%df%dg%dh%d", i, i%10, i%7, i%5, i%3, i%11, i%13, i%17, i%19)
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		_, _ = q.MatchesForEvent([]byte(event))
	}

	caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}
	t.Logf("Worst case pattern (*a*b*c*d*e*f*g*h*):")
	t.Logf("  Caches: %d, States: %d, Creates: %d", caches, states, creates)
	t.Logf("  Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
	t.Logf("  Memory estimate: %d KB", states*2096/1024)

	if states >= maxLazyDFAStates {
		t.Logf("  WARNING: Hit state limit! Cache disabled.")
	}
}

// TestLazyDFAManyPatterns tests with many diverse patterns
func TestLazyDFAManyPatterns(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add 50 different shell-style patterns
	for i := 0; i < 50; i++ {
		pattern := fmt.Sprintf(`{"f": [{"shellstyle": "*pat%d*"}]}`, i)
		if err := q.AddPattern(fmt.Sprintf("p%d", i), pattern); err != nil {
			t.Fatal(err)
		}
	}

	// Match varied inputs
	for i := 0; i < 500; i++ {
		input := fmt.Sprintf("xxpat%dxx", i%50)
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		_, _ = q.MatchesForEvent([]byte(event))
	}

	caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}
	t.Logf("50 diverse patterns:")
	t.Logf("  Caches: %d, States: %d, Creates: %d", caches, states, creates)
	t.Logf("  Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
	t.Logf("  Memory estimate: %d KB", states*2096/1024)
}

// TestLazyDFARandomInputs tests with random-ish inputs to stress state creation
func TestLazyDFARandomInputs(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	pattern := `{"f": [{"shellstyle": "*x*y*z*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Generate many unique random-ish inputs
	for i := 0; i < 10000; i++ {
		// Different prefix lengths and character patterns
		input := fmt.Sprintf("%c%c%c%cx%c%c%cy%c%c%cz%c%c",
			byte('a'+(i%26)), byte('a'+((i/26)%26)), byte('a'+((i/676)%26)), byte('a'+((i/17576)%26)),
			byte('a'+((i*3)%26)), byte('a'+((i*7)%26)), byte('a'+((i*11)%26)),
			byte('a'+((i*13)%26)), byte('a'+((i*17)%26)), byte('a'+((i*19)%26)),
			byte('a'+((i*23)%26)), byte('a'+((i*29)%26)))
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		_, _ = q.MatchesForEvent([]byte(event))
	}

	caches, states, creates, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}
	t.Logf("10000 random-ish inputs:")
	t.Logf("  Caches: %d, States: %d, Creates: %d", caches, states, creates)
	t.Logf("  Hits: %d, Misses: %d, Hit rate: %.1f%%", hits, misses, hitRate)
	t.Logf("  Memory estimate: %d KB", states*2096/1024)

	if states >= maxLazyDFAStates {
		t.Logf("  HIT STATE LIMIT! Cache disabled, fell back to NFA")
	}
}

// TestLazyDFAInputInfluence shows how input affects state count
func TestLazyDFAInputInfluence(t *testing.T) {
	// Pattern with repeated character - input order matters!
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	pattern := `{"f": [{"shellstyle": "*a*a*a*a*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Test 1: Simple matching input
	for i := 0; i < 100; i++ {
		_, _ = q.MatchesForEvent([]byte(`{"f": "aaaa"}`))
	}
	_, states1, _, _, _ := q.bufs.lazyDFAStats()
	t.Logf("After 'aaaa' x100: %d states", states1)

	// Test 2: Input with 'a's spread out
	for i := 0; i < 100; i++ {
		_, _ = q.MatchesForEvent([]byte(`{"f": "xaxaxaxa"}`))
	}
	_, states2, _, _, _ := q.bufs.lazyDFAStats()
	t.Logf("After 'xaxaxaxa' x100: %d states (delta: %d)", states2, states2-states1)

	// Test 3: Many 'a's - creates more state combinations
	for i := 0; i < 100; i++ {
		_, _ = q.MatchesForEvent([]byte(`{"f": "aaaaaaaaaaaa"}`)) // 12 a's
	}
	_, states3, _, _, _ := q.bufs.lazyDFAStats()
	t.Logf("After 12 a's x100: %d states (delta: %d)", states3, states3-states2)

	// Test 4: Mix of a's and other chars
	for i := 0; i < 100; i++ {
		input := fmt.Sprintf(`{"f": "a%ca%ca%ca"}`, byte('b'+i%24), byte('b'+i%24), byte('b'+i%24))
		_, _ = q.MatchesForEvent([]byte(input))
	}
	_, states4, _, _, _ := q.bufs.lazyDFAStats()
	t.Logf("After varied inputs x100: %d states (delta: %d)", states4, states4-states3)

	t.Logf("Total memory: %d KB", states4*2096/1024)
}

// TestLazyDFAStateExplosion tries to trigger actual state explosion
func TestLazyDFAStateExplosion(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Pattern designed to create many state combinations
	// Each 'a' can match at multiple positions, creating 2^n potential states
	pattern := `{"f": [{"shellstyle": "*a*a*a*a*a*a*a*a*"}]}` // 8 a's
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Input with many a's - each could match at different pattern positions
	inputs := []string{
		"aaaaaaaa",           // exactly 8 a's
		"aaaaaaaaaaaa",       // 12 a's
		"aaaaaaaaaaaaaaaa",   // 16 a's
		"aaaaaaaaaaaaaaaaaaaa", // 20 a's
	}

	for _, input := range inputs {
		for i := 0; i < 100; i++ {
			event := fmt.Sprintf(`{"f": "%s"}`, input)
			_, _ = q.MatchesForEvent([]byte(event))
		}
		_, states, _, hits, misses := q.bufs.lazyDFAStats()
		hitRate := float64(hits) / float64(hits+misses) * 100
		t.Logf("After %d a's: %d states, %.1f%% hit rate, %d KB",
			len(input), states, hitRate, states*2096/1024)
	}

	_, finalStates, _, _, _ := q.bufs.lazyDFAStats()
	if finalStates >= maxLazyDFAStates {
		t.Logf("HIT STATE LIMIT at %d states!", finalStates)
	}
}

// TestLazyDFADifferentChars tests if different characters create more states
func TestLazyDFADifferentChars(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Pattern with different characters - more independent choices
	pattern := `{"f": [{"shellstyle": "*a*b*c*d*e*f*g*h*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Inputs with chars in different orders
	inputs := []string{
		"abcdefgh",
		"hgfedcba",        // reverse order - won't match!
		"xxaxxbxxcxxdxxexxfxxgxxhxx",
		"abcdefghabcdefgh", // doubled
		"aabbccddeeffgghh", // repeated chars
		"abababababababab", // alternating (missing some)
		"aaaabbbbccccddddeeeeffffgggghhhh", // many of each
	}

	for _, input := range inputs {
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		matches, _ := q.MatchesForEvent([]byte(event))
		_, states, _, _, _ := q.bufs.lazyDFAStats()
		t.Logf("Input %q: %d states, matched: %v", input[:min(20, len(input))], states, len(matches) > 0)
	}
}

// TestLazyDFAMultipleOverlapping tests overlapping patterns with shared chars
func TestLazyDFAMultipleOverlapping(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Multiple patterns that share characters - creates merged NFA
	patterns := []string{
		`{"f": [{"shellstyle": "*ab*"}]}`,
		`{"f": [{"shellstyle": "*bc*"}]}`,
		`{"f": [{"shellstyle": "*cd*"}]}`,
		`{"f": [{"shellstyle": "*abc*"}]}`,
		`{"f": [{"shellstyle": "*bcd*"}]}`,
		`{"f": [{"shellstyle": "*abcd*"}]}`,
	}
	for i, p := range patterns {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), p); err != nil {
			t.Fatal(err)
		}
	}

	// Various inputs
	testInputs := []string{
		"ab", "bc", "cd", "abc", "bcd", "abcd",
		"xabx", "xbcx", "xcdx", "xabcx", "xbcdx", "xabcdx",
		"abcabcabc", "abcdabcdabcd",
		"aabbccdd", // interleaved
	}

	for _, input := range testInputs {
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		matches, _ := q.MatchesForEvent([]byte(event))
		_, _ = matches, q // suppress unused
	}

	_, states, _, hits, misses := q.bufs.lazyDFAStats()
	hitRate := float64(hits) / float64(hits+misses) * 100
	t.Logf("6 overlapping patterns: %d states, %.1f%% hit rate, %d KB",
		states, hitRate, states*2096/1024)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestLazyDFATryToExplode tries hard to hit the state limit
func TestLazyDFATryToExplode(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add MANY overlapping patterns with wildcards
	// Each pattern adds to NFA complexity
	for i := 0; i < 20; i++ {
		// Patterns like *a*, *b*, *ab*, *ba*, *abc*, etc.
		chars := "abcdefghij"
		for j := 1; j <= 3; j++ {
			if i+j <= len(chars) {
				substr := chars[i : i+j]
				pattern := fmt.Sprintf(`{"f": [{"shellstyle": "*%s*"}]}`, substr)
				if err := q.AddPattern(fmt.Sprintf("p%d_%d", i, j), pattern); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	// Now match many diverse inputs
	for i := 0; i < 1000; i++ {
		// Create varied inputs with different character sequences
		input := fmt.Sprintf("%c%c%c%c%c%c%c%c%c%c",
			byte('a'+(i%10)), byte('a'+((i/10)%10)), byte('a'+((i/100)%10)),
			byte('a'+((i*3)%10)), byte('a'+((i*7)%10)), byte('a'+((i*11)%10)),
			byte('a'+((i*13)%10)), byte('a'+((i*17)%10)), byte('a'+((i*19)%10)),
			byte('a'+((i*23)%10)))
		event := fmt.Sprintf(`{"f": "%s"}`, input)
		_, _ = q.MatchesForEvent([]byte(event))

		if i%100 == 99 {
			_, states, _, hits, misses := q.bufs.lazyDFAStats()
			hitRate := float64(hits) / float64(hits+misses) * 100
			t.Logf("After %d inputs: %d states, %.1f%% hit rate", i+1, states, hitRate)

			if states >= maxLazyDFAStates {
				t.Logf("HIT STATE LIMIT!")
				break
			}
		}
	}

	_, finalStates, _, _, _ := q.bufs.lazyDFAStats()
	t.Logf("Final: %d states, %d KB", finalStates, finalStates*2096/1024)
}
