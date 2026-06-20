package quamina

import (
	"fmt"
	"sort"
)

// viz_export.go exposes a read-only view of a value-matcher's NFA and its
// lazy-DFA cache, for the NFA/DFA visualization demo (see visualization/).
// It is intentionally a thin, demo-only surface: it reaches into the
// unexported automaton from inside the package and emits JSON-serializable
// structs. It is NOT part of Quamina's supported API.

// VizNode is one NFA state in the exported graph.
type VizNode struct {
	ID          int  `json:"id"`
	Accept      bool `json:"accept"`      // has field transitions (a value match lands here)
	EpsilonOnly bool `json:"epsilonOnly"` // pure splice/spinner state (no byte transitions)
}

// VizEdge is one transition in the exported NFA graph.
type VizEdge struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Kind  string `json:"kind"`  // "byte" or "epsilon"
	Label string `json:"label"` // byte char/range for "byte"; "ε" for "epsilon"
}

// VizNFA is the whole NFA graph for one field's value automaton.
type VizNFA struct {
	Start int       `json:"start"`
	Nodes []VizNode `json:"nodes"`
	Edges []VizEdge `json:"edges"`
}

// VizDFATrans is one materialized (cached) lazy-DFA transition.
type VizDFATrans struct {
	Byte  int    `json:"byte"`
	Label string `json:"label"`
	To    int    `json:"to"` // target DFA-state id
}

// VizDFAState is one materialized lazy-DFA state: a set of NFA nodes.
type VizDFAState struct {
	ID       int           `json:"id"`
	Start    bool          `json:"start"`
	NFANodes []int         `json:"nfaNodes"` // the NFA node ids this DFA state represents
	Trans    []VizDFATrans `json:"trans"`
}

// VizFeed is the result of feeding one event: the matches plus a snapshot of
// every lazy-DFA state materialized so far (the cache accumulates across feeds).
type VizFeed struct {
	Event     string        `json:"event"`
	Matches   []string      `json:"matches"`
	DFAStates []VizDFAState `json:"dfaStates"`
	// Stats mirrors lazyDFA.stats(): states, creates, hits, misses, cacheBytes.
	Stats struct {
		States    int `json:"states"`
		Creates   int `json:"creates"`
		Hits      int `json:"hits"`
		Misses    int `json:"misses"`
		CacheByte int `json:"cacheBytes"`
	} `json:"stats"`
}

// VizExporter holds a single-field value automaton and a stable NFA-node id
// assignment, and produces the NFA graph and per-feed lazy-DFA snapshots.
type VizExporter struct {
	q     *Quamina
	field string
	start *faState
	ids   map[*faState]int // NFA node id assignment (stable for this exporter)
	nodes []*faState       // id -> faState
}

// NewVizExporter digs the named field's value automaton out of q and prepares
// a viz exporter. q must be a default (coreMatcher) Quamina with the field
// present as a nondeterministic value automaton (e.g. shellstyle patterns).
// It forces the lazy-DFA path (clears any eager DFA) so that matching always
// materializes the lazy cache the visualization renders.
func NewVizExporter(q *Quamina, field string) (*VizExporter, error) {
	cm, ok := q.matcher.(*coreMatcher)
	if !ok {
		return nil, fmt.Errorf("viz: matcher is not a *coreMatcher")
	}
	vm := cm.fields().state.fields().transitions[field]
	if vm == nil {
		return nil, fmt.Errorf("viz: no value matcher for field %q", field)
	}

	// Force the lazy path: clear any eagerly-built DFA so transitionOn always
	// uses traverseLazyDFA and populates the cache we visualize.
	vf := vm.getFieldsForUpdate()
	vf.dfaStart = nil
	vf.eagerDFAFailed = true
	vm.update(vf)

	start := vm.fields().start
	if start == nil {
		return nil, fmt.Errorf("viz: field %q has no NFA (singleton/empty)", field)
	}

	e := &VizExporter{q: q, field: field, start: start, ids: map[*faState]int{}}
	e.assignIDs()
	return e, nil
}

// id returns the stable NFA-node id for s, assigning one on first sight.
func (e *VizExporter) id(s *faState) int {
	if n, ok := e.ids[s]; ok {
		return n
	}
	n := len(e.nodes)
	e.ids[s] = n
	e.nodes = append(e.nodes, s)
	return n
}

// assignIDs walks the reachable NFA (byte steps + epsilons) breadth-first from
// start, assigning ids in discovery order so the start state is id 0.
func (e *VizExporter) assignIDs() {
	queue := []*faState{e.start}
	e.id(e.start)
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, next := range unpackTable(&s.table) {
			if next != nil {
				if _, seen := e.ids[next]; !seen {
					e.id(next)
					queue = append(queue, next)
				}
			}
		}
		for _, eps := range s.table.epsilons {
			if _, seen := e.ids[eps]; !seen {
				e.id(eps)
				queue = append(queue, eps)
			}
		}
	}
}

// NFA returns the full NFA graph, coalescing runs of bytes that lead to the
// same next state into a single ranged edge.
func (e *VizExporter) NFA() VizNFA {
	g := VizNFA{Start: e.ids[e.start]}
	for id, s := range e.nodes {
		g.Nodes = append(g.Nodes, VizNode{
			ID:          id,
			Accept:      len(s.fieldTransitions) > 0,
			EpsilonOnly: s.table.isEpsilonOnly(),
		})
		// Byte transitions: coalesce contiguous runs with the same target.
		unpacked := unpackTable(&s.table)
		b := 0
		for b < byteCeiling {
			next := unpacked[b]
			if next == nil {
				b++
				continue
			}
			runStart := b
			for b < byteCeiling && unpacked[b] == next {
				b++
			}
			g.Edges = append(g.Edges, VizEdge{
				From:  id,
				To:    e.ids[next],
				Kind:  "byte",
				Label: byteRangeLabel(byte(runStart), byte(b-1)),
			})
		}
		// Epsilon transitions.
		for _, eps := range s.table.epsilons {
			g.Edges = append(g.Edges, VizEdge{
				From: id, To: e.ids[eps], Kind: "epsilon", Label: "ε",
			})
		}
	}
	return g
}

// Feed matches one event, then snapshots every lazy-DFA state materialized so
// far in the per-Quamina cache for this field's start state. The cache
// accumulates across calls, so successive feeds light up more of the NFA.
func (e *VizExporter) Feed(event []byte) (VizFeed, error) {
	out := VizFeed{Event: string(event)}
	matches, err := e.q.MatchesForEvent(event)
	if err != nil {
		return out, err
	}
	for _, m := range matches {
		out.Matches = append(out.Matches, fmt.Sprintf("%v", m))
	}

	ld := e.q.bufs.lazyDFACaches[e.start]
	if ld == nil {
		return out, nil // nothing materialized (e.g. event didn't reach this field)
	}
	states, creates, hits, misses, cacheBytes := ld.stats()
	out.Stats.States, out.Stats.Creates = states, creates
	out.Stats.Hits, out.Stats.Misses, out.Stats.CacheByte = hits, misses, cacheBytes

	// Assign DFA-state ids. Iterate the cache in a deterministic order (sorted
	// by the smallest NFA-node id each DFA state covers) so ids are stable
	// enough for rendering.
	dfaStates := make([]*lazyDFAState, 0, len(ld.cache))
	for _, ds := range ld.cache {
		dfaStates = append(dfaStates, ds)
	}
	sort.Slice(dfaStates, func(i, j int) bool {
		return e.minNFAID(dfaStates[i]) < e.minNFAID(dfaStates[j])
	})
	dfaID := make(map[*lazyDFAState]int, len(dfaStates))
	for i, ds := range dfaStates {
		dfaID[ds] = i
	}

	for i, ds := range dfaStates {
		vs := VizDFAState{ID: i, Start: ds == ld.startState}
		for _, ns := range ds.nfaStates {
			vs.NFANodes = append(vs.NFANodes, e.id(ns))
		}
		sort.Ints(vs.NFANodes)
		for k, b := range ds.transKeys {
			to, ok := dfaID[ds.transValues[k]]
			if !ok {
				continue // target not in cache (shouldn't happen for cached states)
			}
			vs.Trans = append(vs.Trans, VizDFATrans{
				Byte: int(b), Label: byteLabel(b), To: to,
			})
		}
		out.DFAStates = append(out.DFAStates, vs)
	}
	return out, nil
}

// minNFAID returns the smallest NFA-node id covered by a DFA state (for stable
// ordering); returns a large sentinel for an empty set.
func (e *VizExporter) minNFAID(ds *lazyDFAState) int {
	min := 1 << 30
	for _, ns := range ds.nfaStates {
		if id := e.id(ns); id < min {
			min = id
		}
	}
	return min
}

// byteLabel renders a single transition byte for display.
func byteLabel(b byte) string {
	switch {
	case b == valueTerminator:
		return "∎" // value-terminator (end of value)
	case b >= 0x20 && b < 0x7f:
		return string(rune(b))
	default:
		return fmt.Sprintf("0x%02x", b)
	}
}

// byteRangeLabel renders a (possibly single) byte range for display.
func byteRangeLabel(lo, hi byte) string {
	if lo == hi {
		return byteLabel(lo)
	}
	return byteLabel(lo) + "–" + byteLabel(hi)
}
