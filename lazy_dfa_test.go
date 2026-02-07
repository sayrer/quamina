package quamina

import (
	"fmt"
	"testing"
)

func TestLazyDFACacheLimit(t *testing.T) {
	// Create a pattern that could cause state explosion
	// Multiple wildcards create many possible state combinations
	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add a complex wildcard pattern
	pattern := `{"field": [{"shellstyle": "*a*b*c*d*"}]}`
	if err := q.AddPattern("p1", pattern); err != nil {
		t.Fatal(err)
	}

	// Match against various inputs - this exercises different paths through the DFA
	inputs := []string{
		"abcd",
		"XaXbXcXdX",
		"aaabbbcccddd",
		"abcdabcdabcd",
		"xyzabcdefghi",
	}

	for _, input := range inputs {
		event := fmt.Sprintf(`{"field": "%s"}`, input)
		matches, err := q.MatchesForEvent([]byte(event))
		if err != nil {
			t.Errorf("MatchesForEvent(%s): %v", event, err)
		}
		// Just verify no crashes - the pattern should match all inputs
		if len(matches) != 1 {
			t.Errorf("Expected 1 match for %s, got %d", input, len(matches))
		}
	}
}

func TestLazyDFAFallbackToNFA(t *testing.T) {
	// Test that we gracefully fall back to NFA when cache limit is hit
	// This is hard to test directly without exposing internals,
	// but we can at least verify correctness with complex patterns

	q, err := New()
	if err != nil {
		t.Fatal(err)
	}

	// Add multiple complex patterns
	patterns := []string{
		`{"f": [{"shellstyle": "*x*y*z*"}]}`,
		`{"f": [{"shellstyle": "*a*b*c*"}]}`,
		`{"f": [{"shellstyle": "*1*2*3*"}]}`,
	}
	for i, p := range patterns {
		if err := q.AddPattern(fmt.Sprintf("p%d", i), p); err != nil {
			t.Fatal(err)
		}
	}

	// Test various inputs
	tests := []struct {
		input   string
		matches int
	}{
		{"xyz", 1},
		{"abc", 1},
		{"123", 1},
		{"xyzabc123", 3}, // matches all three
		{"nomatch", 0},
	}

	for _, tc := range tests {
		event := fmt.Sprintf(`{"f": "%s"}`, tc.input)
		matches, err := q.MatchesForEvent([]byte(event))
		if err != nil {
			t.Errorf("MatchesForEvent(%s): %v", event, err)
			continue
		}
		if len(matches) != tc.matches {
			t.Errorf("Input %s: expected %d matches, got %d: %v",
				tc.input, tc.matches, len(matches), matches)
		}
	}
}
