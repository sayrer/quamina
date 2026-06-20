# Lazy DFA Port onto Embed-Smalltable `main` — Design

**Date:** 2026-06-20
**Branch:** `lazy-dfa-port` (off `main`)
**Reference implementation:** `lazy_dfa` branch (worktree at `/Users/sayrer/github/sayrer/quamina-lazy`)

## Problem

The `lazy_dfa` branch implements lazy (on-demand) DFA construction plus three
adjacent experiments, but it predates a deep architectural change on `main`:
**`smallTable` was embedded into `faState` by value** (commit `2559315`), which
removed the stable `*smallTable` pointer identity that the lazy DFA cache keys
on. Attempting `git merge main` into `lazy_dfa` produces dense conflicts in the
hottest files (`nfa.go`, `value_matcher.go`, `small_table.go`, `state_lists.go`,
`epsi_closure.go`) and, worse, git's auto-merge silently took main's embedded
`faState` while leaving the lazy DFA code keyed on a pointer that no longer
exists — semantically broken, not just textually conflicted.

This work ports the **entire** `lazy_dfa` branch onto current `main`, faithfully
(behavior-preserving) while folding in the obvious simplifications the new
architecture makes free.

### What `main` changed (vs the `lazy_dfa` merge-base)

- **Embed-smalltable:** `faState.table` is now a `smallTable` value, not
  `*smallTable`. No stable `*smallTable` identity; dedup keys on `tableShareKey`
  (steps-backing-array pointer, `dedup_key.go`).
- **Epsilon-closure sentinel:** `faState.epsilonClosure` of length 0 now means
  `{self}` (the shared `selfOnlyClosure` sentinel). Consumers must process self
  explicitly when the closure is empty. `faState.closureSetGen` was removed
  (build-time gen state moved to side tables).
- **`vmFields.startState` → `start`**, and `start` is a `*faState`.
- **`nfa2Dfa` signature:** now `nfa2Dfa(nfaStart *faState)`, not
  `nfa2Dfa(*smallTable)`.
- **`smallTable.step`:** `step(b) *faState` (single return).
- **`state_lists.intern()`:** dedups via `sort + slices.Compact`, no `seen` map.

### What the `lazy_dfa` branch assumes / adds (4 concerns)

1. **Lazy DFA core** — `lazy_dfa.go` (new): `lazyDFAState` (a set of NFA states),
   `lazyDFA` cache (per-goroutine in `nfaBuffers`, keyed on the start
   `*smallTable`, 8 MB budget matching RE2), `computeStep`, `traverseLazyDFA`.
2. **Eager-DFA-within-budget** — `vmFields.dfaTable`, `eagerDFAFailed`,
   `nfa2DfaWithBudget`; converts eagerly when the result stays under budget,
   falls back to lazy otherwise.
3. **shellFast** — `shell_fast.go` (new), `vmFields.shellFastMatchers`, a
   `shell_style.go` hook: a fast path for pure shell-style patterns.
4. **`stepOut` refactor** — `smallTable.step(b, *stepOut)` writing
   `{step *faState, epsilons []*faState}`.

Sizing: `lazy_dfa` adds ~2,090 non-test lines across 17 files in 26 non-merge
commits; `main` independently rewrote the same hot files. Maximal overlap in the
most correctness-sensitive code.

## Goals

- Port all four concerns onto `main`'s architecture, behavior-preserving.
- Fold in the simplifications embed-smalltable makes free ("obvious wins"), and
  drop genuine dead weight when benchmarks justify it.
- Land it reviewably: clean, per-concern commits, each verified before the next.
- Unblock the parked NFA/DFA visualization (separate spec), which is built on
  the result.

## Non-goals

- No new lazy DFA features or redesign of the cache strategy/memory budget.
- No changes to public API beyond what the experiments already introduce.
- The visualization itself (separate design, Approach A, already approved).

## Strategy — fresh re-implementation onto `main` (Approach 2)

Rejected alternatives:
- **Merge-and-resolve:** one unreviewable merge commit; dense conflicts in
  near-total `nfa.go` rewrites; high risk of a silent hot-path correctness bug.
- **Rebase the 26 commits:** the branch has internal merges; rebasing
  re-resolves the same embed-smalltable conflicts dozens of times.

Chosen: branch from `main`, re-apply each concern's *ideas* adapted to
embed-smalltable from the start, porting file-by-file from the reference branch,
with a differential test gating each concern. The `lazy_dfa` branch is preserved
untouched as the reference.

## Phasing (dependency order)

### Phase 1 — Lazy DFA core
`lazy_dfa.go`, the `nfaBuffers` cache, and the `vmFields` traversal hook
(nondeterministic value matchers route to `traverseLazyDFA`). Sits directly on
main's `step(b) *faState` — it does **not** need the `stepOut` refactor, because
`computeStep` only consumes the stepped-to state and its `epsilonClosure`. Goes
first and stands alone; this is the piece the visualization needs.

### Phase 2 — Eager-DFA-within-budget
`vmFields.dfaTable`, `eagerDFAFailed`, `nfa2DfaWithBudget`, and the dispatch that
prefers the eager DFA when present. Builds on main's `nfa2Dfa(*faState)`.

### Phase 3 — shellFast
`shell_fast.go`, `vmFields.shellFastMatchers`, the `shell_style.go` hook. Most
independent (a separate fast path), depends on neither lazy nor eager.

### Phase 4 — `stepOut` refactor (on probation)
Port `smallTable.step(b, *stepOut)`. **Explicitly conditional:** main's
`step(b) *faState` + the epsilon sentinel already cover every consumer found in
the reference branch, so this phase ports it, benchmarks it, and **if it shows
no measurable win it is dropped** as dead weight (an "obvious win"). The verdict
is recorded at this phase, not decided silently.

## Key technical decisions

- **Cache keying (the crux):** key `lazyDFACaches` on the start `*faState`
  (stable identity post-embed) instead of the now-defunct `*smallTable`. Map
  becomes `map[*faState]*lazyDFA`; `getLazyDFA(start *faState)`. Drop the
  synthetic `getStartState` wrapper entirely — `vmFields.start` *is* the
  `*faState`. (obvious win)
- **Epsilon sentinel (correctness):** `computeStep` must treat an empty
  `epsilonClosure` as `{self}`, matching main's `traverseNFA`/`nfa2Dfa`
  consumers; otherwise it silently drops the self-state from the next NFA set.
  The lazy `lazyDFA.seenStates` generation scratch (independent of the removed
  `faState.closureSetGen`) is retained as-is.
- **state_lists:** use main's `sort + slices.Compact` `intern()`; verify the
  eager `nfa2Dfa`/`n2dNode` paths produce correct canonical state sets against
  it.
- **nfaBuffers:** reconcile main's `fieldSet` scratch buffer with lazy's
  (removed) `startState`/`startClosure` scratch — keep only what each surviving
  path needs.

## Verification & success criteria

- **Differential testing is the gate.** Over a pattern corpus including the
  `wwords.txt` Wordle case, the lazy DFA, eager DFA, and plain NFA paths must
  produce **bit-identical match sets**. Port the reference branch's differential
  tests; a concern is not "done" until its differential test is green.
- **`go test ./...` green, including `-race`** — the cache is per-goroutine and
  must be proven free of cross-goroutine sharing.
- **Benchmarks:** `BenchmarkLazyDFAHotPath` (and the other lazy/regex benches)
  show the expected lazy-DFA advantage; no regression on main's existing
  benchmarks, compared with `benchstat` at `count=6`.

## Risks

- **Silent hot-path correctness bugs** (the epsilon-sentinel trap is one
  instance). Mitigated by differential testing as a hard gate per phase.
- **Cache-keying subtlety** post-embed: keying on the wrong identity gives cache
  corruption, not a compile error. Mitigated by the differential gate plus a
  targeted test that the same start state reuses one cache.
- **`stepOut` may not pay for itself** on the new architecture; handled by the
  probation framing in Phase 4 rather than assuming it lands.

## Hand-off

On completion the port branch carries the lazy DFA, unblocking the parked
visualization (Approach A: snapshot API in `quamina` + thin Go server + D3 view,
event-driven NFA→DFA lighting over the Wordle dictionary), built directly on
this branch.
