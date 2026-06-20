package quamina

import (
	"fmt"
	"testing"
)

// buildVizDemo builds a small multi-shellstyle matcher on field "x" and returns
// an exporter for it.
func buildVizDemo(t *testing.T, words []string) *VizExporter {
	t.Helper()
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range words {
		pat := fmt.Sprintf(`{"x": [ {"shellstyle": "*%s*"} ] }`, w)
		if err := q.AddPattern(fmt.Sprintf("p%d", i), pat); err != nil {
			t.Fatal(err)
		}
	}
	e, err := NewVizExporter(q, "x")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestVizExportNFA(t *testing.T) {
	e := buildVizDemo(t, []string{"cat", "car", "dog"})
	g := e.NFA()
	if len(g.Nodes) == 0 || len(g.Edges) == 0 {
		t.Fatalf("expected a non-empty NFA graph, got %d nodes / %d edges", len(g.Nodes), len(g.Edges))
	}
	if g.Start != 0 {
		t.Fatalf("expected start node id 0, got %d", g.Start)
	}
	// Node ids must be dense 0..N-1 and every edge endpoint in range.
	for i, n := range g.Nodes {
		if n.ID != i {
			t.Fatalf("node %d has non-dense id %d", i, n.ID)
		}
	}
	hasByte, hasEps, hasAccept := false, false, false
	for _, ed := range g.Edges {
		if ed.From < 0 || ed.From >= len(g.Nodes) || ed.To < 0 || ed.To >= len(g.Nodes) {
			t.Fatalf("edge endpoint out of range: %+v", ed)
		}
		switch ed.Kind {
		case "byte":
			hasByte = true
		case "epsilon":
			hasEps = true
		default:
			t.Fatalf("unknown edge kind %q", ed.Kind)
		}
	}
	for _, n := range g.Nodes {
		if n.Accept {
			hasAccept = true
		}
	}
	if !hasByte {
		t.Error("expected at least one byte edge")
	}
	if !hasEps {
		t.Error("expected at least one epsilon edge (shellstyle splices)")
	}
	if !hasAccept {
		t.Error("expected at least one accepting node")
	}
}

func TestVizExportFeedMaterializesDFA(t *testing.T) {
	e := buildVizDemo(t, []string{"cat", "car", "dog"})

	// Before any feed, the cache is empty.
	f0, err := e.Feed([]byte(`{"x": "cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f0.Matches) == 0 {
		t.Fatal(`expected "cat" to match at least pattern p0`)
	}
	if len(f0.DFAStates) == 0 {
		t.Fatal("expected feeding an event to materialize lazy-DFA states")
	}
	if f0.Stats.States != len(f0.DFAStates) {
		t.Fatalf("stats.states=%d but exported %d DFA states", f0.Stats.States, len(f0.DFAStates))
	}
	// Exactly one DFA state should be flagged as the start.
	starts := 0
	for _, ds := range f0.DFAStates {
		if ds.Start {
			starts++
		}
		if len(ds.NFANodes) == 0 {
			t.Errorf("DFA state %d covers no NFA nodes", ds.ID)
		}
		for _, tr := range ds.Trans {
			if tr.To < 0 || tr.To >= len(f0.DFAStates) {
				t.Errorf("DFA transition target out of range: %+v", tr)
			}
		}
	}
	if starts != 1 {
		t.Errorf("expected exactly 1 start DFA state, got %d", starts)
	}

	// Feeding a second, different word should materialize MORE states (the
	// cache accumulates across feeds — the core of the demo).
	f1, err := e.Feed([]byte(`{"x": "dog"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f1.Stats.States < f0.Stats.States {
		t.Fatalf("cache shrank across feeds: %d -> %d", f0.Stats.States, f1.Stats.States)
	}
}
