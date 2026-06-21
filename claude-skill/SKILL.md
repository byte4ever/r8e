---
name: r8e
description: Guide for using the r8e Go resilience library. Use when writing, reviewing, or modifying code that uses github.com/byte4ever/r8e — including creating policies, composing resilience patterns (retry, circuit breaker, timeout, time budget, rate limiter, bulkhead, adaptive concurrency, hedge, request coalescing/singleflight, fallback, stale cache), classifying errors, wiring health/readiness, using the httpx adapter, or loading configuration from JSON. Also use when the user asks about resilience, fault tolerance, or retry patterns in Go.
---

# r8e — Go Resilience Library

r8e (r-esilienc-e) is a composable Go resilience library.
One generic `Policy[T]` type, one `Do()` method, seven composable patterns, automatic ordering.

**Import**: `github.com/byte4ever/r8e`

## Core API

```go
// Create a named policy (auto-registers with DefaultRegistry for health reporting)
policy := r8e.NewPolicy[T](name string, opts ...r8e.Option) *Policy[T]

// Execute through the middleware chain
result, err := policy.Do(ctx, func(ctx context.Context) (T, error) { ... })

// One-off convenience (anonymous, not registered)
result, err := r8e.Do[T](ctx, fn, opts...)
```

Options are `any`-typed to support both generic (`WithFallback[T]`) and non-generic options in the same variadic.

Patterns are **auto-sorted** by priority (outermost to innermost):
Fallback > Cache > Coalesce > Timeout > TimeBudget > CircuitBreaker > RateLimiter > Bulkhead/AdaptiveConcurrency > Retry > Hedge.
The retry budget is not a stage; it gates retries from within Retry. The time
budget stamps a ctx deadline that retry/hedge read. Bulkhead and
AdaptiveConcurrency share the concurrency slot and are mutually exclusive.

## Pattern Options

### Timeout

```go
r8e.WithTimeout(5 * time.Second)
```

Returns `r8e.ErrTimeout` if exceeded.

### Time Budget

```go
r8e.WithTimeBudget(350 * time.Millisecond)
```

One **total** time budget shared across retry + hedge. Before each retry, if the
backoff alone would overrun the remaining budget, retry stops early with
`r8e.ErrTimeBudgetExceeded` (wrapping the real downstream error) instead of
sleeping into a doomed attempt; a hedge is not fired once the budget is spent.
**Tighter than `PerAttemptTimeout`** (which bounds each attempt — the budget caps
the sum). **Cooperative**, measured against the policy `Clock`: it gates whether
more work starts but does not cancel an in-flight attempt — pair with
`WithTimeout` for a hard per-call deadline. Sits between Timeout and the inner
patterns (stamps a clock-based deadline into ctx that retry/hedge read).
**Requires `WithRetry` or `WithHedge`** (gates only those) — neither panics
`NewPolicy` with `r8e.ErrTimeBudgetWithoutConsumer`. Observability:
`OnTimeBudgetExceeded` hook + `TimeBudgetExceeded` metric.

### Retry

```go
r8e.WithRetry(maxAttempts int, strategy BackoffStrategy, opts ...RetryOption)
```

**Strategies** (all take a base duration):
`r8e.ConstantBackoff(d)`, `r8e.ExponentialBackoff(d)`, `r8e.LinearBackoff(d)`, `r8e.ExponentialJitterBackoff(d)`, `r8e.BackoffFunc(func(attempt int) time.Duration)`.

**Options**: `r8e.MaxDelay(d)`, `r8e.PerAttemptTimeout(d)`, `r8e.RetryIf(func(error) bool)`.

Returns `r8e.ErrRetriesExhausted` wrapping the last error.

**Retry-After**: if a failed attempt's error implements `r8e.RetryAfterProvider`
(`RetryAfter() (time.Duration, bool)`), retry honors that delay (±10% jitter,
capped by `MaxDelay`) over the computed backoff. Attach a fixed hint to any error
with `r8e.RetryAfterError(err, d)`, or implement the interface on your own type;
the httpx adapter's `StatusError` implements it from the HTTP `429`/`503`
`Retry-After` header (delay-seconds or HTTP-date), so httpx honors it
automatically. Only a strictly-positive delay counts as a hint.

### Retry Budget

```go
r8e.WithRetryBudget(opts ...RetryBudgetOption)          // per-policy
r8e.WithSharedRetryBudget(*RetryBudget)                 // shared across policies
r8e.NewRetryBudget(opts ...RetryBudgetOption) *RetryBudget
```

Adaptive token bucket gating retries (gRPC `retryThrottling` model): every
success adds `TokenRatio` tokens, every retryable failure removes one; retries
are suppressed while tokens are at or below half capacity. Lives inside Retry
(no separate priority) and **requires `WithRetry`** — a budget without retry
panics in `NewPolicy` (or `BuildOptions` returns `r8e.ErrRetryBudgetWithoutRetry`
for config-driven construction). A shared budget reports the same tokens/exhausted
state under each sharing policy's name (aggregate gauge with max/avg, not sum).
**Options**: `r8e.MaxTokens(n)` (default 10),
`r8e.TokenRatio(r)` (default 0.1). When exhausted it suppresses the retry and
returns the **real downstream error** (not a sentinel); first attempts always
proceed. Outcome-driven (no clock). Observability: `OnRetryBudgetExceeded` hook,
`RetryBudgetExceeded`/`RetryBudgetTokens` metrics, `retry_budget_exhausted`
health condition (degraded).

### Circuit Breaker

```go
r8e.WithCircuitBreaker(opts ...CircuitBreakerOption)
```

**Options**: `r8e.FailureThreshold(n)` (default 5), `r8e.RecoveryTimeout(d)` (default 30s), `r8e.HalfOpenMaxAttempts(n)` (default 1).

States: closed -> open (fast-fail `r8e.ErrCircuitOpen`) -> half-open -> closed.
State transitions are mutex-guarded (linearizable); half-open admits at most
`HalfOpenMaxAttempts` concurrent probes.

### Rate Limiter

```go
r8e.WithRateLimit(rate float64, opts ...RateLimitOption)
```

Token-bucket. `rate` = tokens/sec. Option: `r8e.RateLimitBlocking()` (wait instead of reject).
Returns `r8e.ErrRateLimited` in non-blocking mode.

### Bulkhead

```go
r8e.WithBulkhead(maxConcurrent int)
```

Returns `r8e.ErrBulkheadFull` when all slots occupied.

### Adaptive Concurrency

```go
r8e.WithAdaptiveConcurrency(opts ...AdaptiveOption)
```

Self-tuning concurrency limiter (Netflix **Gradient2**): samples each call's RTT,
lowers the limit when the current RTT rises above a smoothed baseline (queueing),
raises it when latency is steady. Returns `r8e.ErrConcurrencyLimited` when at the
limit. **Options**: `r8e.InitialLimit(n)` (default 20), `r8e.MinLimit(n)` (default
1), `r8e.MaxLimit(n)` (default 200), `r8e.RTTTolerance(f)` (default 1.5). Occupies
the bulkhead slot → **mutually exclusive with `WithBulkhead`**: both panics
`NewPolicy` with `r8e.ErrConcurrencyLimiterConflict` (or `BuildOptions` returns
it). Grows only while loaded (in-flight ≥ half the limit). Standalone:
`r8e.NewAdaptiveLimiter(clock, hooks, opts...)` + `Acquire()`/`Record(start)`.

### Hedge

```go
r8e.WithHedge(delay time.Duration)
```

Fires a second concurrent call after `delay`. Returns first success, cancels the other.

### Request Coalescing (singleflight)

```go
r8e.WithCoalesce(keyFn func(context.Context) string)
```

Collapses concurrent calls sharing a key into one shared execution; followers
wait for and share the leader's result (kills cache stampede). `keyFn` derives
the key from the call context — stamp request identity into `ctx` upstream and
read it back. An **empty** key opts that call out of coalescing. Sits just inside
Fallback and outside every other pattern, so duplicates share one trip through
the chain while each caller keeps its own fallback.

The shared call runs under a **detached context** (`context.WithoutCancel`): one
caller cancelling never aborts the group, and each caller still bails on its own
`ctx.Done()`. Detaching strips the deadline, so **`WithCoalesce` requires
`WithTimeout`** to bound the shared call. Two `NewPolicy` panics: a **nil** keyFn
→ `r8e.ErrCoalesceNilKeyFunc`; **no `WithTimeout`** → `r8e.ErrCoalesceWithoutTimeout`.
Not a cache (only dedups time-overlapping calls). Usable standalone via
`r8e.NewCoalescer[T](hooks)` + `c.Do(ctx, key, fn)` (bound `fn` yourself — no
policy timeout). Code-only — not expressible in `PolicyConfig` (the key function
is code), so absent from `BuildOptions`/`Reconfigure`.

### Read-Through Cache

```go
r8e.WithCache[T](cache Cache[string, CacheEntry[T]], keyFn func(context.Context) string,
    ttl time.Duration, opts ...CacheOption)   // opts: StaleIfError(d), NegativeCache(d)
```

Memoizes successful results. A **fresh hit short-circuits the whole chain**; a miss
executes and caches a success for `ttl`. `keyFn` derives the key from `ctx` (same
idiom as `WithCoalesce`, so one keyFn drives both); an **empty** key opts out. Sits
just inside Fallback and outside everything else, so a hit skips coalesce/timeout/…
and a **fallback value is never cached** (only a genuine downstream success). Pair
with `WithCoalesce` to collapse the miss stampede.

The backing `Cache` is parameterised by **`CacheEntry[T]`** (wrapper carrying age +
recorded error), e.g. `otter.MustNew[string, r8e.CacheEntry[T]](cfg)`. Freshness
uses the policy **`Clock`** (deterministic under a fake clock), not the cache's own
expiry. Three behaviours: **read-through** (fresh hit), **stale-if-error**
(`StaleIfError(d)` — past `ttl`, a value lingers `d` as a fallback; a stale call
revalidates but serves the stale value + fires `OnStaleServed` if that fails; RFC
5861), **negative caching** (`NegativeCache(d)` — a failure with no stale fallback
is cached `d` so repeats fast-fail with the recorded error). `r8e.ForceRefresh(ctx)`
bypasses the cached read for one call. Three `NewPolicy` panics: nil keyFn →
`ErrCacheNilKeyFunc`, nil cache → `ErrCacheNilCache`, ttl ≤ 0 →
`ErrCacheNonPositiveTTL`. Code-only (absent from `PolicyConfig`/`BuildOptions`/
`Reconfigure`). No health condition (healthy optimisation). Standalone via
`r8e.NewReadThroughCache[T](cache, ttl, opts...)` (set clock/hooks with
`CacheClock`/`CacheHooks`). Supersedes the standalone `StaleCache` for in-chain use.

### Fallback

```go
r8e.WithFallback[T](val T)                        // static value
r8e.WithFallbackFunc[T](func(error) (T, error))   // function
```

## Error Classification

**Key rule**: Unclassified errors are treated as transient (retriable). Only `Permanent()` stops retries.

```go
r8e.Transient(err)   // mark as retriable (rarely needed — this is the default)
r8e.Permanent(err)   // mark as non-retriable — stops retries immediately

r8e.IsTransient(err) // true for unclassified AND explicitly transient
r8e.IsPermanent(err) // true only for explicitly permanent
```

**Sentinel errors** (match with `errors.Is`, even when wrapped):
`r8e.ErrCircuitOpen`, `r8e.ErrRateLimited`, `r8e.ErrBulkheadFull`, `r8e.ErrConcurrencyLimited`, `r8e.ErrTimeout`, `r8e.ErrTimeBudgetExceeded`, `r8e.ErrRetriesExhausted`.

## Hooks

```go
r8e.WithHooks(&r8e.Hooks{
    OnRetry:            func(attempt int, err error) {},  // attempt is 1-indexed
    OnCircuitOpen:      func() {},
    OnCircuitClose:     func() {},
    OnCircuitHalfOpen:  func() {},
    OnRateLimited:      func() {},
    OnBulkheadFull:     func() {},
    OnBulkheadAcquired: func() {},
    OnBulkheadReleased: func() {},
    OnTimeout:          func() {},
    OnHedgeTriggered:   func() {},
    OnHedgeWon:         func() {},
    OnFallbackUsed:     func(err error) {},
    OnRetryBudgetExceeded: func() {},  // retry suppressed by the budget
    OnTimeBudgetExceeded:  func() {},  // retry stopped early by the time budget
    OnCoalesceLeader:   func() {},     // call ran a shared coalesced execution
    OnCoalesceFollower: func() {},     // call deduplicated into an in-flight one
    OnConcurrencyRejected:     func() {},     // adaptive limiter shed a call
    OnConcurrencyLimitChanged: func(limit int) {}, // adaptive limit retuned
    OnCacheHit:    func() {},  // served from cache (fresh value or negative entry)
    OnCacheMiss:   func() {},  // no fresh value; downstream executed
    OnCacheStored: func() {},  // successful result written to cache
    OnStaleServed: func() {},  // stale value served after a downstream failure
})
```

Synchronous, set once at construction. All fields optional (nil-safe).
`WithHooks(nil)` is ignored (no panic).

## Metrics

Every policy keeps counters + live gauges automatically (no hooks needed):

```go
m := policy.Metrics()              // r8e.PolicyMetrics for one policy
all := r8e.DefaultRegistry().Snapshot() // []r8e.PolicyMetrics, one per policy
```

`PolicyMetrics` has counters (`Retries`, `Timeouts`, `CircuitOpens`,
`CircuitCloses`, `CircuitHalfOpens`, `RateLimited`, `BulkheadRejected`,
`HedgesTriggered`, `HedgesWon`, `FallbacksUsed`, `RetryBudgetExceeded`,
`TimeBudgetExceeded`, `CoalesceLeaders`, `CoalesceFollowers`,
`ConcurrencyRejected`, `CacheHits`, `CacheMisses`, `CacheStores`,
`CacheStaleServed`) and gauges
(`CircuitState`, `BulkheadInUse`, `BulkheadCap`, `RetryBudgetTokens`,
`CoalesceInFlight`, `ConcurrencyLimit`, `ConcurrencyInFlight`, `Saturated`,
`Healthy`, `Criticality`).

Bridges: `r8ehttp.MetricsHandler(reg)` (JSON, stdlib) and
`r8eotel.Register(meter, reg)` (OpenTelemetry observable instruments, separate
module — keeps core dependency-free).

## Hot reload

Retune the parameters of patterns a policy ALREADY has, at runtime, without
rebuilding:

```go
err := policy.Reconfigure(r8e.PolicyConfig{RateLimit: ptr(50.0)})  // nil fields unchanged
err := reg.Reconfigure("payment-api", cfg)                          // by name
err := store.Reload("config.json")                                  // re-read file + retune live policies
```

Cannot add/remove patterns (chain is fixed) → configuring an absent pattern
returns `r8e.ErrPatternAbsent`; rebuild via GetPolicy/NewPolicy for structural
changes. CircuitBreaker/RateLimiter/Bulkhead/RetryBudget/AdaptiveLimiter also
expose direct `Reconfigure`. The retry budget reconfigures via
`PolicyConfig.RetryBudget` (`max_tokens`, `token_ratio`); the adaptive limiter via
`PolicyConfig.AdaptiveConcurrency` (`initial_limit`, `min_limit`, `max_limit`,
`rtt_tolerance`).

## Health and Readiness

Named policies auto-register with `DefaultRegistry()`. Health is inferred from pattern state:
- Circuit breaker open -> `CriticalityCritical`, unhealthy
- Rate limiter saturated / bulkhead full / retry budget exhausted -> `CriticalityDegraded`

`PolicyStatus.Conditions []string` lists ALL active conditions (order-independent); `State` is a deterministic most-severe summary derived from them.

**Readiness is opt-in.** By default a policy's health does NOT gate the readiness probe (an open breaker is reported but does not pull the pod). This avoids fleet-wide readiness flips when a shared dependency trips every replica's breaker at once. Gate only with `WithReadinessImpact()`, and rely on the probe's `failureThreshold` for hysteresis.

```go
status := policy.HealthStatus() // PolicyStatus{Healthy, State, Conditions, Criticality, AffectsReadiness, ...}

dbPolicy := r8e.NewPolicy[*Result]("database",
    r8e.WithCircuitBreaker(),
    r8e.WithReadinessImpact(),     // gate /readyz on this policy
    r8e.DependsOn(apiPolicy),
)

// /readyz gates traffic (503 only when a readiness-impacting policy is critical).
http.Handle("/readyz", r8ehttp.ReadinessHandler(r8e.DefaultRegistry()))
// /healthz is informational: full report, always 200, never gates.
http.Handle("/healthz", r8ehttp.HealthHandler(r8e.DefaultRegistry()))

report := reg.Health() // r8e.HealthReport{Status: "healthy"|"degraded"|"unhealthy", Policies}
```

## StaleCache (Standalone, Not Part of Policy)

For caching **inside** a policy chain prefer **`WithCache`** (Read-Through Cache
above) — it adds read-through hits + negative caching on top of this same
stale-on-error behaviour as a composable pattern. `StaleCache` remains for
standalone, non-policy use. Compose by wrapping `policy.Do()` inside `staleCache.Do()`.

```go
cache := otter.MustNew[string, *Data](r8e.CacheConfig{MaxSize: 10_000})
sc := r8e.NewStaleCache(cache, 5*time.Minute,
    r8e.OnStaleServed[string, *Data](func(key string) {}),    // receives key only
    r8e.OnCacheRefreshed[string, *Data](func(key string) {}), // receives key only
)

result, err := sc.Do(ctx, "product-42", func(ctx context.Context, key string) (*Data, error) {
    return policy.Do(ctx, func(ctx context.Context) (*Data, error) {
        return fetchData(ctx, key)
    })
})
```

**Cache interface** (implement for custom backends):
```go
type Cache[K comparable, V any] interface {
    Get(key K) (V, bool)
    Set(key K, value V, ttl time.Duration)
    Delete(key K)
}
```

Built-in adapters: `github.com/byte4ever/r8e/otter` (`otter.MustNew[K, V](cfg)`) and `github.com/byte4ever/r8e/ristretto` (`ristretto.MustNew[K, V](cfg)`, K constrained to `uint64|string|byte|int|int32|uint32|int64`).

## httpx — HTTP Adapter

```go
import "github.com/byte4ever/r8e/httpx"

classifier := func(code int) httpx.ErrorClass {
    switch {
    case code >= 200 && code < 300:
        return httpx.Success
    case code == 429, code >= 500:
        return httpx.Transient
    default:
        return httpx.Permanent
    }
}

client := httpx.NewClient("api", http.DefaultClient, classifier,
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
)

resp, err := client.Do(ctx, req)
// Transient: body drained+closed for connection reuse during retries
// Permanent: body preserved but caller must close it
// Access status: var se *httpx.StatusError; errors.As(err, &se)
// StatusError.RetryAfter() parses the Retry-After header; retry honors it
// automatically (over the configured backoff) on 429/503.
```

## Presets

```go
r8e.StandardHTTPClient()    // timeout 5s, retry 3x exp 100ms, CB 5/30s
r8e.AggressiveHTTPClient()  // timeout 2s, retry 5x exp 50ms (max 5s), CB 3/15s, bulkhead 20

// Override from preset
policy := r8e.NewPolicy[T]("api",
    append(r8e.StandardHTTPClient(), r8e.WithTimeout(10*time.Second))...,
)
```

## JSON Configuration

```json
{
  "policies": {
    "payment-api": {
      "timeout": "2s",
      "circuit_breaker": {
        "failure_threshold": 5,
        "recovery_timeout": "30s",
        "half_open_max_attempts": 2
      },
      "retry": {
        "max_attempts": 3,
        "backoff": "exponential",
        "base_delay": "100ms",
        "max_delay": "5s"
      },
      "retry_budget": { "max_tokens": 10, "token_ratio": 0.1 },
      "rate_limit": 100,
      "bulkhead": 10,
      "hedge": "200ms"
    }
  }
}
```

```go
store, err := r8econf.Load("config.json")
policy, err := r8econf.GetPolicy[string](store, "payment-api",
    r8e.WithFallback("unavailable"),  // code opts override config
    r8e.WithHooks(&r8e.Hooks{...}),
)
```

Backoff strategies: `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

You can embed `r8e.PolicyConfig` in your own config struct and call `r8e.BuildOptions(&pc)` directly. `store.Reload(path)` re-reads the file and hot-reloads already-built policies (see Hot reload).

## Testing

Inject a fake `Clock` for deterministic tests:

```go
policy := r8e.NewPolicy[string]("test",
    r8e.WithClock(fakeClock),  // implements r8e.Clock interface
    r8e.WithRetry(3, r8e.ExponentialBackoff(time.Second)),
)
```

## Project Structure

```
github.com/byte4ever/r8e            # core (zero external deps)
github.com/byte4ever/r8e/r8ehttp    # net/http edge: ReadinessHandler, MetricsHandler
github.com/byte4ever/r8e/r8econf    # os+JSON edge: Load, GetPolicy, LoadCacheConfig, Store.Reload
github.com/byte4ever/r8e/httpx      # HTTP client adapter
github.com/byte4ever/r8e/r8eotel    # OpenTelemetry metrics bridge (separate module)
github.com/byte4ever/r8e/otter      # Otter cache adapter
github.com/byte4ever/r8e/ristretto  # Ristretto cache adapter
```

Examples: `examples/01-quickstart` through `examples/23-retry-after`.
