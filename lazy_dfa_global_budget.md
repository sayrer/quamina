# Lazy DFA: Global Budget and Shared Caches

## Global DFA Memory Limit

### What's measurable at state-creation time

When `makeState` or `getOrCreateState` runs, the memory cost is deterministic:

- **Fixed**: `lazyDFAState` struct = 97 bytes (4 slice headers + `cached` bool)
- **Variable per state**: `nfaStates` slice backing = `len(nfaStates) * 8` bytes
- **Variable per state**: `fieldTransitions` slice backing = `len(fieldTransitions) * 8` bytes
- **Cache key**: `len(nfaStates) * 8` bytes (stored as a `string` key in the map)
- **Variable, grows later**: `transKeys`/`transValues` grow as transitions are cached = 9 bytes per transition (1 + 8)

So at creation time you know everything except future transition growth. You could either account for transitions as they're appended (lines 163-164), or use a conservative estimate.

### A global limit via shared atomic

The simplest approach: a single `atomic.Int64` representing bytes remaining in a global budget. Every `lazyDFA` holds a pointer to it.

- **`getOrCreateState`**: Before caching a new state, compute its byte cost, `Add(-cost)`. If the result goes negative, `Add(+cost)` to undo and return `nil` (same as current cache-full behavior — the caller creates a temporary uncached state).
- **`step` appending transitions** (lines 163-164): `Add(-9)`. If negative, undo and skip caching the transition (the state remains valid, just the transition won't be cached next time).
- **No lock needed**: `atomic.Int64.Add` is a single instruction. The contention is minimal because state creation is the slow path — the hot path (cache hit at line 125) never touches the atomic.

This works well because the existing graceful degradation already handles "cache full" — uncached temporary states snap back to cached states when possible. A global atomic just changes *when* that kicks in from "per-cache state count" to "global byte budget."

### What about `Copy()`?

`Copy()` already creates a fresh `nfaBuffers`. You'd just pass the same `*atomic.Int64` pointer to every copy. The budget is inherently shared — goroutine A filling cache reduces what's available to goroutine B, which is exactly what you want for a global memory bound.

### The formula becomes trivial

```
global memory used by lazy DFA ≤ initial budget value
```

No need to know G or N. The atomic counter *is* the accounting. You set one number (say, 10 MB) and the system self-regulates regardless of how many goroutines or NFA start tables exist.

### One subtlety

Map overhead in Go (`ld.cache`) is not tracked by this scheme — each map entry has internal bookkeeping (~50-100 bytes depending on Go version). You could either add a flat per-entry surcharge to the cost calculation, or accept that the actual memory is ~1.5-2x the tracked amount and set the budget accordingly.

## Sharing Lazy DFA Caches Across Goroutines

### What mutates

There are three things that mutate in the current design:

1. **`ld.cache` map** — new entries added in `getOrCreateState` (line 116)
2. **`state.transKeys` / `state.transValues`** — appended in `computeStep` (lines 163-164)
3. **Scratch buffers** (`seenStates`, `seenFields`, `stepResult`, `sortBuf`, `keyBuf`) — per-call working memory

Item 3 must stay per-goroutine regardless. The question is whether 1 and 2 can be shared.

### The problem with sharing transitions

Lines 163-164 do `append` on a shared `lazyDFAState`. In Go, `append` may reallocate the backing array. If goroutine A is reading `transKeys` via `bytes.IndexByte` (line 125) while goroutine B appends, A could be reading a stale or partially-freed backing array. That's a data race with undefined behavior, not just a stale read.

### Two practical approaches

**Option A: Immutable states, shared map**

Make `lazyDFAState` immutable after creation. Instead of appending transitions to the state, move the transition cache into a per-goroutine overlay:

```
shared:        cache map[string]*lazyDFAState   (sync.RWMutex or sync.Map)
per-goroutine: transCache map[*lazyDFAState][256]*lazyDFAState  (or sparse equivalent)
```

The shared cache handles the expensive part — deduplicating NFA state sets and storing `fieldTransitions`/`nfaStates`. The per-goroutine overlay caches the `(state, byte) → nextState` transitions, which are cheap (just a pointer per entry) and input-dependent anyway. After warmup, both the shared map and the overlays are read-only.

This is clean but gives up the current design's nice property of `bytes.IndexByte` SIMD lookup on the state itself.

**Option B: Copy-on-write transitions with atomic pointer swap**

Replace the two slices with a single pointer to an immutable transition table:

```go
type transTable struct {
    keys   []byte
    values []*lazyDFAState
}

type lazyDFAState struct {
    trans            atomic.Pointer[transTable]
    fieldTransitions []*fieldMatcher
    nfaStates        []*faState
}
```

To add a transition: copy the old table, append, store via `atomic.Pointer.Store`. Readers call `atomic.Pointer.Load` then `bytes.IndexByte` — no lock, no race. Duplicate work (two goroutines computing the same transition simultaneously) is harmless since the result is deterministic, and one wins the store.

This preserves the SIMD fast path and the current `step` structure. The cost is an extra copy on the slow path (transition miss), but that's already the cold path doing NFA traversal and epsilon closures. The `atomic.Pointer` load on the hot path is essentially free on arm64/amd64.

### Input dependence doesn't actually hurt

Different goroutines seeing different inputs build *different transitions on the same states*. With option B, these transitions accumulate on the shared state — goroutine B benefits from transitions goroutine A discovered on the same DFA state, even if B never would have seen that byte value. The state space is bounded by the NFA powerset regardless of input diversity. The transition space is bounded by `states × 256`. More goroutines just warm the cache faster.

The one case where sharing doesn't help: if the goroutines are hitting completely different `startTable` pointers (different fields with different patterns). But that's the same as the unshared case — they'd never share anyway.
