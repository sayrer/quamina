package quamina

import (
	"fmt"
	"testing"
)

// BenchmarkShellFastSinglePattern tests the fast path for a single shell-style pattern
func BenchmarkShellFastSinglePattern(b *testing.B) {
	q, _ := New()
	_ = q.AddPattern("p1", `{"f": [{"shellstyle": "*foo*bar*baz*"}]}`)
	event := []byte(`{"f": "prefix_foo_middle_bar_more_baz_suffix"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.MatchesForEvent(event)
	}
}

// BenchmarkShellFastSinglePatternLong tests fast path with long input
func BenchmarkShellFastSinglePatternLong(b *testing.B) {
	q, _ := New()
	_ = q.AddPattern("p1", `{"f": [{"shellstyle": "*needle*"}]}`)

	// Create a long value with needle near end
	value := make([]byte, 10000)
	for i := range value {
		value[i] = byte('a' + (i % 26))
	}
	copy(value[9990:], []byte("needle"))
	event := []byte(fmt.Sprintf(`{"f": "%s"}`, string(value)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.MatchesForEvent(event)
	}
}
