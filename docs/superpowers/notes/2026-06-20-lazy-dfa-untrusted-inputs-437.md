# Lazy DFA vs. untrusted inputs (the #437 angle)

**Date:** 2026-06-20
**Branch:** `lazy-dfa-port` (demo)
**Code:** `lazy_dfa_repetitive_bench_test.go`

## The fact this trades on

Quamina takes **two untrusted inputs**:

- **Patterns** — regex/shellstyle/wildcard/numeric rules, often authored by end
  users in filter-rule deployments.
- **Events** — JSON, parsed on the match hot path.

An adversary (or just a heavy user) can therefore supply a **giant pattern set**
whose fully-determinized DFA is infeasible to build. The eager NFA→DFA
conversion explodes; you'd think you're then stuck with slow NFA traversal.

The lazy DFA's point: if the **event stream is repetitive** (low-cardinality —
the same handful of values recur, e.g. timestamps, hostnames, a few hot words),
you still get **near-DFA throughput**, because the lazy DFA only ever
materializes — and then caches — the few NFA-state-sets those recurring events
actually traverse. You pay for the paths you use, not for the whole DFA.

## How fast quamina's shellstyle DFA explodes

The eager DFA is not "big but buildable" here — it blows up almost immediately
(measured on this branch):

| pattern style | scale | eager DFA |
|---|---|---|
| substring `*word*` | 2 rules | 36 states |
| substring `*word*` | 3 rules | 331 states |
| substring `*word*` | 6 rules | 1,203 states |
| substring `*word*` | 8 rules | >20,000 states |
| subsequence `*a*b*c*d*e*` | 5 rules | >100,000 states |

quamina's own default eager budget is `maxEagerDFAStates` (500), so it abandons
eager conversion and uses the lazy DFA for anything past a few rules.

## The benchmarks

### Giant set, eager infeasible — `BenchmarkGiantPatternsRepetitiveEvents`

400 subsequence rules (`aback` → `*a*b*a*c*k*`). The eager DFA exceeds 50,000
states and is abandoned. Matching a repetitive 4-event stream:

| arm | ns/op | allocs |
|---|---|---|
| `nfa` — re-walk the giant NFA every event | ~15,544 | 0 |
| `lazyDFA` — warm cache, direct traversal | **~148** | 0 |
| `full_path_warm` — real `MatchesForEvent` | ~295 | 0 |

≈ **105× faster than the NFA baseline.** `TestEagerInfeasibleLazyTractable`
asserts the eager DFA is abandoned and that the lazy cache stays tiny — **27
cached states, 99.6% hit rate, ~217 KB** after 800 events.

(The `nfa` arm feeds the field value quoted, as the flattener stores string
values — `"aback"`, not `aback` — otherwise the leading `"` never matches and
the traversal is a no-op. The differential test proves `traverseNFA` and
`traverseLazyDFA` return identical results, so the arms are apples-to-apples.)

### Small set, eager feasible — `BenchmarkEagerVsLazyVsNFA`

6 substring rules (`*word*`), eager force-built with a bigger budget (5,000)
purely to time it:

| arm | ns/op |
|---|---|
| `eager` — single deterministic walk | **~74** |
| `lazyDFA` — warm cache | ~151 |
| `nfa` | ~389 |

## What the two together say

- Where the eager DFA **is** feasible, the lazy DFA runs at the same order
  (~2× eager here) without building the full DFA, and well ahead of the NFA.
- Where the eager DFA is **infeasible** (the realistic adversarial case for
  untrusted patterns), the eager arm can't exist at all; the lazy DFA holds
  ~150 ns/op while the NFA balloons to ~15.5 µs.

So: a hostile or heavy pattern set can't be eagerly determinized, but a
repetitive event stream still gets DFA-class match speed from the lazy DFA — the
property that makes the two-untrusted-inputs situation in #437 tractable.

## Caveats

- The eager arm is necessarily at a toy scale (6 rules); eager simply cannot be
  built at any "giant" scale for these patterns.
- The `full_path_warm` number includes JSON flattening on top of the ~148 ns
  traversal.
- Numbers are Apple M1 Ultra, `-benchmem`, illustrative — not a regression gate.
