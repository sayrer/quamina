# Phase 4 (`stepOut` refactor) — evaluated and DROPPED

**Date:** 2026-06-20
**Branch:** `lazy-dfa-port` (spike)
**Decision:** Do **not** port the `lazy_dfa` reference branch's `stepOut` refactor.

## What it was
The reference branch changed `smallTable.step` from `step(b) *faState` to
`step(b, *stepOut)`, where `stepOut{ step *faState; epsilons []*faState }`
bundles the byte-step result together with the table's **live** epsilon
transitions.

## Why it was on probation
The design spec (`2026-06-20-lazy-dfa-port-design.md`) put Phase 4 "on
probation": port it only if it shows a measurable win, since main's
`step(b) *faState` + the precomputed-epsilon-closure sentinel already cover
every consumer the lazy core needs.

## Why it is dropped (static analysis, conclusive)
On this (post-embed-smalltable) branch, **no match-time consumer reads a
table's live `epsilons`**. Every caller of `step()` on the hot path —
`computeStep` (`lazy_dfa.go`), `traverseNFA` and `traverseDFA` (`nfa.go`) —
uses the returned `*faState` and then the **precomputed** `faState.epsilonClosure`.
The only readers of live `table.epsilons` are build/merge-time
(`mergeFAStates`, `simplifySplices`, `rune_range.go`, `regexp_nfa.go`),
memory-cost/stats accounting, and the debug prettyprinter — none of which go
through `step()`.

Therefore `stepOut`'s `epsilons` field would have **zero consumers**. Porting
it would add a struct-pointer write (and epsilon-slice population) to
`smallTable.step`, which is documented as "the white-hot center of Quamina's
runtime CPU; keep it inlinable." An out-parameter struct write risks
de-inlining the single hottest function for no functional benefit — a strict
regression risk with no upside. A benchmark was deemed unnecessary: it would
only measure the overhead of populating a field nothing reads.

The reference's rationale for `stepOut` (getting live epsilons alongside the
step during a traversal that walked epsilons live) does not transfer to this
architecture, where epsilon closures are precomputed at build time and
consumed from `epsilonClosure`.

## Net
Phases 1–3 (lazy DFA core, eager-DFA-within-budget, shellFast) are ported.
Phase 4 is intentionally omitted; this note records why so it is not
re-attempted without new motivation.
