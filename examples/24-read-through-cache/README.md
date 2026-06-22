*[Lire en Français](README.fr.md)*

# Example 24 — Read-Through Cache

Demonstrates `WithCache`, the read-through cache policy that folds four behaviours
behind one option so a hot key doesn't turn every request into a downstream
round-trip.

## What it demonstrates

The cache is keyed off the call context (the same idiom as Request Coalescing),
stores values as `r8e.CacheEntry[T]` wrappers, and measures freshness against the
policy's `Clock`. A tiny in-memory `mapCache` stands in for a real otter or
ristretto adapter. The example walks four sections, each resetting the backend
call counter so you can see exactly when the downstream was touched:

1. **Read-through** — the first call misses and populates; the second lands within
   the 50ms fresh TTL and is served from cache, so the backend is called once.
2. **ForceRefresh** — `r8e.ForceRefresh(ctx)` returns a child context that makes
   one call bypass the cached read and repopulate on success — the escape hatch
   when you need the authoritative value now.
3. **Stale-if-error** — after the value ages past the fresh TTL and the backend
   breaks, the failing revalidation serves the **last-known-good** value instead
   of the error (RFC 5861 stale-if-error), so a brief outage degrades to slightly
   stale data.
4. **Negative caching** — a never-successful key has no stale value to fall back
   on, so its failure is cached briefly; the next call fast-fails from that
   negative entry instead of hammering the broken backend.

Finally it prints the cache metrics (`hits`, `misses`, `stores`, `stale_served`).

## Key concepts

| Concept | Detail |
|---|---|
| `WithCache(cache, key, ttl, ...)` | Read-through cache; a fresh hit short-circuits the whole chain, a miss runs the chain and caches a successful result |
| Context key function | The key comes from the call context (`resourceKey`), so the same idiom drives both cache and coalesce; an empty key opts a call out |
| `r8e.CacheEntry[T]` | The wrapper the cache stores, carrying each entry's age and any recorded error so fresh/stale/negative can be told apart |
| `StaleIfError(d)` | Past the fresh TTL, a failed revalidation serves the stale value for `d` instead of erroring, firing `OnStaleServed` |
| `NegativeCache(d)` | A failure with no stale fallback is itself cached for `d`, so a known-bad key fast-fails |
| `ForceRefresh(ctx)` | Child context that bypasses the cached read for one call and repopulates on success |

## When to use

- Read-heavy lookups of slow or rate-limited dependencies where the same keys
  repeat (catalogs, profiles, config) — turn repeated reads into cheap hits.
- Where serving slightly stale data through a brief outage beats erroring
  (stale-if-error), or where known-bad keys should stop hammering the backend
  (negative caching).
- Pair with `WithCoalesce(key)` + `WithTimeout` to also collapse a concurrent miss
  stampede into one downstream call (see example 20).

## Run

```bash
go run ./examples/24-read-through-cache/
```

## Expected output

Four labelled sections. Read-through reports the backend was called once (second
was a cache hit); ForceRefresh shows one backend call; stale-if-error serves the
prior value while the backend is broken; negative caching fast-fails the second
call without a backend hit. The closing metrics line summarises the run.
