# Example 08 — Stale Cache

Demonstrates the standalone keyed stale-on-error cache (`StaleCache`). On
success, results are cached per key. On failure, the last-known-good value
for that key is served if available.

## What it demonstrates

Three scenarios illustrate stale cache behavior:

1. **Success — cache populated:** The first call to key `"user:1"` succeeds.
   The result is stored in the cache and the `OnCacheRefreshed` hook fires.

2. **Failure — stale served:** The downstream is toggled to fail. The second
   call to the same key `"user:1"` fails, but the cache holds the previous
   value. The stale result is returned (no error), and the `OnStaleServed`
   hook fires.

3. **Failure, different key — no cache:** A call to key `"user:2"` fails.
   Since no cached value exists for this key, the original error propagates
   to the caller.

The example uses a simple in-memory `map` as the cache backend to show the
`Cache[K, V]` interface in action. In production, use a proper cache adapter
like `otter` or `ristretto`.

## Key concepts

| Concept | Detail |
|---|---|
| `StaleCache[K, V]` | Standalone keyed cache wrapper — not part of `Policy` |
| `Cache[K, V]` | Interface that cache backends implement: `Get`, `Set`, `Delete` |
| `NewStaleCache` | Constructor taking a `Cache` backend, a TTL, and optional hooks |
| `OnStaleServed` | Hook fired when a stale cached value is served instead of an error |
| `OnCacheRefreshed` | Hook fired when a cache entry is updated after a successful call |
| Per-key isolation | Each key has its own cached value; a miss on one key doesn't affect others |

## Composing with Policy

`StaleCache` is standalone. To combine it with a `Policy`, call `policy.Do`
inside `staleCache.Do`:

```go
result, err := sc.Do(ctx, key, func(ctx context.Context, k string) (string, error) {
    return policy.Do(ctx, func(ctx context.Context) (string, error) {
        return fetchData(ctx, k)
    })
})
```

## Run

```bash
go run ./examples/08-stale-cache/
```

## Expected output

```
=== Call 1: Success (populates cache) ===
  [hook] cache refreshed for key="user:1"
  result: "fresh data for user:1 at ...", err: <nil>

=== Call 2: Failure (served from stale cache) ===
  [hook] serving stale data for key="user:1"
  result: "fresh data for user:1 at ...", err: <nil>

=== Call 3: Different key, no cache ===
  err: downstream unavailable (no cached value for this key)
```
