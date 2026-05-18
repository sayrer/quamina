# Lazy DFA Cache on `coreFields`

**Status:** Design approved, ready for implementation plan
**Date:** 2026-05-17
**Branch target:** New branch off `build-context-extract` (PR #521)
**Related:** issue #481 (NFA → DFA optimization), PR #521 (HeapAlloc memory budget),
prototype branch `lazy_dfa`

## Background

Quamina compiles patterns to an NFA. NFAs handle wildcards, regexes, and pattern
merges without state explosion, but match-time traversal is slower than a DFA
because every input byte may require expanding an epsilon closure across many
NFA states.

A full eager NFA → DFA conversion (the dormant `nfa2Dfa`) can blow up
exponentially in the number of NFA states (`O(2^N)`), so it is not viable
for patterns with many wildcards or quantified character classes. But in
real workloads, inputs are not uniformly distributed across the theoretical
state space — they hammer a small hot subset. A **lazy DFA**, which materialises
DFA states on demand and caches them, captures most of the eager-DFA speedup
without paying the eager-DFA construction cost.

The prototype in branch `lazy_dfa` demonstrated the speedup (8x–337x on
hot-path benchmarks, neutral or slightly faster on existing workloads) using
a per-goroutine cache stored in `nfaBuffers`. The prototype has two
correctness problems that this design fixes, and one architectural choice
that prevents it from composing with multi-goroutine workloads.

### Problems with the prototype

1. **Cache lifetime is wrong.** The cache lives in `nfaBuffers` (per-Quamina
   instance, never invalidated). But `coreMatcher` uses copy-on-write:
   `addPattern` builds a fresh `coreFields` and atomic-swaps `updateable`
   (core_matcher.go:28, 149). NFA `*faState` pointers in the new `coreFields`
   are different objects. After `AddPattern`, the cache's keys reference
   stale pointers — either silently wrong matches or use-after-free via the
   `unsafe.Pointer` math in `computeKey`. The prototype's tests don't
   `AddPattern` between matches, so the bug never fired.

2. **No sharing across goroutines.** With `Quamina.Copy()` producing N
   instances sharing one matcher, each instance's `nfaBuffers` builds its
   own cache from cold start. Memory cost scales as N × budget; warmup cost
   is paid per goroutine.

3. **No invalidation hook.** Because of (1), even ignoring the correctness
   issue, the prototype has no way to drop the cache when the underlying
   NFA changes.

## Goals

1. Match-time speedup comparable to the prototype (5x–100x on hot-path
   workloads, neutral on already-fast workloads).
2. Correct under concurrent `MatchesForEvent` calls across `Copy()` instances.
3. Correct under `AddPattern` racing with in-flight matches.
4. Bounded total memory: one configurable budget bounds the cache for the
   whole `coreMatcher`, not per-goroutine.
5. Opt-in: zero behavior change for callers that do not set
   `WithLazyDFACacheBytes`.
6. Independent of the `AddPattern` memory budget from PR #521. Both budgets
   coexist; neither is the other.

## Non-goals (deliberately out of scope for v1)

1. **Eviction.** Cache freezes when full; existing entries serve hits forever
   within a snapshot's lifetime. `AddPattern` is the implicit "evict
   everything" event.
2. **Sharded `insertMu`.** Single global insert mutex. Slow path is rare
   after warmup.
3. **Eager `nfa2Dfa` reconciliation.** This design is purely match-time
   caching. AddPattern-time DFA construction (Tim's `OptimizedForSpeed`
   boolean, the dormant `nfa2Dfa` function) is orthogonal and stays as-is.
4. **Cross-snapshot cache carry-over.** Even if `addPattern` happens to
   preserve some `*faState` pointers via merge, we do not attempt to inherit
   cached states.
5. **Unified `MatcherStats()` API.** Tim and Rishi discussed a broader stats
   surface; we ship `LazyDFAStats()` standalone now and fold it in later.
6. **Per-Quamina-instance budget override.** Budget is set at `New()` and
   immutable. No `SetLazyDFACacheBytes` runtime mutator.
7. **Folding `cacheBytes` into `currentMemory`.** The lazy DFA cache size
   is not counted against the AddPattern budget. Operators read them
   separately.

## Architecture & lifetime

The cache is a field on `coreFields`. Born lazily on the first
`matchesForFields` call for a given snapshot; dies when `addPattern`'s
atomic swap replaces `coreFields`. Each CoW snapshot has its own cache, so
invalidation is automatic.

```
coreMatcher
  └─ updateable atomic.Pointer[coreFields]
       └─ coreFields { state, segmentsTree, memoryBudget, currentMemory,
                       lazyDFABudget uint64,           // ← NEW (config)
                       lazyDFA atomic.Pointer[lazyDFA], // ← NEW (lazy init)
                       lazyDFAOnce sync.Once }          // ← NEW

lazyDFA (shared across goroutines, lock-free hot path)
  ├─ cache sync.Map           // stateKey → *lazyDFAState
  ├─ cacheBytes atomic.Uint64 // approximate retained cache size
  ├─ budget uint64            // immutable, from WithLazyDFACacheBytes
  ├─ startState atomic.Pointer[lazyDFAState]
  ├─ insertMu sync.Mutex      // held only during cache-miss insert
  └─ stats { hits, misses, scratchFallback, stateCount atomic.Uint64 }
                              // all atomic so LazyDFAStats() reads are race-free

lazyDFAState (heap-allocated, shared across goroutines)
  ├─ transitions atomic.Pointer[lazyTransitions]  // hot path: atomic.Load
  ├─ transMu sync.Mutex                            // slow path: append serial
  ├─ fieldTransitions []*fieldMatcher              // immutable after publish
  ├─ nfaStates []*faState                          // immutable after publish
  └─ cached bool

lazyTransitions (immutable once published)
  ├─ keys   []byte
  └─ values []*lazyDFAState

nfaBuffers (per-goroutine scratch — unchanged shape from prototype)
  ├─ keyBuf, sortBuf                                  // computeKey
  ├─ stepResult                                       // smallTable.step
  ├─ scratchNFA [2][]*faState, scratchNFAIdx,
     scratchState                                     // cache-full path
  ├─ seenStates map[*faState]uint64, stepGen uint64   // computeStep dedup
  └─ seenFields map[*fieldMatcher]bool                // makeState dedup
```

### Lifetime invariants

- After a `*lazyDFAState` is published into the cache via `sync.Map.LoadOrStore`,
  its `fieldTransitions`, `nfaStates`, and `cached` fields never change.
  The only mutable thing is `transitions`, an `atomic.Pointer` to an
  immutable `lazyTransitions`.
- Cache keys use `*faState` pointers from the current `coreFields` snapshot.
  Those pointers stay valid for the snapshot's entire lifetime — the GC
  keeps the NFA alive as long as any goroutine holds the snapshot via
  `m.updateable.Load()`.
- `cacheBytes` is approximate; incremented under `insertMu`, read via
  `atomic.Load` when checking against the budget.

### Why pointer, not value, for `lazyDFA` on `coreFields`

- Lazy `nil`-init avoids cost for matchers that never call `MatchesForEvent`.
- `addPattern`'s `freshStart` construction skips initializing the new cache
  — it remains `nil` until the first match against the new snapshot.
- Cleaner reasoning: each snapshot's cache is born and dies with the snapshot.

## Hot path and slow path

### Hot path: `step(state *lazyDFAState, b byte, bufs *nfaBuffers) *lazyDFAState`

```
1. trans := state.transitions.Load()        // atomic, lock-free
2. if trans != nil:
     if idx := bytes.IndexByte(trans.keys, b); idx >= 0:
       atomic.AddUint64(&ld.stats.hits, 1)
       return trans.values[idx]              // HOT PATH ends here
3. return computeStep(state, b, bufs)        // slow path
```

Two field reads, one atomic load (the `transitions` pointer), one
`bytes.IndexByte` (SIMD intrinsic on amd64/arm64). No mutex, no `sync.Map`
lookup, no allocation. Same shape as the prototype's per-goroutine version.

### Slow path: `computeStep`

```
1. atomic.AddUint64(&ld.stats.misses, 1)
2. Compute nextNFAStates using per-goroutine scratch
   (bufs.scratchNFA ping-pong + bufs.seenStates dedup + bufs.stepResult).
3. If len(nextNFAStates) == 0: return nil.
4. If !state.cached (we're already in scratch-state fallback):
     populate bufs.scratchState, return &bufs.scratchState.
5. Compute key = computeKey(nextNFAStates) into bufs.keyBuf.
6. nextState = lookupOrInsert(ld, key, nextNFAStates)  // see below
7. If nextState == nil (cache full):
     atomic.AddUint64(&ld.stats.scratchFallback, 1)
     populate bufs.scratchState, return &bufs.scratchState.
8. appendTransition(state, b, nextState)               // see below
9. Return nextState.
```

### Cache lookup and insert: `lookupOrInsert`

```
if existing, ok := ld.cache.Load(string(key)); ok {
    return existing.(*lazyDFAState)
}

ld.insertMu.Lock()
defer ld.insertMu.Unlock()

// Re-check under lock — another goroutine may have inserted.
if existing, ok := ld.cache.Load(string(key)); ok {
    return existing.(*lazyDFAState)
}

// Build the state first so we can size-estimate from its actual
// fieldTransitions. If the budget rejects it, we discard one allocation
// — acceptable because this path runs at most a few times per cache
// lifetime (right at the boundary where cacheBytes approaches budget).
newState := makeState(nextNFAStates)  // newState.cached = true
estCost := estimateBytes(newState)
if ld.cacheBytes.Load()+estCost > ld.budget {
    return nil  // caller falls back to scratch state
}
ld.cache.Store(string(key), newState)
ld.cacheBytes.Add(estCost)
ld.stats.stateCount.Add(1)
return newState
```

### Transition append: `appendTransition`

```
state.transMu.Lock()
defer state.transMu.Unlock()

// Double-check: another goroutine may have appended b while we waited.
// cur may be nil if this is the first transition appended to state.
cur := state.transitions.Load()
var curKeys []byte
var curValues []*lazyDFAState
if cur != nil {
    if idx := bytes.IndexByte(cur.keys, b); idx >= 0 {
        return  // someone else added it; their nextState is identical
    }
    curKeys = cur.keys
    curValues = cur.values
}

// Allocate exactly one new immutable transitions struct.
newKeys := make([]byte, 0, len(curKeys)+1)
newKeys = append(newKeys, curKeys...)
newKeys = append(newKeys, b)

newValues := make([]*lazyDFAState, 0, len(curValues)+1)
newValues = append(newValues, curValues...)
newValues = append(newValues, nextState)

state.transitions.Store(&lazyTransitions{keys: newKeys, values: newValues})
```

Cloning is required: if we `append` to the existing backing array in place
when cap > len, a concurrent reader holding the old `*lazyTransitions` could
observe a torn write (length not yet updated but new value already in a
backing array slot). Cloning costs one extra copy per slow-path call —
slow path is rare after warmup.

### Why per-state mutex instead of CAS retry

A `CompareAndSwap` loop on `state.transitions` would work but allocates a
new `lazyTransitions` on every retry attempt under contention.
`state.transMu` serializes appends to a single state's transitions while
leaving appends to other states fully parallel. Cost: 8 bytes per state
(roughly 5KB for the prototype's worst observed cache size of 630 states).
Dead after warmup.

### Per-goroutine vs shared split

| Lives on | Why |
|---|---|
| `lazyDFA` (shared, atomic or immutable) | `cache`, `cacheBytes`, `budget`, `startState`, `insertMu`, stats counters |
| `lazyDFAState` (shared, immutable or atomic) | `fieldTransitions`, `nfaStates`, `cached`, `transitions` (atomic.Pointer), `transMu` |
| `nfaBuffers` (per-goroutine, unsynchronized) | `keyBuf`, `sortBuf`, `stepResult`, `scratchNFA`, `scratchNFAIdx`, `scratchState`, `seenStates`, `stepGen`, `seenFields` |

## Data flow and API surface

### Public API additions

```go
// WithLazyDFACacheBytes opts the matcher into lazy DFA caching with the
// given byte budget. budget == 0 disables (default). Larger budgets cache
// more DFA states for hotter workloads; recommended starting point is
// 8<<20 (matches RE2's default).
func WithLazyDFACacheBytes(budget uint64) Option

// LazyDFAStats returns a snapshot of the lazy DFA cache's behavior.
// Safe to call concurrently with MatchesForEvent.
type LazyDFAStats struct {
    Enabled         bool
    CacheBytes      uint64
    Budget          uint64
    StateCount      int
    TransitionHits  uint64
    TransitionMiss  uint64
    ScratchFallback uint64
}

func (q *Quamina) LazyDFAStats() LazyDFAStats
```

Default: `0` means **disabled** (existing NFA traversal). Opt-in eliminates
behavior change risk for current callers.

### AddPattern flow

Unchanged. The lazy DFA cache is not involved at AddPattern time — it is
purely a match-time accelerator. The HeapAlloc-based budget from PR #521
governs AddPattern as it does today. The two budgets do not interact.

### MatchesForEvent flow

```
Quamina.MatchesForEvent(event)
  → flattener.Flatten(event)
  → coreMatcher.matchesForFields(fields, q.bufs)
       cf := m.updateable.Load()              // snapshot CoW pointer
       ld := cf.getOrInitLazyDFA()            // may be nil if disabled
       (existing per-field traversal loop)
       → valueMatcher.transitionOn(field, ld, bufs)
            if ld == nil:
              return traverseNFA(table, val, transitions, bufs)
            return traverseLazyDFA(table, val, transitions, ld, bufs)
```

The branch is in `valueMatcher.transitionOn` — one nil check per
field-value traversal.

### Lazy initialization with `sync.Once`

```go
func (f *coreFields) getOrInitLazyDFA() *lazyDFA {
    if ld := f.lazyDFA.Load(); ld != nil {
        return ld
    }
    f.lazyDFAOnce.Do(func() {
        if f.lazyDFABudget > 0 {
            f.lazyDFA.Store(newLazyDFA(f.lazyDFABudget))
        }
    })
    return f.lazyDFA.Load()  // still nil if budget == 0
}
```

`sync.Once` ensures exactly one allocation across concurrent first-callers.
After the first call, the hot path is one `atomic.Load`.

### How the budget is plumbed

`New(WithLazyDFACacheBytes(n))` sets `q.lazyDFABudget`. When `newCoreMatcher`
initializes the first `coreFields`, it copies this value into
`coreFields.lazyDFABudget`. Every subsequent CoW `coreFields` (built by
`addPattern`) inherits the value (configuration constant for the matcher's
lifetime, not per-pattern).

### `Quamina.Copy()` interaction

Copies share `q.matcher` (and therefore `coreMatcher.updateable` and all
`coreFields` snapshots). Each copy has its own `nfaBuffers`. Therefore
**all copies automatically share the lazy DFA cache** — this is the entire
point of moving the cache to `coreFields`. No additional plumbing.

## Error handling

The lazy DFA path **never returns errors to the caller.**

1. **Cache full + slow-path miss** → fall through to scratch state, traverse
   onward, return correct match result, increment `ScratchFallback`.

2. **AddPattern racing with in-flight matches** → `m.updateable.Load()` at
   the top of `matchesForFields` pins a snapshot for the call's duration.
   The in-flight match completes against that snapshot's `lazyDFA`. New
   `AddPattern` builds fresh `coreFields` with `lazyDFA: nil`; subsequent
   matches lazily rebuild. Old `coreFields` becomes garbage when the last
   goroutine holding the snapshot returns. No invalidation logic needed.

3. **`WithLazyDFACacheBytes(absurd_value)`** → validate at `New()`:
   reject values above `1 << 40` with an error. No runtime check.

## Testing

### Unit tests (`lazy_dfa_test.go`)

- `TestLazyDFA_DisabledByDefault` — `New()` without the option produces a
  matcher whose `coreFields.lazyDFA` stays `nil` forever, even after matches.
  Asserts `LazyDFAStats().Enabled == false`.
- `TestLazyDFA_EquivalenceWithNFA` — for each shellstyle and regex pattern
  in the existing test corpus, the result of an event matched in NFA-only
  mode equals the result in lazy-DFA mode. Correctness anchor.
- `TestLazyDFA_CacheLifetimeAcrossAddPattern` — match, then `AddPattern`,
  then match again. No panic from stale pointers; results correct;
  `StateCount` resets after the AddPattern.
- `TestLazyDFA_CacheFullBehavior` — tight budget (a few KB), match until
  `cacheBytes == budget`, continue matching. Results stay correct;
  `ScratchFallback` increases.
- `TestLazyDFA_ConcurrentMatchesSameSnapshot` — N goroutines on `Copy()`
  instances do the same match in parallel before, during, and after
  warmup. Run under `-race`. No data races; results identical.
- `TestLazyDFA_ConcurrentMatchesAcrossAddPattern` — goroutines matching
  while another goroutine `AddPattern`s. Run under `-race`. Each match
  returns correct results for whichever snapshot it pinned.

### Equivalence-by-fuzz (`lazy_dfa_fuzz_test.go`)

- `FuzzLazyDFAEquivalence` — Go's native `testing.F`. Pattern and event
  generated from fuzz corpus; NFA-only result must equal lazy-DFA result.
  Pin a small seed corpus in `testdata/fuzz/FuzzLazyDFAEquivalence/`.

### Static analysis

- All tests and benchmarks pass under `go test -race ./...`.
- `go vet -atomic` clean — atomic operations on `transitions` not mixed
  with non-atomic loads.

## Benchmarks (`lazy_dfa_bench_test.go`)

Each benchmark runs in two modes via subbenchmark:

```go
for _, mode := range []struct{ name string; opt Option }{
    {"NFAOnly", WithLazyDFACacheBytes(0)},
    {"LazyDFA", WithLazyDFACacheBytes(8 << 20)},
} {
    b.Run(mode.name, func(b *testing.B) { ... })
}
```

Reports `ns/op`, `B/op`, `allocs/op`, and (via `b.ReportMetric`) cache hit
rate and `cacheBytes` from `LazyDFAStats`.

### Sanity (no regression)

1. **`BenchmarkLazyDFA_ExactString`** — 1 pattern `{"x":["foobar"]}`,
   uniform event. Cache holds ~5 states. **Target:** ≤2% slowdown vs
   NFA-only; zero allocs/op after warmup.

2. **`BenchmarkLazyDFA_SingleShellstyle`** — 1 pattern `{"x":["*foo*"]}`,
   uniform matching event. Prototype's published 8x win. **Validates:**
   shared-cache design preserves the prototype's speedup.

### Hot-path concentration (5x–100x wins)

3. **`BenchmarkLazyDFA_ManyOverlappingWildcards`** — N ∈ {8, 16, 32, 64,
   128} patterns like `*aXb*`, `*bYc*`, etc., uniform event matching half.
   NFA epsilon work grows with N; lazy DFA's hot set saturates ~30–60
   states. **Validates:** the textbook O(2^N) → O(visited) collapse.

4. **`BenchmarkLazyDFA_RegexAlternation`** — 20 variants of
   `(foo|bar|baz|quux|xyzzy)\\d+` patterns, single hot event. Dense epsilon
   structure. **Validates:** the regex case that NFA traversal does badly.

5. **`BenchmarkLazyDFA_LiteralInRegex`** — `.*ERROR.*\\d+.*` against
   synthetic log lines. Literal sub-runs amplify caching. **Validates:**
   real log workload shape.

### Eager-DFA-explosion territory (unique wins)

6. **`BenchmarkLazyDFA_QuantifiedCharClass`** — `[a-z]{8,16}` × 5 patterns.
   Eager `nfa2Dfa` blows up; lazy DFA materializes only exercised states.
   **Validates:** the case where eager conversion is infeasible.

7. **`BenchmarkLazyDFA_ManyAnchoredRegex`** — 200 patterns
   `PFX[0-9]+SFX<i>` for `i ∈ 0..199`, events targeting various `<i>`.
   **Validates:** many regex patterns without eager-DFA cost.

8. **`BenchmarkLazyDFA_DeepEpsilonNest`** — pattern designed to maximize
   epsilon closure depth: `((a|b|c)*(d|e|f)*)+` with several layers.
   **Validates:** the epsilon-closure dimension specifically.

### Adversarial (must not regress)

9. **`BenchmarkLazyDFA_CacheThrashing`** — pattern admitting a huge state
   space (e.g., `*X*Y*Z*W*V*`), inputs permuted so each event visits
   uncached `(state, byte)` combinations. Cache fills, every miss hits the
   scratch path. **Target:** within 10% of NFA-only baseline; zero
   crashes; `cacheBytes == budget`; hit rate trending down.

### Multi-goroutine (validates "shared on coreFields")

10. **`BenchmarkLazyDFA_ParallelMatchers`** — 8 / 16 / 32 / 64 goroutines
    on `Copy()` instances sharing one `coreMatcher`, each running the #3
    workload. **Validates simultaneously:** (a) shared cache eliminates
    per-goroutine cold-start, (b) total memory bounded by single budget
    (not N × budget). The benchmark that makes the "shared on coreFields"
    choice defensible by data.

### Regression gates

We won't gate CI on absolute ns/op (machine-dependent), but the **ratio**
between modes is bounded:

- `ExactString.LazyDFA` ≤ 1.02 × `ExactString.NFAOnly`
- `CacheThrashing.LazyDFA` ≤ 1.10 × `CacheThrashing.NFAOnly`
- `ManyOverlappingWildcards.LazyDFA / .NFAOnly` ≤ 0.20 at N=64
  (5x speedup or better)

## Open items / future work

- **Eviction.** If long-running services see workload drift hurt cache
  effectiveness, design v2: clock/approximate-LRU sketched in the
  brainstorming discussion as Option B.
- **Sharded `insertMu`.** If profiling shows contention on first-warmup
  matches, shard by hash of state key. YAGNI until measured.
- **Unified `MatcherStats()`.** Fold `LazyDFAStats` into the broader API
  when that lands.
- **Cache size estimation accuracy.** `estimateBytes` is approximate; if
  it under-counts significantly on real workloads, real heap may exceed
  configured budget. Validate empirically in `BenchmarkLazyDFA_CacheThrashing`
  (measure `runtime.MemStats.HeapAlloc` after fill, compare to `cacheBytes`).
