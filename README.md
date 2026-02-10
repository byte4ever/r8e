*[Lire en Francais](README.fr.md)*

# r8e

**Stop writing retry loops. Start shipping resilient services.**

r8e — short for r(esilienc)e, just like k8s stands for k(ubernete)s — gives you timeout, retry, circuit breaker, rate limiter, bulkhead, hedged requests, and fallback — all composable into a single policy with one line of code. A standalone keyed stale cache with pluggable cache backends complements the policy chain. Zero dependencies. Lock-free internals. 100% test coverage.

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
- **Observable** — 12 lifecycle hooks on Policy, plus per-StaleCache hooks
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
| **Stale Cache** | Serve last-known-good value per key on failure (standalone wrapper with pluggable cache backends) |
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

`StaleCache[K, V]` is a standalone, keyed stale-on-error wrapper. On success it stores the result in a pluggable `Cache[K, V]` backend. On failure it serves the last-known-good value for that key (if within TTL).

The `Cache[K, V]` interface that backends must implement:

```go
type Cache[K comparable, V any] interface {
    Get(key K) (V, bool)
    Set(key K, value V, ttl time.Duration)
    Delete(key K)
}
```

Usage with the Otter adapter:

```go
import (
    "github.com/byte4ever/r8e"
    otteradapter "github.com/byte4ever/r8e/otter"
)

// Create cache backend
cache := otteradapter.New[string, string](r8e.CacheConfig{MaxSize: 10_000})

// Create stale cache with hooks
sc := r8e.NewStaleCache(cache, 5*time.Minute,
    r8e.OnStaleServed[string, string](func(key string) {
        log.Printf("served stale value for key %q", key)
    }),
    r8e.OnCacheRefreshed[string, string](func(key string) {
        log.Printf("refreshed cache for key %q", key)
    }),
)

// Compose with a Policy — call policy.Do inside staleCache.Do
policy := r8e.NewPolicy[string]("pricing-api",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
)

result, err := sc.Do(ctx, "product-42", func(ctx context.Context, key string) (string, error) {
    return policy.Do(ctx, func(ctx context.Context) (string, error) {
        return fetchPrice(ctx, key)
    })
})
```

### Cache Adapters

Adapter sub-packages implement `Cache[K, V]` for popular cache libraries. Each is a separate Go module so the main `r8e` package stays dependency-free.

| Adapter | Install | Description |
|---|---|---|
| **Otter** | `go get github.com/byte4ever/r8e/otter` | High-performance, contention-free cache with per-entry TTL |
| **Ristretto** | `go get github.com/byte4ever/r8e/ristretto` | Admission-based cache from Dgraph with cost-aware eviction |

Both adapters accept an `r8e.CacheConfig` to configure capacity:

```go
cfg := r8e.CacheConfig{MaxSize: 50_000}

otterCache := otteradapter.New[string, string](cfg)
risCache   := ristrettoadapter.New[string, string](cfg)
```

Cache configuration can also be loaded from JSON (see [Configuration](#configuration)).

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
    → Timeout         (global deadline)
      → Circuit Breaker  (fast-fail if open)
        → Rate Limiter   (throttle throughput)
          → Bulkhead     (limit concurrency)
            → Retry       (retry transient failures)
              → Hedge     (innermost — races redundant calls)
                → fn()    (your function)
```

StaleCache is standalone and wraps the entire policy call from the outside (see [Stale Cache](#stale-cache)).

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
    r8e.WithHooks(&r8e.Hooks{
        OnRetry:        func(attempt int, err error) { log.Printf("retry #%d: %v", attempt, err) },
        OnCircuitOpen:  func() { log.Println("circuit breaker opened") },
        OnCircuitClose: func() { log.Println("circuit breaker closed") },
        OnTimeout:      func() { log.Println("request timed out") },
        OnRateLimited:  func() { log.Println("rate limited") },
        OnFallbackUsed: func(err error) { log.Printf("fallback used: %v", err) },
    }),
)
```

Available hooks on `Hooks` (12): `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnRateLimited`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnTimeout`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`.

StaleCache has its own hooks configured via `StaleCacheOption`: `OnStaleServed[K,V]` and `OnCacheRefreshed[K,V]` (see [Stale Cache](#stale-cache)).

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
      "bulkhead": 10
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

Cache backends can be configured separately via `LoadCacheConfig`:

```json
{
  "caches": {
    "pricing": {
      "ttl": "5m",
      "max_size": 10000
    }
  }
}
```

```go
cfg, err := r8e.LoadCacheConfig("caches.json", "pricing")
if err != nil {
    log.Fatal(err)
}
cache := otteradapter.New[string, string](cfg)
sc := r8e.NewStaleCache(cache, cfg.TTL)
```

## Custom Configuration

The exported `PolicyConfig`, `CircuitBreakerConfig`, and `RetryConfig` structs carry both `json` and `yaml` tags, so you can embed them in your own application config and unmarshal from any format. Call `BuildOptions` to convert a `PolicyConfig` into functional options without going through `LoadConfig`.

```go
package main

import (
    "log"
    "os"

    "github.com/byte4ever/r8e"
    "gopkg.in/yaml.v3"
)

type AppConfig struct {
    Addr    string          `yaml:"addr"`
    Payment r8e.PolicyConfig `yaml:"payment"`
}

func main() {
    data, _ := os.ReadFile("app.yaml")

    var cfg AppConfig
    if err := yaml.Unmarshal(data, &cfg); err != nil {
        log.Fatal(err)
    }

    opts, err := r8e.BuildOptions(&cfg.Payment)
    if err != nil {
        log.Fatal(err)
    }

    policy := r8e.NewPolicy[string]("payment", opts...)
    _ = policy
}
```

## Presets

Ready-made option bundles for common scenarios:

```go
// Standard: 5s timeout, 3 retries (100ms exp backoff), CB (5 failures, 30s recovery)
p := r8e.NewPolicy[string]("api", r8e.StandardHTTPClient()...)

// Aggressive: 2s timeout, 5 retries (50ms exp, 5s cap), CB (3 failures, 15s), bulkhead(20)
p = r8e.NewPolicy[string]("fast-api", r8e.AggressiveHTTPClient()...)
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

## Claude Code Skill

r8e ships with a [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill file that teaches the AI assistant the full r8e API, patterns, and idioms. To enable it, symlink or copy the skill into your project's `.claude/skills/` directory:

```bash
mkdir -p .claude/skills
cp -r ./vendor/github.com/byte4ever/r8e/claude-skill .claude/skills/r8e
```

Or if you cloned r8e directly:

```bash
mkdir -p .claude/skills
ln -s "$(go list -m -f '{{.Dir}}' github.com/byte4ever/r8e)/claude-skill" .claude/skills/r8e
```

Once installed, Claude Code will automatically apply r8e knowledge when you work on resilience-related code.

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
