# Lazy DFA: Memory Budget vs State Count

## Problem

The lazy DFA cache currently caps at 1000 states (`maxLazyDFAStates`). Once
exceeded, the cache is permanently disabled and matching falls back to full NFA
traversal. This is an arbitrary limit — it doesn't account for the actual memory
cost of each state, which varies significantly.

## Per-State Memory

Each `lazyDFAState` contains:

| Component | Size (bytes) | Notes |
|---|---|---|
| `transitions [256]*lazyDFAState` | 2048 | Fixed. 256 pointers, mostly nil. |
| Struct overhead (slice headers) | 48 | Two slice headers (24 bytes each) |
| `nfaStates` backing array | `N * 8` | Variable. N = number of NFA states in this DFA state. |
| `fieldTransitions` backing array | `M * 8` | Variable. M = number of field matchers. |
| Map key (string) | `N * 8` | Stored in the `cache` map. |

A typical small state (3 NFA states, 1 field transition) costs ~2,152 bytes.
At 1000 states, total cache memory is ~2.1 MB.

But states are not uniform. A DFA state created from 200 overlapping NFA states
costs significantly more than one from 3. The fixed 1000-state limit can't
distinguish between a cache of 1000 tiny states (well under 2 MB) and 100 huge
states (well over 2 MB).

## Proposed: Memory Budget

Replace the state count limit with a byte budget, similar to RE2's DFA cache
(which defaults to 8 MB). A 2 MB budget for quamina would be reasonable given
the per-goroutine storage model.

```go
const maxLazyDFABytes = 2 * 1024 * 1024 // 2 MB

type lazyDFA struct {
    // ...
    memUsed int64
}
```

On each state creation, compute the actual memory cost:

```go
stateBytes := int64(unsafe.Sizeof(lazyDFAState{})) +
    int64(len(nfaStates)) * 8 +        // nfaStates backing array
    int64(len(fieldTransitions)) * 8 +  // fieldTransitions backing array
    int64(len(key))                     // map key string

ld.memUsed += stateBytes
if ld.memUsed > maxLazyDFABytes {
    ld.disabled = true
    return nil
}
```

## Benefits

- **Scales with actual cost**: A cache of many small states stays active longer;
  a cache of few large states hits the limit sooner. Both are bounded to the
  same memory.
- **Predictable resource usage**: Users can reason about memory impact
  independent of pattern complexity.
- **Matches industry practice**: RE2 uses an 8 MB budget for its lazy DFA cache.
  tcpdump/libpcap BPF JIT uses fixed memory budgets. Memory budgets are the
  standard approach for caching computed automaton states.

## Benchmark Evidence

The 1000-state limit causes a performance cliff at ~256 overlapping patterns.
With `BenchmarkShellstyleManyMatchers`:

| Patterns | ns/op | Notes |
|---|---|---|
| 128 | 13,682 | Lazy DFA active, fast |
| 256 | 13,603,859 | State limit hit, **1000x slower** |
| 512 | 60,875,621 | Pure NFA traversal |
| 1024 | 251,441,885 | Pure NFA traversal |

The cliff from 128 to 256 patterns is entirely caused by exceeding the state
limit. A memory budget would allow the cache to remain active as long as the
actual memory cost is within bounds, avoiding this arbitrary cutoff.
