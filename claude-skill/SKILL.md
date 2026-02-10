---
name: r8e
description: Guide for using the r8e Go resilience library. Use when writing, reviewing, or modifying code that uses github.com/byte4ever/r8e — including creating policies, composing resilience patterns (retry, circuit breaker, timeout, rate limiter, bulkhead, hedge, fallback, stale cache), classifying errors, wiring health/readiness, using the httpx adapter, or loading configuration from JSON. Also use when the user asks about resilience, fault tolerance, or retry patterns in Go.
---

# r8e — Go Resilience Library

r8e (r-esilienc-e) is a composable Go resilience library.
One generic `Policy[T]` type, one `Do()` method, seven composable patterns, automatic ordering.

**Import**: `github.com/byte4ever/r8e`

## Core API

```go
// Create a named policy (auto-registers with DefaultRegistry for health reporting)
policy := r8e.NewPolicy[T](name string, opts ...any) *Policy[T]

// Execute through the middleware chain
result, err := policy.Do(ctx, func(ctx context.Context) (T, error) { ... })

// One-off convenience (anonymous, not registered)
result, err := r8e.Do[T](ctx, fn, opts...)
```

Options are `any`-typed to support both generic (`WithFallback[T]`) and non-generic options in the same variadic.

Patterns are **auto-sorted** by priority (outermost to innermost):
Fallback > Timeout > CircuitBreaker > RateLimiter > Bulkhead > Retry > Hedge.

## Pattern Options

### Timeout

```go
r8e.WithTimeout(5 * time.Second)
```

Returns `r8e.ErrTimeout` if exceeded.

### Retry

```go
r8e.WithRetry(maxAttempts int, strategy BackoffStrategy, opts ...RetryOption)
```

**Strategies** (all take a base duration):
`r8e.ConstantBackoff(d)`, `r8e.ExponentialBackoff(d)`, `r8e.LinearBackoff(d)`, `r8e.ExponentialJitterBackoff(d)`, `r8e.BackoffFunc(func(attempt int) time.Duration)`.

**Options**: `r8e.MaxDelay(d)`, `r8e.PerAttemptTimeout(d)`, `r8e.RetryIf(func(error) bool)`.

Returns `r8e.ErrRetriesExhausted` wrapping the last error.

### Circuit Breaker

```go
r8e.WithCircuitBreaker(opts ...CircuitBreakerOption)
```

**Options**: `r8e.FailureThreshold(n)` (default 5), `r8e.RecoveryTimeout(d)` (default 30s), `r8e.HalfOpenMaxAttempts(n)` (default 1).

States: closed -> open (fast-fail `r8e.ErrCircuitOpen`) -> half-open -> closed.
Lock-free via `sync/atomic`.

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

### Hedge

```go
r8e.WithHedge(delay time.Duration)
```

Fires a second concurrent call after `delay`. Returns first success, cancels the other.

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

**Sentinel errors** (all implement `ResilienceError` interface with `IsResilience() bool`):
`r8e.ErrCircuitOpen`, `r8e.ErrRateLimited`, `r8e.ErrBulkheadFull`, `r8e.ErrTimeout`, `r8e.ErrRetriesExhausted`.

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
})
```

Synchronous, set once at construction. All fields optional (nil-safe).

## Health and Readiness

Named policies auto-register with `DefaultRegistry()`. Health is inferred from pattern state:
- Circuit breaker open -> `CriticalityCritical`, unhealthy
- Rate limiter saturated / bulkhead full -> `CriticalityDegraded`

```go
status := policy.HealthStatus() // PolicyStatus

// Hierarchical dependencies
dbPolicy := r8e.NewPolicy[*Result]("database",
    r8e.WithCircuitBreaker(),
    r8e.DependsOn(apiPolicy),
)

// Kubernetes /readyz endpoint
http.Handle("/readyz", r8e.ReadinessHandler(r8e.DefaultRegistry()))

// Custom registry
reg := r8e.NewRegistry()
policy := r8e.NewPolicy[string]("svc", r8e.WithRegistry(reg), ...)
http.Handle("/readyz", r8e.ReadinessHandler(reg))
```

## StaleCache (Standalone, Not Part of Policy)

Compose by wrapping `policy.Do()` inside `staleCache.Do()`.

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
      "rate_limit": 100,
      "bulkhead": 10,
      "hedge": "200ms"
    }
  }
}
```

```go
reg, err := r8e.LoadConfig("config.json")
policy := r8e.GetPolicy[string](reg, "payment-api",
    r8e.WithFallback[string]("unavailable"),  // code opts override config
    r8e.WithHooks(&r8e.Hooks{...}),
)
```

Backoff strategies: `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

You can embed `r8e.PolicyConfig` in your own config struct and call `r8e.BuildOptions(&pc)` directly.

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
github.com/byte4ever/r8e/httpx      # HTTP adapter
github.com/byte4ever/r8e/otter      # Otter cache adapter
github.com/byte4ever/r8e/ristretto  # Ristretto cache adapter
```

Examples: `examples/01-quickstart` through `examples/18-httpx-retry`.
