package quamina

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// TestArrayBehavior is here prove that (a) you can index a map with an array and
// the indexing actually relies on the values in the array. This has nothing to do with
// Quamina, but I'm leaving it here because I had to write this stupid test after failing
// to find a straightforward question of whether this works as expected anywhere in the
// Golang docs.
func TestArrayBehavior(t *testing.T) {
	type gpig [4]int
	pigs := []gpig{
		{1, 2, 3, 4},
		{4, 3, 2, 1},
	}
	nonPigs := []gpig{
		{3, 4, 3, 4},
		{99, 88, 77, 66},
	}
	m := make(map[gpig]bool)
	for _, pig := range pigs {
		m[pig] = true
	}
	for _, pig := range pigs {
		_, ok := m[pig]
		if !ok {
			t.Error("missed pig")
		}
	}
	pigs[0][0] = 111
	pigs[1][3] = 777
	pigs = append(pigs, nonPigs...)
	for _, pig := range pigs {
		_, ok := m[pig]
		if ok {
			t.Error("mutant pig")
		}
	}
	newPig := gpig{1, 2, 3, 4}
	_, ok := m[newPig]
	if !ok {
		t.Error("Newpig")
	}
}

func TestFocusedMerge(t *testing.T) {
	shellStyles := []string{
		"a*b",
		"ab*",
		"*ab",
	}
	var automata []*smallTable
	var matchers []*fieldMatcher

	for _, shellStyle := range shellStyles {
		str := `"` + shellStyle + `"`
		automaton, matcher := makeShellStyleFA([]byte(str), &nullPrinter{})
		automata = append(automata, automaton)
		matchers = append(matchers, matcher)
	}

	var cab uintptr
	for _, mm := range matchers {
		uu := uintptr(unsafe.Pointer(mm))
		cab = cab ^ uu
	}

	merged := newSmallTable()
	for _, automaton := range automata {
		merged = mergeFAs(merged, automaton, sharedNullPrinter)

		s := statsAccum{
			fmVisited: make(map[*fieldMatcher]bool),
			vmVisited: make(map[*valueMatcher]bool),
			stVisited: make(map[*smallTable]bool),
		}
		faStats(merged, &s)
		fmt.Println(s.stStats())
	}
}

func TestNfa2Dfa(t *testing.T) {
	type n2dtest struct {
		pattern string
		shoulds []string
		nopes   []string
	}
	tests := []n2dtest{
		{
			pattern: "*abc",
			shoulds: []string{"abc", "fooabc", "abcabc"},
			nopes:   []string{"abd", "fooac"},
		},
		{
			pattern: "a*bc",
			shoulds: []string{"abc", "axybc", "abcbc"},
			nopes:   []string{"abd", "fooac"},
		},
		{
			pattern: "ab*c",
			shoulds: []string{"abc", "abxyxc", "abbbbbc"},
			nopes:   []string{"abd", "abcxy"},
		},
		{
			pattern: "abc*",
			shoulds: []string{"abc", "abcfoo"},
			nopes:   []string{"xabc", "abxbar"},
		},
	}
	pp := newPrettyPrinter(4567)
	transitions := []*fieldMatcher{}
	bufs := newNfaBuffers()
	for _, test := range tests {
		nfa, _ := makeShellStyleFA(asQuotedBytes(t, test.pattern), pp)
		//fmt.Println("NFA: " + pp.printNFA(nfa))

		for _, should := range test.shoulds {
			matched := testTraverseNFA(nfa, asQuotedBytes(t, should), transitions, bufs)
			if len(matched) != 1 {
				t.Errorf("NFA %s didn't %s: ", test.pattern, should)
			}
		}
		for _, nope := range test.nopes {
			matched := testTraverseNFA(nfa, asQuotedBytes(t, nope), transitions, bufs)
			if len(matched) != 0 {
				t.Errorf("NFA %s matched %s", test.pattern, nope)
			}
		}
		dfa := nfa2Dfa(nfa)
		// fmt.Println("DFA: " + pp.printNFA(dfa.table))
		for _, should := range test.shoulds {
			matched := traverseDFA(dfa.table, asQuotedBytes(t, should), transitions)
			if len(matched) != 1 {
				t.Errorf("DFA %s didn't match %s ", test.pattern, should)
			}
		}
		for _, nope := range test.nopes {
			matched := traverseDFA(dfa.table, asQuotedBytes(t, nope), transitions)
			if len(matched) != 0 {
				t.Errorf("DFA %s matched %s", test.pattern, nope)
			}
		}
	}
}
func asQuotedBytes(t *testing.T, s string) []byte {
	t.Helper()
	s = `"` + s + `"`
	return []byte(s)
}

// testTraverseNFA wraps traverseNFA with the push/pop that tryToMatch
// normally provides. Test-only convenience so direct callers don't need
// to manage the transmap stack themselves.
func testTraverseNFA(table *smallTable, val []byte, transitions []*fieldMatcher, bufs *nfaBuffers) []*fieldMatcher {
	tm := bufs.getTransmap()
	tm.push()
	result := traverseNFA(table, val, transitions, bufs)
	tm.pop()
	return result
}

// TestNestedTransmapSafety verifies that the transmap handles nested traverseNFA calls correctly.
// The bug scenario: tryToMatch iterates over fieldMatchers returned from transitionOn (which uses
// the transmap buffer). During iteration, recursive tryToMatch calls transitionOn again, which
// would clobber the buffer if not handled properly. The stack-based transmap prevents this.
func TestNestedTransmapSafety(t *testing.T) {
	// Create patterns with shellstyle on multiple fields to force NFA mode and nested calls.
	// Field "a" comes before "b" lexically, so tryToMatch processes "a" first, then recurses for "b".
	patterns := []string{
		`{"a": [{"shellstyle": "foo*"}], "b": [{"shellstyle": "bar*"}]}`,
		`{"a": [{"shellstyle": "foo*"}], "b": [{"shellstyle": "baz*"}]}`,
		`{"a": [{"shellstyle": "fox*"}], "b": [{"shellstyle": "bar*"}]}`,
	}

	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	for i, p := range patterns {
		err = q.AddPattern(fmt.Sprintf("P%d", i), p)
		if err != nil {
			t.Fatalf("AddPattern %d: %v", i, err)
		}
	}

	// Events that match different combinations
	tests := []struct {
		event   string
		matches []string
	}{
		// Matches P0: a=foo*, b=bar*
		{`{"a": "fooXYZ", "b": "barXYZ"}`, []string{"P0"}},
		// Matches P1: a=foo*, b=baz*
		{`{"a": "fooABC", "b": "bazABC"}`, []string{"P1"}},
		// Matches P2: a=fox*, b=bar*
		{`{"a": "foxDEF", "b": "barDEF"}`, []string{"P2"}},
		// Matches P0 and P1: a=foo*, b matches both bar* and baz*
		{`{"a": "fooXYZ", "b": "bar"}`, []string{"P0"}},
		{`{"a": "fooXYZ", "b": "baz"}`, []string{"P1"}},
		// No match
		{`{"a": "nomatch", "b": "nomatch"}`, []string{}},
	}

	for _, tc := range tests {
		matches, err := q.MatchesForEvent([]byte(tc.event))
		if err != nil {
			t.Errorf("MatchesForEvent(%s): %v", tc.event, err)
			continue
		}

		if len(matches) != len(tc.matches) {
			t.Errorf("Event %s: got %d matches %v, want %d matches %v",
				tc.event, len(matches), matches, len(tc.matches), tc.matches)
			continue
		}

		// Verify expected matches
		matchSet := make(map[string]bool)
		for _, m := range matches {
			matchSet[m.(string)] = true
		}
		for _, want := range tc.matches {
			if !matchSet[want] {
				t.Errorf("Event %s: missing expected match %s, got %v", tc.event, want, matches)
			}
		}
	}
}

// TestTransmapBufferReuse directly tests that the transmap buffer reuse is safe.
// With a buggy single-buffer implementation, nested push/pop calls corrupt the outer buffer.
func TestTransmapBufferReuse(t *testing.T) {
	// Create dummy fieldMatchers for testing
	fm1 := &fieldMatcher{}
	fm2 := &fieldMatcher{}
	fm3 := &fieldMatcher{}

	tm := newTransMap()

	// Simulate start of matchesForFields
	tm.resetDepth()

	// Simulate outer traverseNFA call
	tm.push()
	tm.add([]*fieldMatcher{fm1, fm2})

	// Get outer result - this returns a buffer backed by level 0
	outerResult := tm.pop()

	// Verify outer result before inner call
	if len(outerResult) != 2 {
		t.Fatalf("outer result before inner: got %d, want 2", len(outerResult))
	}

	// Remember which fieldMatchers we expect
	expectFM1 := outerResult[0] == fm1 || outerResult[1] == fm1
	expectFM2 := outerResult[0] == fm2 || outerResult[1] == fm2
	if !expectFM1 || !expectFM2 {
		t.Fatalf("outer result should have fm1 and fm2")
	}

	// Simulate inner traverseNFA call (would happen during iteration in tryToMatch).
	// push() goes to level 1, so level 0's buffer (outerResult) is safe.
	tm.push()
	tm.add([]*fieldMatcher{fm3})
	innerResult := tm.pop()

	// Inner should have fm3
	if len(innerResult) != 1 || innerResult[0] != fm3 {
		t.Errorf("inner result: got %v, want [fm3]", innerResult)
	}

	// Verify outerResult wasn't corrupted by the inner push/pop.
	foundFM1 := false
	foundFM2 := false
	for _, fm := range outerResult {
		if fm == fm1 {
			foundFM1 = true
		}
		if fm == fm2 {
			foundFM2 = true
		}
	}

	if !foundFM1 || !foundFM2 {
		t.Errorf("outer result was corrupted after inner call: expected fm1 and fm2, got %v", outerResult)
	}
}

// TestTransmapHashPromotion exercises the adaptive hash set that activates
// when a transmap level exceeds transmapLinearMax entries.
func TestTransmapHashPromotion(t *testing.T) {
	tm := newTransMap()
	tm.resetDepth()
	tm.push()

	// Create more field matchers than the linear threshold.
	n := transmapLinearMax * 4
	fms := make([]*fieldMatcher, n)
	for i := range fms {
		fms[i] = &fieldMatcher{}
	}

	// Add all field matchers.
	tm.add(fms)

	// Add them all again — should be deduplicated.
	tm.add(fms)

	result := tm.pop()
	if len(result) != n {
		t.Fatalf("expected %d unique field matchers, got %d", n, len(result))
	}

	// Verify all original matchers are present.
	seen := make(map[*fieldMatcher]bool)
	for _, fm := range result {
		if seen[fm] {
			t.Errorf("duplicate field matcher in result")
		}
		seen[fm] = true
	}
	for i, fm := range fms {
		if !seen[fm] {
			t.Errorf("field matcher %d missing from result", i)
		}
	}
}

// collectClosureStats walks an NFA and reports epsilon closure size statistics.
func collectClosureStats(startTable *smallTable) (stateCount, totalEntries, maxClosure int, tableSharing int) {
	visitedTables := make(map[*smallTable]bool)
	visitedStates := make(map[*faState]bool)
	tableCounts := make(map[*smallTable]int)

	var walkTable func(t *smallTable)
	walkTable = func(t *smallTable) {
		if t == nil || visitedTables[t] {
			return
		}
		visitedTables[t] = true
		for _, state := range t.steps {
			if state != nil && !visitedStates[state] {
				visitedStates[state] = true
				tableCounts[state.table]++
				ec := len(state.epsilonClosure)
				totalEntries += ec
				if ec > maxClosure {
					maxClosure = ec
				}
				walkTable(state.table)
			}
		}
		for _, eps := range t.epsilons {
			if !visitedStates[eps] {
				visitedStates[eps] = true
				tableCounts[eps.table]++
				ec := len(eps.epsilonClosure)
				totalEntries += ec
				if ec > maxClosure {
					maxClosure = ec
				}
				walkTable(eps.table)
			}
		}
	}
	walkTable(startTable)

	for _, count := range tableCounts {
		if count > 1 {
			tableSharing += count - 1
		}
	}
	return len(visitedStates), totalEntries, maxClosure, tableSharing
}

// dedupWorkload defines a set of patterns for testing table-pointer dedup.
type dedupWorkload struct {
	name         string
	patterns     []string // shellstyle patterns
	regexps      []string // regexp patterns
	stateCount   int      // expected NFA state count
	totalEntries int      // expected total epsilon closure entries
	maxMax       int      // max closure must not exceed this
	tableSharing int      // expected count of states sharing a smallTable
	matches      []int    // expected match counts for the 3 standard events
}

var dedupWorkloads = []dedupWorkload{
	{
		name: "6-regexps-12-shell",
		patterns: []string{
			"*a*b*c*", "*x*y*z*", "*e*f*g*", "*m*n*o*",
			"*p*q*r*", "*s*t*u*", "*a*e*i*", "*b*d*f*",
			"*c*g*k*", "*d*h*l*", "*i*o*u*", "*r*s*t*",
		},
		regexps: []string{
			"(([abc]?)*)+", "([abc]+)*d", "(a*)*b",
			"([xyz]?)*end", "(([mno]?)*)+", "([pqr]+)*s",
		},
		stateCount:   1101,
		totalEntries: 4371,
		maxMax:       20,
		tableSharing: 11,
		matches:      []int{3, 2, 7},
	},
	{
		name: "20-nested-regexps",
		regexps: []string{
			"(([abc]?)*)+", "([abc]+)*d", "(a*)*b", "([xyz]?)*end",
			"(([mno]?)*)+", "([pqr]+)*s", "(([def]?)*)+", "([ghi]+)*j",
			"(([stu]?)*)+", "([vwx]+)*y", "(b*)*c", "(d*)*e",
			"(([fg]?)*)+", "([hi]+)*k", "(([jk]?)*)+", "([lm]+)*n",
			"(([op]?)*)+", "([qr]+)*t", "(e*)*f", "(g*)*h",
		},
		stateCount:   149,
		totalEntries: 261,
		maxMax:       50,
		tableSharing: 39,
		matches:      []int{0, 0, 0},
	},
	{
		name: "deeply-nested",
		regexps: []string{
			"(((a?)*b?)*c?)*",
			"(((x?)*y?)*z?)*",
			"(((d?)*e?)*f?)*",
			"(((m?)*n?)*o?)*",
			"((((a?)*b?)*c?)*d?)*",
			"((((x?)*y?)*z?)*w?)*",
		},
		stateCount:   59,
		totalEntries: 220,
		maxMax:       35,
		tableSharing: 20,
		matches:      []int{0, 0, 0},
	},
	{
		name: "overlapping-char-classes",
		regexps: []string{
			"(([abc]?)*)+", "(([bcd]?)*)+", "(([cde]?)*)+",
			"(([def]?)*)+", "(([efg]?)*)+", "(([fgh]?)*)+",
			"(([ghi]?)*)+", "(([hij]?)*)+", "(([ijk]?)*)+",
			"(([jkl]?)*)+", "(([klm]?)*)+", "(([lmn]?)*)+",
		},
		stateCount:   85,
		totalEntries: 156,
		maxMax:       30,
		tableSharing: 24,
		matches:      []int{0, 0, 0},
	},
	{
		name: "shell+deep-overlap",
		patterns: []string{
			"*a*b*", "*b*c*", "*c*d*", "*d*e*", "*e*f*",
			"*a*c*", "*b*d*", "*c*e*", "*d*f*", "*a*d*",
		},
		regexps: []string{
			"(((a?)*b?)*c?)*", "(((b?)*c?)*d?)*", "(((c?)*d?)*e?)*",
			"(((d?)*e?)*f?)*", "(([abcd]?)*)+", "(([cdef]?)*)+",
		},
		stateCount:   837,
		totalEntries: 3410,
		maxMax:       30,
		tableSharing: 16,
		matches:      []int{10, 10, 10},
	},
}

func dedupEvents() [][]byte {
	return [][]byte{
		[]byte(`{"val": "abcdefgh"}`),
		[]byte(`{"val": "` + strings.Repeat("abcdef", 5) + `"}`),
		[]byte(`{"val": "` + strings.Repeat("abcdefghijklmnop", 3) + `"}`),
	}
}

func buildDedupMatcher(tb testing.TB, wl dedupWorkload) *Quamina {
	tb.Helper()
	q, _ := New()
	i := 0
	for _, ss := range wl.patterns {
		pattern := fmt.Sprintf(`{"val": [{"shellstyle": "%s"}]}`, ss)
		if err := q.AddPattern(fmt.Sprintf("s%d", i), pattern); err != nil {
			tb.Fatal(err)
		}
		i++
	}
	for _, re := range wl.regexps {
		pattern := fmt.Sprintf(`{"val": [{"regexp": "%s"}]}`, re)
		if err := q.AddPattern(fmt.Sprintf("r%d", i), pattern); err != nil {
			tb.Fatal(err)
		}
		i++
	}
	return q
}

// TestTablePointerDedup verifies that table-pointer dedup keeps epsilon
// closures bounded and that matching produces correct results for workloads
// with nested quantifier regexps and overlapping character classes.
func TestTablePointerDedup(t *testing.T) {
	events := dedupEvents()

	for _, wl := range dedupWorkloads {
		t.Run(wl.name, func(t *testing.T) {
			q := buildDedupMatcher(t, wl)
			m := q.matcher.(*coreMatcher)

			vm := m.fields().state.fields().transitions["val"]
			nfaStart := vm.fields().startTable
			stateCount, totalEntries, maxClosure, tableSharing := collectClosureStats(nfaStart)

			if stateCount != wl.stateCount {
				t.Errorf("stateCount = %d, want %d", stateCount, wl.stateCount)
			}
			if totalEntries != wl.totalEntries {
				t.Errorf("totalEntries = %d, want %d", totalEntries, wl.totalEntries)
			}
			if maxClosure > wl.maxMax {
				t.Errorf("max closure %d exceeds bound %d", maxClosure, wl.maxMax)
			}
			if tableSharing != wl.tableSharing {
				t.Errorf("tableSharing = %d, want %d", tableSharing, wl.tableSharing)
			}

			for ei, event := range events {
				matches, err := q.MatchesForEvent(event)
				if err != nil {
					t.Fatalf("event %d: %v", ei, err)
				}
				if len(matches) != wl.matches[ei] {
					t.Errorf("event %d: got %d matches, want %d", ei, len(matches), wl.matches[ei])
				}
			}
		})
	}
}
