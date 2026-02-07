# r8e

**Stop writing retry loops. Start shipping resilient services.**

r8e (_resilience_) gives you timeout, retry, circuit breaker, rate limiter, bulkhead, hedged requests, stale cache, and fallback — all composable into a single policy with one line of code. Zero dependencies. Lock-free internals. 100% test coverage.

[![Go Reference](https://pkg.go.dev/badge/github.com/byte4ever/r8e.svg)](https://pkg.go.dev/github.com/byte4ever/r8e)
[![Go Report Card](https://goreportcard.com/badge/github.com/byte4ever/r8e)](https://goreportcard.com/report/github.com/byte4ever/r8e)
![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)

```go
policy := r8e.NewPolicy[string]("payments",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithFallback("service unavailable"),
)
result, err := policy.Do(ctx, callPaymentGateway)
```

That's it. Patterns are auto-sorted into the correct execution order. The circuit breaker reports health to your Kubernetes `/readyz` endpoint. Hooks feed your metrics pipeline. And when your 3 AM page fires, `r8e.ErrCircuitOpen` tells you exactly what happened.

```bash
go get github.com/byte4ever/r8e
```

## Why r8e?

- **One policy, all patterns** — compose any combination; r8e handles the ordering
- **Production-grade** — lock-free atomics, zero allocations on the hot path, 100% test coverage
- **Kubernetes-native** — built-in health reporting with hierarchical dependencies and a `/readyz` handler
- **Observable** — 14 lifecycle hooks for logging, metrics, and alerting
- **Testable** — `Clock` interface lets you control time in tests, no `time.Sleep` flakiness
- **Configurable** — define policies in code, JSON, or use ready-made presets
- **Zero dependencies** — only the Go standard library

## Features

| Pattern | What it does |
|---|---|
| **Timeout** | Cancel slow calls after a deadline |
| **Retry** | Retry transient failures with pluggable backoff (constant, exponential, linear, jitter) |
| **Circuit Breaker** | Fast-fail when a dependency is down, auto-recover via half-open probe |
| **Rate Limiter** | Token-bucket throughput control (reject or blocking mode) |
| **Bulkhead** | Semaphore-based concurrency limiting |
| **Hedged Requests** | Fire a second call after a delay to reduce tail latency |
| **Stale Cache** | Serve last-known-good value on failure |
| **Fallback** | Static value or function fallback as last resort |

Plus: automatic pattern ordering, JSON config, presets, health & readiness, hooks, `Clock` for deterministic tests.

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/byte4ever/r8e"
)

func main() {
    policy := r8e.NewPolicy[string]("my-api",
        r8e.WithTimeout(2*time.Second),
        r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
        r8e.WithCircuitBreaker(),
    )

    result, err := policy.Do(context.Background(), func(ctx context.Context) (string, error) {
        return "hello, resilience!", nil
    })
    fmt.Println(result, err) // hello, resilience! <nil>
}
```

## Resilience Patterns

### Timeout

Cancel slow calls after a deadline. If the function doesn't complete in time, `r8e.ErrTimeout` is returned.

```go
policy := r8e.NewPolicy[string]("timeout-example",
    r8e.WithTimeout(2*time.Second),
)

result, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
    // ctx will be cancelled after 2s
    time.Sleep(5 * time.Second)
    return "too slow", nil
})
// err == r8e.ErrTimeout
```

### Retry

Retry transient failures with pluggable backoff strategies. Errors wrapped with `r8e.Permanent()` stop retries immediately.

**Backoff strategies:**

| Strategy | Formula | Use case |
|---|---|---|
| `ConstantBackoff(d)` | `d` | Fixed interval polling |
| `ExponentialBackoff(base)` | `base * 2^attempt` | Standard retry |
| `LinearBackoff(step)` | `step * (attempt+1)` | Gradual ramp-up |
| `ExponentialJitterBackoff(base)` | `rand[0, base * 2^attempt]` | Prevent thundering herd |

```go
policy := r8e.NewPolicy[string]("retry-example",
    r8e.WithRetry(4, r8e.ExponentialBackoff(200*time.Millisecond),
        r8e.MaxDelay(5*time.Second),
        r8e.PerAttemptTimeout(1*time.Second),
        r8e.RetryIf(func(err error) bool {
            return !errors.Is(err, errNotFound)
        }),
    ),
)
```

### Circuit Breaker

Fast-fail when a dependency is unhealthy. After `FailureThreshold` consecutive failures, the breaker opens. After `RecoveryTimeout`, it enters half-open state and allows a probe. `HalfOpenMaxAttempts` successful probes close the breaker.

```go
policy := r8e.NewPolicy[string]("cb-example",
    r8e.WithCircuitBreaker(
        r8e.FailureThreshold(3),
        r8e.RecoveryTimeout(10*time.Second),
        r8e.HalfOpenMaxAttempts(2),
    ),
)

_, err := policy.Do(ctx, callDownstream)
if errors.Is(err, r8e.ErrCircuitOpen) {
    // downstream is down, fail fast
}
```

### Rate Limiter

Token-bucket rate limiter. Default mode rejects with `r8e.ErrRateLimited`; blocking mode waits for a token.

```go
// Reject mode (default): 10 requests/second
policy := r8e.NewPolicy[string]("rl-reject",
    r8e.WithRateLimit(10),
)

// Blocking mode: wait for a token
policy = r8e.NewPolicy[string]("rl-blocking",
    r8e.WithRateLimit(10, r8e.RateLimitBlocking()),
)
```

### Bulkhead

Limit concurrent access to a resource. Returns `r8e.ErrBulkheadFull` when at capacity.

```go
policy := r8e.NewPolicy[string]("bulkhead-example",
    r8e.WithBulkhead(5), // max 5 concurrent calls
)
```

### Hedged Request

Fire a second concurrent call after a delay. The first response wins; the other is cancelled. Reduces tail latency.

```go
policy := r8e.NewPolicy[string]("hedge-example",
    r8e.WithHedge(100*time.Millisecond),
)
```

### Stale Cache

Cache successful results and serve them when subsequent calls fail, as long as the cached value is within TTL.

```go
policy := r8e.NewPolicy[string]("cache-example",
    r8e.WithStaleCache(5*time.Minute),
)
```

### Fallback

Last line of defence. Return a static value or call a fallback function when everything else fails.

```go
// Static fallback
policy := r8e.NewPolicy[string]("static-fb",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithFallback("default-value"),
)

// Function fallback
policy = r8e.NewPolicy[string]("func-fb",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithFallbackFunc(func(err error) (string, error) {
        return "computed from: " + err.Error(), nil
    }),
)
```

## Composing Patterns

Combine any patterns in a single policy. `r8e` automatically sorts them by priority so the execution order is always correct regardless of the order you specify options.

```go
policy := r8e.NewPolicy[string]("composed",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeout(5*time.Second),
    r8e.WithCircuitBreaker(),
    r8e.WithBulkhead(10),
    r8e.WithRateLimit(100),
    r8e.WithFallback("fallback"),
)
```

### Execution Order

Patterns are auto-sorted by priority. The outermost middleware executes first:

```
Request
  → Fallback          (outermost — catches final error)
    → Stale Cache     (serves cached value on failure)
      → Timeout       (global deadline)
        → Circuit Breaker  (fast-fail if open)
          → Rate Limiter   (throttle throughput)
            → Bulkhead     (limit concurrency)
              → Retry       (retry transient failures)
                → Hedge     (innermost — races redundant calls)
                  → fn()    (your function)
```

## Error Classification

Classify errors to control retry behavior:

```go
// Transient errors are retried (this is the default for unclassified errors)
return r8e.Transient(fmt.Errorf("connection reset"))

// Permanent errors stop retries immediately
return r8e.Permanent(fmt.Errorf("invalid API key"))

// Check classification
r8e.IsTransient(err)  // true for unclassified and explicitly transient errors
r8e.IsPermanent(err)  // true only for explicitly permanent errors
```

## Hooks & Observability

Set lifecycle callbacks to integrate with your logging, metrics, or alerting systems:

```go
policy := r8e.NewPolicy[string]("observed",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithHooks(r8e.Hooks{
        OnRetry:        func(attempt int, err error) { log.Printf("retry #%d: %v", attempt, err) },
        OnCircuitOpen:  func() { log.Println("circuit breaker opened") },
        OnCircuitClose: func() { log.Println("circuit breaker closed") },
        OnTimeout:      func() { log.Println("request timed out") },
        OnRateLimited:  func() { log.Println("rate limited") },
        OnFallbackUsed: func(err error) { log.Printf("fallback used: %v", err) },
    }),
)
```

Available hooks: `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnRateLimited`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnTimeout`, `OnStaleServed`, `OnCacheRefreshed`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`.

## Health & Readiness

Policies automatically report health status. Wire up a Kubernetes `/readyz` endpoint in a few lines:

```go
import "net/http"

// Policies auto-register with the default registry
apiPolicy := r8e.NewPolicy[string]("api-gateway",
    r8e.WithCircuitBreaker(),
)
dbPolicy := r8e.NewPolicy[string]("database",
    r8e.WithCircuitBreaker(),
    r8e.DependsOn(apiPolicy), // hierarchical dependency
)

// Expose readiness endpoint
http.Handle("/readyz", r8e.ReadinessHandler(r8e.DefaultRegistry()))
```

Check health programmatically:

```go
status := apiPolicy.HealthStatus()
fmt.Println(status.Healthy)     // true/false
fmt.Println(status.State)       // "healthy", "circuit_open", etc.
fmt.Println(status.Criticality) // CriticalityNone, CriticalityDegraded, CriticalityCritical
```

## Configuration

Load policies from a JSON file:

```json
{
  "policies": {
    "payment-api": {
      "timeout": "2s",
      "circuit_breaker": {
        "failure_threshold": 5,
        "recovery_timeout": "30s"
      },
      "retry": {
        "max_attempts": 3,
        "backoff": "exponential",
        "base_delay": "100ms",
        "max_delay": "30s"
      },
      "rate_limit": 100,
      "bulkhead": 10,
      "stale_cache": "5m"
    }
  }
}
```

```go
reg, err := r8e.LoadConfig("config.json")
if err != nil {
    log.Fatal(err)
}

// Get a typed policy — config options are merged with code-level options
policy := r8e.GetPolicy[string](reg, "payment-api",
    r8e.WithFallback("service unavailable"),
)
```

Supported backoff strategies in config: `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

## Presets

Ready-made option bundles for common scenarios:

```go
// Standard: 5s timeout, 3 retries (100ms exp backoff), CB (5 failures, 30s recovery)
p := r8e.NewPolicy[string]("api", r8e.StandardHTTPClient()...)

// Aggressive: 2s timeout, 5 retries (50ms exp, 5s cap), CB (3 failures, 15s), bulkhead(20)
p = r8e.NewPolicy[string]("fast-api", r8e.AggressiveHTTPClient()...)

// Cached: StandardHTTPClient + 5min stale cache
p = r8e.NewPolicy[string]("cached-api", r8e.CachedClient()...)
```

## Convenience Function

For one-off calls without creating a named policy:

```go
result, err := r8e.Do[string](ctx, myFunc,
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
)
```

## Testing

The `Clock` interface allows deterministic testing by substituting fake time:

```go
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    NewTimer(d time.Duration) Timer
}

// Use in tests:
policy := r8e.NewPolicy[string]("test",
    r8e.WithClock(fakeClock),
    r8e.WithRetry(3, r8e.ExponentialBackoff(time.Second)),
)
```

## Examples

See the [`examples/`](examples/) directory for runnable examples demonstrating each feature:

```bash
go run ./examples/01-quickstart/
go run ./examples/02-retry/
go run ./examples/03-circuit-breaker/
go run ./examples/04-timeout/
go run ./examples/05-rate-limiter/
go run ./examples/06-bulkhead/
go run ./examples/07-hedge/
go run ./examples/08-stale-cache/
go run ./examples/09-fallback/
go run ./examples/10-full-policy/
go run ./examples/11-error-classification/
go run ./examples/12-hooks/
go run ./examples/13-health-readiness/
go run ./examples/14-config/
go run ./examples/15-presets/
go run ./examples/16-convenience-do/
```

## License

MIT
