# r8e/otter

[English] · [Français](README.fr.md)

Otter cache adapter for [**r8e**](../README.md). It implements the `r8e.Cache`
interface on top of [Otter](https://github.com/maypok86/otter) — a
high-performance, contention-free cache with per-entry TTL — so you can back
r8e's read-through cache policy (`r8e.WithCache` / `r8e.ReadThroughCache`) or the
standalone `r8e.StaleCache` with a production-grade cache.

## Install

```bash
go get github.com/byte4ever/r8e/otter
```

## Quick start

```go
import (
    "github.com/byte4ever/r8e"
    otteradapter "github.com/byte4ever/r8e/otter"
)

// The cache stores r8e.CacheEntry[V] (the freshness wrapper) keyed by string —
// the same key type r8e.WithCache uses.
cache := otteradapter.MustNew[string, r8e.CacheEntry[string]](
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

The first call for a key executes the downstream and stores the result; calls
within the freshness TTL are served from Otter without re-executing. See
[`examples/01-read-through-cache`](examples/01-read-through-cache).

## API

```go
func MustNew[K comparable, V any](cfg r8e.CacheConfig) r8e.Cache[K, V]
```

Returns an `r8e.Cache[K, V]` backed by an Otter cache with per-entry TTL. The
returned value exposes the three `r8e.Cache` methods:

| Method | Behaviour |
|---|---|
| `Get(key K) (V, bool)` | retrieves a cached value |
| `Set(key K, value V, ttl time.Duration)` | stores a value with a per-entry TTL |
| `Delete(key K)` | removes an entry |

## Notes

- Only `MaxSize` from `r8e.CacheConfig` is consumed (the cache capacity). The
  freshness TTL is applied **per `Set` call** by r8e, not read from the config.
- `MustNew` **panics** if the underlying Otter cache cannot be built (e.g.
  `MaxSize: 0`) — call it at startup, like other `Must*` constructors.
- For the read-through policy, instantiate the value type as
  `r8e.CacheEntry[V]`: `otteradapter.MustNew[string, r8e.CacheEntry[string]](cfg)`.
  Otter itself is reused unchanged — the `CacheEntry` wrapper carries r8e's
  freshness metadata.

See the [main r8e README](../README.md#cache) for the full cache-policy
documentation (read-through, stale-if-error, negative caching, refresh-ahead).
