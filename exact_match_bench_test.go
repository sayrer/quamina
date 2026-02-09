package quamina

import (
	"fmt"
	"testing"
)

// BenchmarkExactMatch exercises the deterministic DFA path (traverseDFA)
// using only exact-match string patterns with no wildcards, shellstyle,
// or regexp operators.
func BenchmarkExactMatch(b *testing.B) {
	q, _ := New()

	// 20 exact-match patterns on the same field — keeps the automaton
	// deterministic so traverseDFA is used instead of traverseLazyDFA.
	streets := []string{
		"CRANLEIGH", "MONTGOMERY", "BROADWAY", "MARKET", "MISSION",
		"VALENCIA", "FOLSOM", "HOWARD", "HARRISON", "BRYANT",
		"BRANNAN", "TOWNSEND", "KING", "BERRY", "CHANNEL",
		"DIVISION", "ALAMEDA", "POTRERO", "MARIPOSA", "CESAR CHAVEZ",
	}
	for _, s := range streets {
		pattern := fmt.Sprintf(`{"STREET": ["%s"]}`, s)
		if err := q.AddPattern(s, pattern); err != nil {
			b.Fatal(err)
		}
	}

	// Events: cycle through matches and non-matches.
	events := make([][]byte, 0, len(streets)+5)
	for _, s := range streets {
		events = append(events, []byte(fmt.Sprintf(`{"STREET": "%s"}`, s)))
	}
	// Add some non-matching events with longer values.
	nonMatches := []string{"UNKNOWN", "EMBARCADERO", "VAN NESS", "GEARY", "OCEAN"}
	for _, s := range nonMatches {
		events = append(events, []byte(fmt.Sprintf(`{"STREET": "%s"}`, s)))
	}

	b.ResetTimer()
	b.ReportAllocs()

	var localMatches []X
	for i := 0; i < b.N; i++ {
		matches, err := q.MatchesForEvent(events[i%len(events)])
		if err != nil {
			b.Fatal(err)
		}
		localMatches = matches
	}
	topMatches = localMatches
}
