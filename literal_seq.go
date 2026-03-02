package quamina

// singleByteTransition examines a smallTable and returns the single byte
// value and target state if and only if the table has exactly one non-nil
// transition covering exactly one byte value. Returns false if there are
// 0 or 2+ transitions, or if a transition covers a range of bytes.
func singleByteTransition(t *smallTable) (byte, *faState, bool) {
	var foundByte byte
	var foundState *faState
	found := false
	prev := byte(0)

	for i, ceiling := range t.ceilings {
		state := t.steps[i]
		rangeSize := int(ceiling) - int(prev)
		if state != nil {
			if rangeSize != 1 {
				// Covers multiple bytes — not a single-byte transition
				return 0, nil, false
			}
			if found {
				// Second non-nil transition
				return 0, nil, false
			}
			found = true
			foundByte = prev
			foundState = state
		}
		prev = ceiling
	}
	if !found {
		return 0, nil, false
	}
	return foundByte, foundState, true
}

// isCleanChainState reports whether a state is suitable as an interior node
// of a literal chain: not a spinner, no epsilons, no fieldTransitions.
func isCleanChainState(s *faState) bool {
	return !s.isSpinner &&
		len(s.table.epsilons) == 0 &&
		len(s.fieldTransitions) == 0
}
