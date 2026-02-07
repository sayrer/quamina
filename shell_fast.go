package quamina

import (
	"bytes"
)

// shellFastMatcher is a specialized matcher for pure shell-style patterns
// of the form *lit1*lit2*...*litN* (only * wildcards and literal segments).
// It uses bytes.Index for fast substring matching, bypassing the NFA machinery.
//
// For patterns like *foo*bar*baz*, this finds each literal in order,
// which is O(n) where n is input length, using SIMD-optimized string search.
type shellFastMatcher struct {
	pattern     []byte   // original pattern (with quotes) for NFA migration
	literals    [][]byte // literal segments between wildcards
	startsWild  bool     // pattern starts with *
	endsWild    bool     // pattern ends with *
	nextField   *fieldMatcher
	totalLitLen int // sum of literal lengths for early rejection
}

// newShellFastMatcher creates a fast matcher for a shell-style pattern.
// Returns nil if the pattern contains features beyond simple * wildcards
// (like character classes, escapes, etc.).
func newShellFastMatcher(patternWithQuotes []byte, nextField *fieldMatcher) *shellFastMatcher {
	// Pattern comes with quotes, strip them
	if len(patternWithQuotes) < 2 || patternWithQuotes[0] != '"' || patternWithQuotes[len(patternWithQuotes)-1] != '"' {
		return nil
	}
	pattern := patternWithQuotes[1 : len(patternWithQuotes)-1]

	if len(pattern) == 0 {
		return nil
	}

	// Check for unsupported features (?, [, \, etc.)
	for _, b := range pattern {
		if b == '?' || b == '[' || b == '\\' {
			return nil
		}
	}

	sfm := &shellFastMatcher{
		pattern:    patternWithQuotes, // keep original for NFA migration
		startsWild: pattern[0] == '*',
		endsWild:   pattern[len(pattern)-1] == '*',
		nextField:  nextField,
	}

	// Split on * to get literals
	start := 0
	for i := 0; i <= len(pattern); i++ {
		if i == len(pattern) || pattern[i] == '*' {
			if i > start {
				lit := pattern[start:i]
				sfm.literals = append(sfm.literals, lit)
				sfm.totalLitLen += len(lit)
			}
			start = i + 1
		}
	}

	// Pattern with only wildcards (like "***") - matches anything
	if len(sfm.literals) == 0 {
		sfm.startsWild = true
		sfm.endsWild = true
	}

	return sfm
}

// match checks if the input matches the pattern.
// Input should be the raw value without quotes.
func (sfm *shellFastMatcher) match(input []byte) bool {
	if sfm == nil {
		return false
	}

	// Quick length check: input must be at least as long as all literals combined
	if len(input) < sfm.totalLitLen {
		return false
	}

	// No literals = match anything (pattern was all wildcards)
	if len(sfm.literals) == 0 {
		return true
	}

	literals := sfm.literals
	pos := 0
	startIdx := 0

	// First literal: if pattern doesn't start with *, must match at start
	if !sfm.startsWild {
		first := literals[0]
		if !bytes.HasPrefix(input, first) {
			return false
		}
		pos = len(first)
		startIdx = 1
		if len(literals) == 1 {
			// Only one literal, check end condition
			if sfm.endsWild {
				return true
			}
			return len(input) == len(first)
		}
	}

	// Middle literals: find each in order
	endIdx := len(literals) - 1
	for i := startIdx; i < endIdx; i++ {
		lit := literals[i]
		idx := bytes.Index(input[pos:], lit)
		if idx < 0 {
			return false
		}
		pos += idx + len(lit)
	}

	// Last literal: if pattern doesn't end with *, must match at end
	last := literals[len(literals)-1]
	if sfm.endsWild {
		idx := bytes.Index(input[pos:], last)
		return idx >= 0
	}

	// Must end with the last literal
	return bytes.HasSuffix(input[pos:], last)
}

// matchShellFast is a standalone function for quick testing.
// Pattern format: *lit1*lit2*lit3* (with or without leading/trailing *)
func matchShellFast(pattern, input []byte) bool {
	sfm := newShellFastMatcher(pattern, nil)
	return sfm.match(input)
}
