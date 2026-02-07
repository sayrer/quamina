# Lazy DFA State Dynamics

## How States Are Created

A lazy DFA state represents a **set of simultaneously active NFA states**. When matching input against a pattern like `*a*b*`:

```
NFA structure:
  [Spinner1] --ε--> [Spinner2] --ε--> [Spinner3]
       |                |                |
       a                b             (end)
       ↓                ↓
   [Escape1]        [Escape2]
```

Each DFA state is a unique combination like:
- `{Spinner1}` - haven't matched anything yet
- `{Spinner1, Escape1}` - might have matched 'a', or still looking
- `{Spinner1, Escape1, Spinner2}` - definitely matched 'a', now in second spinner
- `{Spinner1, Escape1, Spinner2, Escape2}` - matched both 'a' and 'b'

## Pattern Complexity Effect

**Single pattern `*a*b*`**: ~5-7 DFA states
- Limited combinations because NFA states have dependencies
- Can't be in Spinner2 without having passed Escape1

**Multiple overlapping patterns** on same field:
```
Pattern 1: *ab*
Pattern 2: *bc*
Pattern 3: *abc*
```

The merged NFA has independent subgraphs that can combine:
- Could be matching "ab" in pattern 1 AND "bc" in pattern 2 simultaneously
- Creates cross-product of possibilities
- 6 overlapping patterns → 40 states

**Many patterns with shared characters**:
```
27 patterns like *a*, *b*, *ab*, *abc*, *bcd*, etc.
```
- Merged NFA has many parallel paths
- Each input byte activates different combinations
- 630+ states after 100 diverse inputs

## Input Diversity Effect

The input determines **which DFA states get discovered**:

```
Input "abc" on pattern *a*b*:
  Start → {S1}
  'a'   → {S1, E1, S2}     ← new DFA state created
  'b'   → {S1, E1, S2, E2} ← new DFA state created
  'c'   → {S1, E1, S2, E2} ← cache hit (same state)
```

**Uniform inputs** (e.g., always "foobar"):
- Explore same path repeatedly
- Few DFA states, high cache hit rate (99%+)

**Diverse inputs** (e.g., random strings):
- Each unique prefix may create new state combinations
- More DFA states, lower hit rate initially
- Eventually saturates as all reachable states are discovered

## The Saturation Effect

For a fixed NFA, there's a **maximum number of reachable DFA states**. This is bounded by:
- 2^n where n = number of NFA states (theoretical max)
- In practice, much smaller due to NFA structure constraints

Once all reachable states are cached, hit rate goes to ~100%.

## Why 1000 is Reasonable

| Scenario | States | Memory | Notes |
|----------|--------|--------|-------|
| Single wildcard pattern | 5-12 | 10-25 KB | Always fine |
| Few overlapping patterns | 20-50 | 40-100 KB | Typical usage |
| Many overlapping patterns | 100-300 | 200-600 KB | Heavy usage |
| Pathological (27+ patterns, diverse inputs) | 1000+ | 2+ MB | Limit protects |

The limit triggers fallback to NFA traversal, which:
- Still produces correct results
- Is slower but not catastrophically so
- Prevents unbounded memory growth

## Trade-offs for State Limit Options

### Option 1: Keep at 1000
- Pro: Handles most real workloads entirely in cache
- Pro: 2 MB per goroutine is acceptable for most systems
- Con: Could be wasteful if typical usage is 50 states

### Option 2: Lower to 200-500
- Pro: Saves memory (400 KB - 1 MB per goroutine)
- Pro: Earlier fallback means less time spent on cache misses
- Con: More fallbacks for legitimate heavy patterns

### Option 3: Configurable
```go
q, _ := New(WithLazyDFAStateLimit(500))
```
- Pro: User can tune for their workload
- Con: Another knob to understand/misconfigure

### Option 4: Adaptive
```go
// Start at 100, grow if hit rate > 90%, cap at 1000
if hitRate > 0.9 && stateCount < maxCap {
    limit = min(limit * 2, maxCap)
}
```
- Pro: Automatically finds right size
- Pro: Saves memory for simple patterns
- Con: More complex, potential for oscillation
