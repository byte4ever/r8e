# r8e/ristretto

[English] · [Français](README.fr.md)

Ristretto cache adapter for [**r8e**](../README.md). It implements the
`r8e.Cache` interface on top of
[Ristretto](https://github.com/dgraph-io/ristretto) — Dgraph's admission-based,
cost-aware cache — so you can back r8e's read-through cache policy
(`r8e.WithCache` / `r8e.ReadThroughCache`) or the standalone `r8e.StaleCache`
with a production-grade cache.

## Install

```bash
go get github.com/byte4ever/r8e/ristretto
```

## Quick start

```go
import (
    "github.com/byte4ever/r8e"
    ristrettoadapter "github.com/byte4ever/r8e/ristretto"
)

// The cache stores r8e.CacheEntry[V] (the freshness wrapper) keyed by string —
// the same key type r8e.WithCache uses. The key must satisfy the Key constraint.
cache := ristrettoadapter.MustNew[string, r8e.CacheEntry[string]](
    r8e.CacheConfig{MaxSize: 10_000},
)

policy := r8e.NewPolicy[string]("profiles",
    r8e.WithCache(
        cache,
        func(ctx context.Context) string { return userIDFromCtx(ctx) },
        30*time.Second, // freshness TTL
    ),
)
```

See [`examples/01-read-through-cache`](examples/01-read-through-cache).

## API

```go
func MustNew[K Key, V any](cfg r8e.CacheConfig) r8e.Cache[K, V]

type Key interface {
    uint64 | string | byte | int | int32 | uint32 | int64
}
```

Returns an `r8e.Cache[K, V]` backed by a Ristretto cache. `K` must satisfy `Key`
(the comparable subset of Ristretto key types). The returned value exposes the
three `r8e.Cache` methods:

| Method | Behaviour |
|---|---|
| `Get(key K) (V, bool)` | retrieves a cached value |
| `Set(key K, value V, ttl time.Duration)` | stores a value (cost 1) with a per-entry TTL |
| `Delete(key K)` | removes an entry |

## Notes

- Only `MaxSize` from `r8e.CacheConfig` is consumed. It bounds the **entry
  count**: each entry is stored with a fixed cost of 1, and `NumCounters` is sized
  at `10 × MaxSize` per Ristretto's recommendation. The freshness TTL is applied
  **per `Set` call** by r8e, not read from the config.
- **Async admission.** Ristretto admits writes through a buffer, so a value `Set`
  on one call may not be visible to the very next `Get`, and writes can be dropped
  under buffer pressure. This is safe with r8e: a dropped or not-yet-admitted write
  simply degrades to a cache miss (the read-through layer re-executes), never a
  wrong value. Otter offers stronger immediate-read-after-write semantics if you
  need them — see [`r8e/otter`](../otter/README.md).
- `MustNew` **panics** if the underlying Ristretto cache cannot be built — call it
  at startup.

See the [main r8e README](../README.md#cache) for the full cache-policy
documentation (read-through, stale-if-error, negative caching, refresh-ahead).
