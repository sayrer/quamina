# Lazy DFA Port — Phase 1 (Lazy DFA Core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the lazy (on-demand) DFA construction core from the `lazy_dfa` reference branch onto current `main`, so nondeterministic value matchers match via a per-goroutine lazy DFA cache instead of `traverseNFA`, with identical match results.

**Architecture:** A `lazyDFAState` is a set of NFA `*faState`s (subset construction). A per-goroutine `lazyDFA` cache (keyed by the start `*faState`, 8 MB budget) memoizes `(state,byte)→state` transitions, materialized on demand during traversal. We adapt the reference implementation to main's embed-smalltable architecture: `smallTable.step(b) *faState`, the empty-closure self-only sentinel, the start state as a `*faState`, and main's `fieldSet`+`transmap.levels[depth]` transition-collection model.

**Tech Stack:** Go (module `quamina.net/go/quamina/v2`), standard `testing`/`go test`, `benchstat`.

**Reference branch:** `lazy_dfa`, checked out at the worktree `/Users/sayrer/github/sayrer/quamina-lazy`. Read reference code with `git show lazy_dfa:<file>` or directly from that worktree path. The reference is **pre-embed-smalltable** — every `*smallTable` and `step(b,*stepOut)` usage in it must be adapted per this plan.

## Global Constraints

- Module path: `quamina.net/go/quamina/v2`; package `quamina` (single package, internal access).
- Go version floor: per `go.mod` (do not raise it). `b.Loop` benches require `//go:build go1.24`.
- The lazy DFA cache is **per-goroutine** (lives on `nfaBuffers`); never share it across goroutines, never add synchronization.
- Match results must be **bit-identical** to main's `traverseNFA` for every input. This is the gate.
- Do **not** delete `traverseNFA` — it remains the differential-test reference and the correctness oracle.
- Match the surrounding code's style (comment density, naming). No new exported API in Phase 1.
- `smallTable.step` on main is `func (t *smallTable) step(utf8Byte byte) *faState` — single return, no `stepOut`. Phase 1 uses it as-is (the `stepOut` refactor is Phase 4, deferred).
- Epsilon-closure sentinel (main): `faState.epsilonClosure` of `len 0` means `{self}` (self processed implicitly); `len>=2` is an explicit list that already includes self; `len 1` is never stored.

---

### Task 1: Lazy DFA core types and cache (no traversal yet)

**Files:**
- Create: `lazy_dfa.go`
- Test: `lazy_dfa_core_test.go` (new, Phase-1 unit tests for cache keying/dedup)

**Interfaces:**
- Produces:
  - `type lazyDFAState struct { transKeys []byte; transValues []*lazyDFAState; fieldTransitions []*fieldMatcher; nfaStates []*faState; cached bool }`
  - `type lazyDFA struct { ... }` with `func newLazyDFA() *lazyDFA`
  - `func (ld *lazyDFA) stats() (stateCount, stateCreates, hits, misses, cacheBytes int)`
  - `func (ld *lazyDFA) makeState(nfaStates []*faState) *lazyDFAState`
  - `func (ld *lazyDFA) populateScratchState(nextNFAStates []*faState, writeIdx int) *lazyDFAState`
  - `func (ld *lazyDFA) getOrCreateState(nfaStates []*faState) *lazyDFAState`
  - `func (ld *lazyDFA) computeKey(states []*faState) []byte`
  - `func (ld *lazyDFA) step(state *lazyDFAState, b byte) *lazyDFAState`
  - `func (ld *lazyDFA) computeStep(state *lazyDFAState, b byte) *lazyDFAState`
  - `const maxLazyDFACacheBytes = 8 << 20`

- [ ] **Step 1: Port the unchanged core verbatim**

Create `lazy_dfa.go` by copying the reference `git show lazy_dfa:lazy_dfa.go` lines 1–279 (everything from the package clause through the end of `computeKey`) **except** adapt `computeStep` per Step 2. The following port **unchanged**: imports (`bytes`, `slices`, `unsafe`), `lazyDFAState`, `maxLazyDFACacheBytes`, `lazyDFA`, `newLazyDFA`, `stats`, `makeState`, `populateScratchState`, `getOrCreateState`, `step`, `computeKey`. Do **not** copy `traverseLazyDFA` (lines 281–330) — that is Task 3.

- [ ] **Step 2: Adapt `computeStep` to main's `step()` + the sentinel**

Replace the reference `computeStep` body's inner loop. The reference calls `nfaState.table.step(b, &ld.stepResult)` and iterates `ld.stepResult.step.epsilonClosure` — both must change. Write `computeStep` exactly as:

```go
func (ld *lazyDFA) computeStep(state *lazyDFAState, b byte) *lazyDFAState {
	// Use the ping-pong buffer that is NOT holding the current state's nfaStates.
	writeIdx := 1 - ld.scratchNFAIdx
	nextNFAStates := ld.scratchNFA[writeIdx][:0]
	ld.stepGen++
	gen := ld.stepGen

	for _, nfaState := range state.nfaStates {
		nextStep := nfaState.table.step(b)
		if nextStep == nil {
			continue
		}
		// Expand the epsilon closure of the result. An empty closure is the
		// self-only sentinel (main's encoding): the closure is {nextStep}.
		if len(nextStep.epsilonClosure) == 0 {
			if ld.seenStates[nextStep] != gen {
				ld.seenStates[nextStep] = gen
				nextNFAStates = append(nextNFAStates, nextStep)
			}
			continue
		}
		for _, ecState := range nextStep.epsilonClosure {
			if ld.seenStates[ecState] != gen {
				ld.seenStates[ecState] = gen
				nextNFAStates = append(nextNFAStates, ecState)
			}
		}
	}

	// Save the buffer back (append may have grown it).
	ld.scratchNFA[writeIdx] = nextNFAStates

	if len(nextNFAStates) == 0 {
		return nil
	}

	if state.cached {
		nextState := ld.getOrCreateState(nextNFAStates)
		if nextState != nil {
			state.transKeys = append(state.transKeys, b)
			state.transValues = append(state.transValues, nextState)
			ld.cacheBytes += 9 // 1 byte key + 8 byte pointer
			return nextState
		}
	}

	return ld.populateScratchState(nextNFAStates, writeIdx)
}
```

- [ ] **Step 3: Remove the now-dead `stepResult` field**

In the `lazyDFA` struct, delete the `stepResult stepOut` field (the reference's line 58). Phase 1 no longer references `stepOut` (it is a Phase-4 type and does not exist on this branch). Leave all other scratch fields (`stepGen`, `seenStates`, `seenFields`, `sortBuf`, `keyBuf`, `scratchState`, `scratchNFA`, `scratchNFAIdx`, `scratchFT`) intact.

- [ ] **Step 4: Write the failing unit test**

Create `lazy_dfa_core_test.go`. This tests cache identity semantics directly with synthetic states (no traversal needed). `faState{}` zero values are fine — `computeKey` only reads pointer addresses; `getOrCreateState` reads `nfaStates` and `fieldTransitions`.

```go
package quamina

import "testing"

func TestLazyDFACoreKeyDedup(t *testing.T) {
	ld := newLazyDFA()
	a, b, c := &faState{}, &faState{}, &faState{}

	// Same set in different order must map to the same cached state.
	s1 := ld.getOrCreateState([]*faState{a, b, c})
	s2 := ld.getOrCreateState([]*faState{c, a, b})
	if s1 != s2 {
		t.Fatalf("same NFA-state set in different order produced different cache states")
	}
	if !s1.cached {
		t.Fatalf("getOrCreateState must mark the state cached")
	}

	// A different set must map to a different state.
	s3 := ld.getOrCreateState([]*faState{a, b})
	if s3 == s1 {
		t.Fatalf("different NFA-state set collided with an existing cache state")
	}

	if got, _, _, _, _ := ld.stats(); got != 2 {
		t.Fatalf("expected 2 cached states, got %d", got)
	}
}
```

- [ ] **Step 5: Run the test to verify it fails**

Run: `go test ./... -run TestLazyDFACoreKeyDedup -v`
Expected: FAIL to **compile** initially if `lazy_dfa.go` is incomplete, then PASS once Steps 1–3 are done. (If `lazy_dfa.go` is complete, this test should pass — it validates the ported core. Treat a compile error as the "red" state.)

- [ ] **Step 6: Make it compile and pass**

Run: `go build ./... && go test ./... -run TestLazyDFACoreKeyDedup -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add lazy_dfa.go lazy_dfa_core_test.go
git commit -m "lazy_dfa: port core cache types onto embed-smalltable main"
```

---

### Task 2: Wire the cache into `nfaBuffers`

**Files:**
- Modify: `nfa.go` (the `nfaBuffers` struct + new methods, near the other `nfaBuffers` getters around line 95–145)

**Interfaces:**
- Consumes: `lazyDFA`, `newLazyDFA` (Task 1).
- Produces:
  - field `lazyDFACaches map[*faState]*lazyDFA` on `nfaBuffers`
  - `func (nb *nfaBuffers) getLazyDFA(start *faState) *lazyDFA`
  - `func (nb *nfaBuffers) lazyDFAStats() (cacheCount, totalStates, totalCreates, totalHits, totalMisses, totalCacheBytes int)`

- [ ] **Step 1: Add the cache field to `nfaBuffers`**

In `nfa.go`, add one field to the `nfaBuffers` struct (after `qNumBuf`):

```go
	lazyDFACaches map[*faState]*lazyDFA // per-goroutine lazy DFA caches, keyed by start state
```

- [ ] **Step 2: Add `getLazyDFA` and `lazyDFAStats`**

Add these methods near the other `nfaBuffers` getters (e.g. after `getFieldSet`). Note the key type is `*faState` (the start state), **not** `*smallTable` — embed-smalltable removed stable `*smallTable` identity, and `vmFields.start` is already a `*faState`.

```go
// getLazyDFA returns the per-goroutine lazy DFA cache for the given start
// state, creating it on first use. No synchronization needed: nfaBuffers is
// per-goroutine. Keyed by the start *faState, whose identity is stable.
func (nb *nfaBuffers) getLazyDFA(start *faState) *lazyDFA {
	if nb.lazyDFACaches == nil {
		nb.lazyDFACaches = make(map[*faState]*lazyDFA)
	}
	ld, ok := nb.lazyDFACaches[start]
	if !ok {
		ld = newLazyDFA()
		nb.lazyDFACaches[start] = ld
	}
	return ld
}

// lazyDFAStats aggregates stats across all lazy DFA caches. Test/analysis only.
func (nb *nfaBuffers) lazyDFAStats() (cacheCount, totalStates, totalCreates, totalHits, totalMisses, totalCacheBytes int) {
	if nb.lazyDFACaches == nil {
		return 0, 0, 0, 0, 0, 0
	}
	cacheCount = len(nb.lazyDFACaches)
	for _, ld := range nb.lazyDFACaches {
		states, creates, hits, misses, cacheBytes := ld.stats()
		totalStates += states
		totalCreates += creates
		totalHits += hits
		totalMisses += misses
		totalCacheBytes += cacheBytes
	}
	return
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: success (no callers yet; methods may be flagged unused by linters but compile fine — Task 3/4 use them).

- [ ] **Step 4: Commit**

```bash
git add nfa.go
git commit -m "lazy_dfa: add per-goroutine cache to nfaBuffers, keyed by start *faState"
```

---

### Task 3: `traverseLazyDFA` adapted to main's transmap model

**Files:**
- Modify: `lazy_dfa.go` (append `traverseLazyDFA`)

**Interfaces:**
- Consumes: `lazyDFA.step`, `lazyDFA.getOrCreateState`, `lazyDFA.populateScratchState` (Task 1); `nfaBuffers.getFieldSet`, `nfaBuffers.getTransmap`, `transmap.levels`, `transmap.depth` (main); `valueTerminator` (main); start `*faState.epsilonClosure`.
- Produces: `func traverseLazyDFA(start *faState, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers) []*fieldMatcher`

> **Why this differs from the reference:** main's `transmap` has **no `add()`** and the caller (`tryToMatch`) owns `push()`/`pop()`. Transition dedup lives in `nfaBuffers.fieldSet`; results are written into `tm.levels[tm.depth]` (already `[:0]` from the caller's `push()`). The traversal must **not** call `push()`/`pop()` itself — doing so corrupts the transmap stack. This mirrors `traverseNFA` (`nfa.go:265-350`) exactly.

- [ ] **Step 1: Write `traverseLazyDFA`**

Append to `lazy_dfa.go`:

```go
// traverseLazyDFA traverses an NFA from start using lazy DFA construction,
// materializing and caching DFA states on demand. Match semantics are identical
// to traverseNFA; only the state representation differs. Follows main's transmap
// contract: the caller (tryToMatch) has already push()ed a transmap level, and
// dedup happens via bufs.fieldSet — this function never push()/pop()s.
func traverseLazyDFA(start *faState, val []byte, transitions []*fieldMatcher, ld *lazyDFA, bufs *nfaBuffers) []*fieldMatcher {
	// Get or create the start lazyDFAState (cached on the lazyDFA for the hot path).
	currentState := ld.startState
	if currentState == nil {
		startStates := start.epsilonClosure
		if len(startStates) == 0 {
			// self-only sentinel: the start closure is {start}.
			writeIdx := 1 - ld.scratchNFAIdx
			ld.scratchNFA[writeIdx] = append(ld.scratchNFA[writeIdx][:0], start)
			startStates = ld.scratchNFA[writeIdx]
		}
		currentState = ld.getOrCreateState(startStates)
		if currentState == nil {
			// Cache full at start — use a scratch state to avoid allocation.
			writeIdx := 1 - ld.scratchNFAIdx
			ld.scratchNFA[writeIdx] = append(ld.scratchNFA[writeIdx][:0], startStates...)
			currentState = ld.populateScratchState(ld.scratchNFA[writeIdx], writeIdx)
		} else {
			ld.startState = currentState // cache for next time
		}
	}

	// Dedup transitions via the flat fieldSet, matching traverseNFA.
	fieldSet := bufs.getFieldSet()
	clear(fieldSet)
	for _, fm := range transitions {
		fieldSet[fm] = true
	}
	for _, fm := range currentState.fieldTransitions {
		fieldSet[fm] = true
	}

	for index := 0; index <= len(val); index++ {
		var utf8Byte byte
		if index < len(val) {
			utf8Byte = val[index]
		} else {
			utf8Byte = valueTerminator
		}
		nextState := ld.step(currentState, utf8Byte)
		if nextState == nil {
			break
		}
		for _, fm := range nextState.fieldTransitions {
			fieldSet[fm] = true
		}
		currentState = nextState
	}

	if len(fieldSet) == 0 {
		return nil
	}
	tm := bufs.getTransmap()
	buf := tm.levels[tm.depth] // already [:0] from the caller's push()
	for fm := range fieldSet {
		buf = append(buf, fm)
	}
	tm.levels[tm.depth] = buf
	return buf
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add lazy_dfa.go
git commit -m "lazy_dfa: add traverseLazyDFA adapted to main's fieldSet/transmap model"
```

---

### Task 4: Route nondeterministic matching through the lazy DFA

**Files:**
- Modify: `value_matcher.go` (the `transitionOn` dispatch, around lines 53–90)
- Test: `lazy_dfa_test.go` (new — ported correctness tests)

**Interfaces:**
- Consumes: `traverseLazyDFA` (Task 3), `nfaBuffers.getLazyDFA` (Task 2), `vmFields.start`, `vmFields.isNondeterministic`, `vmFields.hasNumbers`.
- Produces: behavioral change only (nondeterministic value matchers now use the lazy DFA).

- [ ] **Step 1: Port the end-to-end correctness tests**

Create `lazy_dfa_test.go` with the two reference tests (they are architecture-independent — copy verbatim from `git show lazy_dfa:lazy_dfa_test.go`): `TestLazyDFACacheLimit` and `TestLazyDFAFallbackToNFA`.

- [ ] **Step 2: Run them — they pass on main already (via traverseNFA)**

Run: `go test ./... -run 'TestLazyDFACacheLimit|TestLazyDFAFallbackToNFA' -v`
Expected: PASS (these assert match counts, which main already satisfies through `traverseNFA`). They are the regression guard for the dispatch swap, not a red test. The red test is the stats assertion in Task 6.

- [ ] **Step 3: Swap the dispatch from `traverseNFA` to `traverseLazyDFA`**

In `value_matcher.go`, the `vmFields.start != nil` case currently calls `traverseNFA(vmFields.start, qNum, transitions, bufs)` and `traverseNFA(vmFields.start, val, transitions, bufs)` in the two `isNondeterministic` branches (around lines 75 and 83). Replace **only** those two `traverseNFA` calls. Leave the `traverseDFA` (deterministic) branches untouched.

Replace the numeric branch (around line 74–77):

```go
				if vmFields.isNondeterministic {
					ld := bufs.getLazyDFA(vmFields.start)
					return traverseLazyDFA(vmFields.start, qNum, transitions, ld, bufs)
				}
				return traverseDFA(vmFields.start, qNum, transitions)
```

Replace the string branch (around line 82–85):

```go
		if vmFields.isNondeterministic {
			ld := bufs.getLazyDFA(vmFields.start)
			return traverseLazyDFA(vmFields.start, val, transitions, ld, bufs)
		}
		return traverseDFA(vmFields.start, val, transitions)
```

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: PASS. If anything fails, the lazy path diverges from `traverseNFA` — debug before proceeding (do not adjust tests to fit).

- [ ] **Step 5: Commit**

```bash
git add value_matcher.go lazy_dfa_test.go
git commit -m "lazy_dfa: route nondeterministic value matchers through traverseLazyDFA"
```

---

### Task 5: Differential test — lazy DFA vs NFA must agree (the gate)

**Files:**
- Test: `lazy_dfa_differential_test.go` (new)

**Interfaces:**
- Consumes: public `New`/`AddPattern`/`MatchesForEvent`; internal `traverseNFA` (oracle), `readWWords` (test helper in `benchmarks_test.go`).

- [ ] **Step 1: Write the differential test**

The cleanest oracle is a second matcher whose nondeterministic matching still uses `traverseNFA`. Since the dispatch now always uses lazy, drive the oracle at the package level by comparing against a direct `traverseNFA` walk is intrusive; instead assert the lazy results equal a **brute-force** oracle over the Wordle corpus: every pattern is a `shellstyle` star-wrapped word, so an event matches pattern `i` iff the word is a subsequence-with-literal-substring per shellstyle semantics. To avoid re-implementing shellstyle, use this equivalence oracle: build the SAME patterns twice in two matchers, feed identical events, and assert identical match sets (this catches nondeterminism/cache bugs that depend on cache state and input order):

```go
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
```

> Note: both matchers use the lazy path, so this primarily guards determinism/order-independence and self-match completeness. The stronger lazy-vs-NFA oracle is added in Step 3.

- [ ] **Step 2: Run it**

Run: `go test ./... -run TestLazyDFADifferentialWordle -v`
Expected: PASS.

- [ ] **Step 3: Add the lazy-vs-NFA oracle test**

Add a test that walks the same value automaton with both `traverseLazyDFA` and `traverseNFA` and asserts equal field-transition sets. Build one matcher, reach into the field's `vmFields.start`, and compare directly:

```go
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
		// reset transmap depth between iterations
		bufs.getTransmap().resetDepth()
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
```

> The accessor chain is **verified on main (2026-06-20)**: plain `New()` (no options) yields `q.matcher` of concrete type `*coreMatcher` and `buildMode == BuiltForComfort` (so nondeterministic shellstyle patterns stay NFAs → the lazy path). `coreMatcher.fields().state` is the start `*fieldMatcher`; `fieldMatcher.fields().transitions` is `map[string]*valueMatcher`; `valueMatcher.fields().start` is `*faState`. (Only `WithPatternDeletion(true)` would make `q.matcher` a `*prunerMatcher`, whose `.Matcher` field holds the `*coreMatcher` — not used by this test.) If `nil` incoming transitions are not accepted by `traverseLazyDFA`/`traverseNFA`, pass an empty `[]*fieldMatcher{}` instead.

- [ ] **Step 4: Run it**

Run: `go test ./... -run TestLazyDFAvsNFADirect -v`
Expected: PASS. A failure here is a real correctness bug in the port — fix the implementation, never the oracle.

- [ ] **Step 5: Commit**

```bash
git add lazy_dfa_differential_test.go
git commit -m "lazy_dfa: differential tests — lazy DFA agrees with NFA over Wordle corpus"
```

---

### Task 6: Port the stats tests (prove the cache is actually exercised)

**Files:**
- Test: `lazy_dfa_stats_test.go` (new — ported, adapted)

**Interfaces:**
- Consumes: `nfaBuffers.lazyDFAStats` (Task 2) via `q.bufs.lazyDFAStats()`.

- [ ] **Step 1: Port the stats tests**

Copy `git show lazy_dfa:lazy_dfa_stats_test.go` into `lazy_dfa_stats_test.go`. These call `q.bufs.lazyDFAStats()` and assert states/creates/hits/misses behavior. Adapt only if a test references reference-only fields (e.g. `dfaTable`, `eagerDFAFailed`, `shellFastMatchers`) — those belong to later phases; **delete or skip any stats test that depends on them** and leave a `// Phase N: <name>` comment noting why. Keep every test that exercises only the lazy core.

- [ ] **Step 2: Run them**

Run: `go test ./... -run TestLazyDFA -v`
Expected: PASS, and at least one test must assert `states > 0` / `creates > 0` (proving the lazy path ran). If `states == 0`, the dispatch swap (Task 4) did not take effect — debug.

- [ ] **Step 3: Run the full suite with the race detector**

Run: `go test -race ./...`
Expected: PASS, no race warnings (the cache is per-goroutine; this proves no accidental sharing — pay attention to `quamina.Copy()` / concurrency tests).

- [ ] **Step 4: Commit**

```bash
git add lazy_dfa_stats_test.go
git commit -m "lazy_dfa: port cache-stats tests; verify lazy path is exercised"
```

---

### Task 7: Benchmarks and no-regression check

**Files:**
- Test: `lazy_dfa_bench_test.go` (new — ported)

**Interfaces:**
- Consumes: public API + `readWWords`.

- [ ] **Step 1: Port the lazy DFA benchmarks**

Copy `git show lazy_dfa:lazy_dfa_bench_test.go` into `lazy_dfa_bench_test.go`. Remove any benchmark that references later-phase fields (`dfaTable`, `shellFastMatchers`); keep `BenchmarkLazyDFAHotPath` and any pure-lazy benches. Ensure the build tag matches the helpers it uses (add `//go:build go1.24` if it uses `b.Loop`).

- [ ] **Step 2: Capture this branch's numbers**

Run: `go test -run='^$' -bench=BenchmarkLazyDFAHotPath -benchmem -count=6 . | tee /tmp/lazy.txt`
Expected: completes; records ns/op and allocs/op.

- [ ] **Step 3: Capture main's baseline for the same workload**

The hot-path workload (nondeterministic shellstyle matching) runs on `main` via `traverseNFA`. Check out main's `value_matcher.go` only, re-run, compare:

```bash
git stash push -- value_matcher.go lazy_dfa.go 2>/dev/null || true
# Simpler: compare against main by running the same bench on a clean main checkout
```

Instead, use the interleaved A/B already established for this repo: run the bench on `main` (which uses `traverseNFA`) by temporarily swapping `value_matcher.go` back to main's version, capture `/tmp/nfa.txt`, restore, then:

Run: `~/go/bin/benchstat /tmp/nfa.txt /tmp/lazy.txt`
Expected: lazy DFA shows a speedup on the repeated-hot-path benchmark (the whole point); no pathological regression on first-touch. Record the numbers in the commit message.

- [ ] **Step 4: Final full verification**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add lazy_dfa_bench_test.go
git commit -m "lazy_dfa: port hot-path benchmarks; record lazy-vs-NFA benchstat"
```

---

## Self-Review

**Spec coverage (Phase 1 scope only):**
- Lazy DFA core types/cache → Task 1. ✓
- Cache keying on `*faState` (obvious win, drop `getStartState`) → Task 2 (key type) + Task 3 (uses `start.epsilonClosure` directly, no synthetic start). ✓
- Epsilon sentinel correctness in `computeStep` → Task 1 Step 2; in `traverseLazyDFA` start → Task 3. ✓
- main's `step(b) *faState` (no `stepOut`) → Task 1 Step 2–3. ✓
- transmap/fieldSet model adaptation → Task 3. ✓
- Differential testing gate → Task 5. ✓
- `go test ./...` + `-race` → Task 6. ✓
- Benchmarks + benchstat count=6 → Task 7. ✓
- Phases 2–4 (eager DFA, shellFast, stepOut) → **out of scope; separate plans.**

**Type consistency:** `getLazyDFA(start *faState)` / `lazyDFACaches map[*faState]*lazyDFA` (Task 2) match the `traverseLazyDFA(start *faState, …, ld *lazyDFA, …)` call in Task 4. `lazyDFAStats()` 6-tuple matches its use in Task 6. `traverseLazyDFA` signature in Task 3 matches its calls in Task 4 and Task 5.

**Placeholder scan:** No TBD/TODO; every code step shows complete code. The one verification caveat (Task 5 Step 3 accessor chain) is explicit and bounded, with a fallback instruction.

**Known risk to watch during execution:** Task 5 Step 3 reaches into unexported accessors (`q.matcher.(*coreMatcher)`, `fields().state`, `transitions["x"]`, `fields().start`). These were confirmed to exist during design but their exact names must be verified against the code at implementation time; the step says so and gives the expected shapes.
