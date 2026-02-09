package quamina

import (
	"fmt"
	"testing"
)

// BenchmarkEagerVsLazy compares the eager DFA path (traverseDFA on a
// pre-built DFA) against the lazy DFA path for nondeterministic patterns
// that fit within the eager budget.
//
// Sub-benchmarks:
//   - eager: normal path — hits dfaTable → traverseDFA
//   - lazy:  dfaTable cleared — falls back to traverseLazyDFA
func BenchmarkEagerVsLazy(b *testing.B) {
	// Use a handful of wildcard patterns: nondeterministic but small enough
	// to fit within the eager DFA budget.
	patterns := []string{
		`{"city": [{"shellstyle": "San*"}]}`,
		`{"city": [{"shellstyle": "*York*"}]}`,
		`{"city": [{"shellstyle": "*go"}]}`,
		`{"city": [{"shellstyle": "Los*"}]}`,
		`{"city": [{"shellstyle": "*land*"}]}`,
		`{"city": [{"shellstyle": "*ton"}]}`,
	}

	events := [][]byte{
		[]byte(`{"city": "San Francisco"}`),
		[]byte(`{"city": "New York City"}`),
		[]byte(`{"city": "Chicago"}`),
		[]byte(`{"city": "Los Angeles"}`),
		[]byte(`{"city": "Portland"}`),
		[]byte(`{"city": "Boston"}`),
		[]byte(`{"city": "Oakland"}`),
		[]byte(`{"city": "Houston"}`),
		[]byte(`{"city": "San Diego"}`),
		[]byte(`{"city": "Cleveland"}`),
	}

	// Build the matcher once.
	q, _ := New()
	for i, p := range patterns {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), p); err != nil {
			b.Fatal(err)
		}
	}

	// Sanity: verify we get matches.
	for _, ev := range events {
		m, err := q.MatchesForEvent(ev)
		if err != nil {
			b.Fatal(err)
		}
		if len(m) == 0 {
			b.Fatalf("no match for %s", ev)
		}
	}

	// Locate the valueMatcher for "city" so we can toggle dfaTable.
	cm := q.matcher.(*coreMatcher)
	fm := cm.fields().state
	fmf := fm.updateable.Load()
	vm := fmf.transitions["city"]
	if vm == nil {
		b.Fatal("could not find valueMatcher for 'city'")
	}

	// Confirm dfaTable is populated (eager succeeded).
	if vm.fields().dfaTable == nil {
		b.Fatal("eager DFA was not built — benchmark is invalid")
	}

	b.Run("eager", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, ev := range events {
				_, _ = q.MatchesForEvent(ev)
			}
		}
	})

	b.Run("lazy", func(b *testing.B) {
		// Temporarily clear dfaTable to force lazy DFA path.
		fields := vm.getFieldsForUpdate()
		savedDFA := fields.dfaTable
		fields.dfaTable = nil
		vm.update(fields)
		defer func() {
			fields := vm.getFieldsForUpdate()
			fields.dfaTable = savedDFA
			vm.update(fields)
		}()

		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, ev := range events {
				_, _ = q.MatchesForEvent(ev)
			}
		}
	})
}
