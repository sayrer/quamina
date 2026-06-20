package quamina

import (
	"fmt"
	"sort"
	"testing"
)

func TestLazyDFADifferentialWordle(t *testing.T) {
	words := readWWords(t, 300)

	q1, _ := New()
	q2, _ := New()
	for i, w := range words {
		pat := fmt.Sprintf(`{"x": [ {"shellstyle": "*%s*"} ] }`, string(w))
		id := fmt.Sprintf("p%d", i)
		if err := q1.AddPattern(id, pat); err != nil {
			t.Fatal(err)
		}
		if err := q2.AddPattern(id, pat); err != nil {
			t.Fatal(err)
		}
	}

	for _, w := range words {
		ev := []byte(fmt.Sprintf(`{"x": "%s"}`, string(w)))
		m1, err := q1.MatchesForEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		m2, err := q2.MatchesForEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		if !sameMatchSet(m1, m2) {
			t.Fatalf("matcher divergence on %q: %v vs %v", string(w), m1, m2)
		}
		// Every star-wrapped word must at least match its own pattern.
		if len(m1) == 0 {
			t.Fatalf("expected at least a self-match for %q", string(w))
		}
	}
}

func sameMatchSet(a, b []X) bool {
	if len(a) != len(b) {
		return false
	}
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i := range a {
		as[i] = fmt.Sprintf("%v", a[i])
		bs[i] = fmt.Sprintf("%v", b[i])
	}
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func TestLazyDFAvsNFADirect(t *testing.T) {
	words := readWWords(t, 300)
	q, _ := New()
	for i, w := range words {
		pat := fmt.Sprintf(`{"x": [ {"shellstyle": "*%s*"} ] }`, string(w))
		if err := q.AddPattern(fmt.Sprintf("p%d", i), pat); err != nil {
			t.Fatal(err)
		}
	}

	cm := q.matcher.(*coreMatcher)
	vm := cm.fields().state.fields().transitions["x"]
	start := vm.fields().start
	if start == nil {
		t.Fatal("expected a nondeterministic value automaton for field x")
	}

	bufs := newNfaBuffers()
	for _, w := range words {
		val := []byte(string(w))

		bufs.getTransmap().push()
		ld := bufs.getLazyDFA(start)
		lazy := append([]*fieldMatcher(nil), traverseLazyDFA(start, val, nil, ld, bufs)...)
		bufs.getTransmap().pop()

		bufs.getTransmap().push()
		nfa := append([]*fieldMatcher(nil), traverseNFA(start, val, nil, bufs)...)
		bufs.getTransmap().pop()

		if !sameFieldMatcherSet(lazy, nfa) {
			t.Fatalf("lazy vs NFA divergence on %q: %d vs %d transitions", string(w), len(lazy), len(nfa))
		}
	}
}

func sameFieldMatcherSet(a, b []*fieldMatcher) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[*fieldMatcher]int{}
	for _, fm := range a {
		seen[fm]++
	}
	for _, fm := range b {
		seen[fm]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
