*[Lire en Francais](README.fr.md)*

# r8e

A small Go library for composing resilience patterns — timeout, retry, circuit
breaker, rate limiter, bulkhead, hedged requests, and fallback — into a single
policy. (The name is short for r(esilienc)e, in the spirit of k8s.) A standalone
keyed stale cache with pluggable backends complements the policy chain. The core
package has no external dependencies.

[![Go Reference](https://pkg.go.dev/badge/github.com/byte4ever/r8e.svg)](https://pkg.go.dev/github.com/byte4ever/r8e)
[![Go Report Card](https://goreportcard.com/badge/github.com/byte4ever/r8e)](https://goreportcard.com/report/github.com/byte4ever/r8e)

```go
policy := r8e.NewPolicy[string]("payments",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithFallback("service unavailable"),
)
result, err := policy.Do(ctx, callPaymentGateway)
```

Patterns are auto-sorted into a sensible execution order. A circuit breaker can
report health to a Kubernetes `/readyz` endpoint, hooks and metrics feed an
observability pipeline, and sentinel errors such as `r8e.ErrCircuitOpen` make
the failure mode explicit.

```bash
go get github.com/byte4ever/r8e
```

## Status

r8e is young (pre-1.0): the API may still change and it has had limited
production exposure. If you need a mature, widely-adopted library, take a look
at [failsafe-go](https://github.com/failsafe-go/failsafe-go). r8e's angle is an
integrated, opinionated take — named policies with built-in metrics, optional
health reporting, and configuration hot-reload.

## Highlights

- **One policy, all patterns** — compose any combination; r8e orders them for you
- **Concurrency** — lock-free rate limiter and bulkhead; a mutex-guarded, linearizable circuit breaker
- **Health reporting** — optional Kubernetes `/readyz` integration with hierarchical dependencies (`r8ehttp`)
- **Observability** — 18 lifecycle hooks, per-policy metrics (counters + live gauges), a JSON endpoint, and an OpenTelemetry bridge (`r8eotel`)
- **Runtime tuning** — hot-reload pattern parameters (circuit-breaker thresholds, rate limits, timeouts…) without a redeploy
- **Testable** — a `Clock` interface to control time in tests, avoiding `time.Sleep` flakiness
- **Configurable** — define policies in code, JSON (`r8econf`), or with presets
- **Zero-dependency core** — the `r8e` package uses only the Go standard library

## Features

| Pattern | What it does |
|---|---|
| **Timeout** | Cancel slow calls after a deadline |
| **Time Budget** | One total time budget across the chain; retry/hedge stop early before overrunning it |
| **Retry** | Retry transient failures with pluggable backoff (constant, exponential, linear, jitter) |
| **Retry Budget** | Adaptive token bucket that throttles retries when failures dominate, preventing retry storms |
| **Circuit Breaker** | Fast-fail when a dependency is down, auto-recover via half-open probe |
| **Rate Limiter** | Token-bucket throughput control (reject or blocking mode) |
| **Bulkhead** | Semaphore-based concurrency limiting (fixed limit) |
| **Adaptive Concurrency** | Self-tuning concurrency limit from observed latency (Netflix Gradient2) |
| **Hedged Requests** | Fire a second call after a delay to reduce tail latency |
| **Request Coalescing** | Collapse concurrent identical calls into one shared execution (singleflight), killing cache stampede |
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
    → Coalesce        (collapse duplicate concurrent calls)
      → Timeout         (global deadline — hard cancel)
        → Time Budget    (total cooperative budget for retry + hedge)
          → Circuit Breaker  (fast-fail if open)
            → Rate Limiter   (throttle throughput)
              → Bulkhead     (limit concurrency — fixed, or adaptive)
                → Retry       (retry transient failures, gated by the retry budget)
                  → Hedge     (innermost — races redundant calls)
                    → fn()    (your function)
```

The retry budget is not a separate stage: it lives inside Retry, throttling
retry attempts against the live success/failure ratio (see [Retry Budget](#retry-budget)).

Coalescing sits just inside Fallback and outside everything else, so a burst of
duplicate calls shares one trip through timeout, circuit breaker, rate limiter,
bulkhead, retry, and hedge — while each caller still gets its own fallback (see
[Request Coalescing](#request-coalescing)).

StaleCache is standalone and wraps the entire policy call from the outside (see [Stale Cache](#stale-cache)).

## Time Budget

`WithTimeBudget` sets one **total** time budget for the whole call, shared across
retry and hedge. Before each retry, if the backoff alone would overrun the
remaining budget, the retry **stops early** with `ErrTimeBudgetExceeded` (wrapping
the real downstream error) instead of sleeping and launching an attempt that
cannot finish in time; a hedge is not fired once the budget is spent.

```go
policy := r8e.NewPolicy[Response]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeBudget(350*time.Millisecond), // cap total time across all attempts
)
```

This is **tighter than a per-attempt timeout**: `PerAttemptTimeout` bounds each
attempt independently (5 attempts × 1s = up to 5s), whereas the time budget caps
the *sum*. The budget is **cooperative** and measured against the policy's
`Clock`: it gates whether more work starts but does not cancel an in-flight
attempt — pair it with `WithTimeout` (a hard deadline) to bound a single stuck
call. The budget gates only retry and hedge, so it **requires** `WithRetry` or
`WithHedge` — configured with neither, `NewPolicy` panics with
`ErrTimeBudgetWithoutConsumer`. Observability: the `OnTimeBudgetExceeded` hook and
the `TimeBudgetExceeded` metric. See [`examples/22-time-budget`](examples/22-time-budget).

## Retry Budget

A retry budget caps how many retries fire relative to the failure rate, so a
struggling dependency is not buried under a *retry storm*. It follows gRPC's
`retryThrottling` model: an adaptive token bucket (capacity `MaxTokens`) where
every success returns `TokenRatio` tokens and every retryable failure removes
one. While the bucket sits at or below half capacity, retries are suppressed —
the call returns the real downstream error, and the original (first) attempt of
every request always proceeds.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond)),
    r8e.WithRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1)), // gRPC defaults
)
```

To coordinate retries across several policies in one process, build one budget
and share it:

```go
budget := r8e.NewRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1))

a := r8e.NewPolicy[string]("a", r8e.WithRetry(3, strategy), r8e.WithSharedRetryBudget(budget))
b := r8e.NewPolicy[string]("b", r8e.WithRetry(3, strategy), r8e.WithSharedRetryBudget(budget))
```

A budget requires `WithRetry` — configuring one without a retry pattern panics
in `NewPolicy` (or, for config-driven construction, `BuildOptions` returns
`ErrRetryBudgetWithoutRetry`). Throttling is observable via the
`OnRetryBudgetExceeded` hook, the `RetryBudgetExceeded` / `RetryBudgetTokens`
metrics, and a degraded `retry_budget_exhausted` health condition (it never
gates readiness — first attempts still flow). A *shared* budget reports the same
token level and exhausted condition under every sharing policy's name, so
aggregate its gauge with `max`/`avg`, not `sum`. See
[`examples/19-retry-budget`](examples/19-retry-budget).

## Request Coalescing

Request coalescing (a.k.a. *singleflight*) collapses concurrent calls that share
a key into a single shared execution: the first caller (the *leader*) runs the
work, and every caller that arrives while it is in flight (a *follower*) waits
for and shares that one result. When a hot cache key expires, N simultaneous
misses become one downstream call instead of N — the classic cache-stampede fix.

```go
policy := r8e.NewPolicy[string]("user-fetch",
    r8e.WithTimeout(time.Second),       // required — bounds the shared call
    r8e.WithCoalesce(func(ctx context.Context) string {
        return "user:" + userIDFrom(ctx) // derive the key from the call context
    }),
)
```

The key function reads the call's context, so stamp request identity into `ctx`
upstream and read it back here. Returning an empty string opts a call out of
coalescing entirely (it runs on its own). Two misconfigurations panic in
`NewPolicy`: a nil key function (`ErrCoalesceNilKeyFunc`) and a policy with no
`WithTimeout` to bound the detached shared call (`ErrCoalesceWithoutTimeout`).

Coalescing deduplicates only calls that overlap in time; once the leader
finishes its key is released, so a later call starts fresh. It is **not** a cache
— put one in front or behind it for that.

**Detached shared context.** The shared call runs under a context detached from
any single caller (`context.WithoutCancel`), so one caller cancelling cannot
abort work the whole group depends on, and the work runs to completion even if
every caller leaves (handy for still populating a cache). Each caller — leader
included — independently stops waiting the moment *its own* context is done and
returns `ctx.Err()`, so a slow leader never pins a follower past its deadline.
Detaching also strips the caller's deadline — which is why coalescing **requires**
a `WithTimeout` to bound the shared work; without it a leader whose `fn` never
returns would park a goroutine and wedge its key.

Observability: the `OnCoalesceLeader` / `OnCoalesceFollower` hooks, the
`CoalesceLeaders` / `CoalesceFollowers` counters (their ratio is the dedup rate),
and the `CoalesceInFlight` gauge. Coalescing is a healthy optimisation, so it
surfaces no health condition. See
[`examples/20-coalesce`](examples/20-coalesce).

A `Coalescer` can also be used standalone, without a `Policy` (there is no policy
timeout here, so give `fetch` its own deadline):

```go
c := r8e.NewCoalescer[string](&r8e.Hooks{})
val, err := c.Do(ctx, "user:42", fetch)
```

## Adaptive Concurrency

`WithAdaptiveConcurrency` replaces the fixed ceiling of a [Bulkhead](#bulkhead)
with a limit the policy **tunes itself from observed latency**, using Netflix's
Gradient2 algorithm. Each completed call samples its round-trip time (RTT); when
the current RTT rises above a smoothed long-term baseline — the signature of
queueing downstream — the limit is lowered, and when latency is steady the limit
drifts back up. Calls arriving while in-flight is at the current limit are
rejected with `ErrConcurrencyLimited`.

```go
policy := r8e.NewPolicy[Response]("downstream",
    r8e.WithAdaptiveConcurrency(
        r8e.InitialLimit(20),   // start here before any latency is observed
        r8e.MinLimit(1),        // never admit fewer than this
        r8e.MaxLimit(200),      // never admit more than this
        r8e.RTTTolerance(1.5),  // tolerate a 1.5x latency rise before backing off
    ),
)
```

It occupies the same chain slot as the bulkhead, so it is **mutually exclusive**
with `WithBulkhead`: configuring both panics `NewPolicy` with
`ErrConcurrencyLimiterConflict` (or, for config-driven construction,
`BuildOptions` returns it). The limit only grows while the limiter is actually
loaded (in-flight at or above half the limit), so a quiet service is never pushed
to probe higher.

Observability: the `OnConcurrencyRejected` and `OnConcurrencyLimitChanged(limit)`
hooks, the `ConcurrencyRejected` counter, and the `ConcurrencyLimit` /
`ConcurrencyInFlight` gauges. Saturation surfaces as a degraded
`concurrency_limited` health condition (it never gates readiness). See
[`examples/21-adaptive-concurrency`](examples/21-adaptive-concurrency).

An `AdaptiveLimiter` can also be used standalone with `NewAdaptiveLimiter`,
`Acquire`, and `Record`.

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

Available hooks on `Hooks` (18): `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnRateLimited`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnTimeout`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`, `OnRetryBudgetExceeded`, `OnTimeBudgetExceeded`, `OnCoalesceLeader`, `OnCoalesceFollower`, `OnConcurrencyRejected`, `OnConcurrencyLimitChanged`.

StaleCache has its own hooks configured via `StaleCacheOption`: `OnStaleServed[K,V]` and `OnCacheRefreshed[K,V]` (see [Stale Cache](#stale-cache)).

### Metrics

Beyond callbacks, every policy keeps cumulative counters and live gauges, so you don't have to wire hooks by hand. `Policy.Metrics()` returns a snapshot, and `Registry.Snapshot()` returns one per registered policy:

```go
m := policy.Metrics()
fmt.Println(m.Retries, m.CircuitOpens, m.FallbacksUsed) // counters
fmt.Println(m.CircuitState, m.BulkheadInUse, m.Saturated) // live gauges
```

Two zero-config bridges expose them:

```go
// JSON debug endpoint (stdlib only).
http.Handle("/metrics", r8ehttp.MetricsHandler(r8e.DefaultRegistry()))

// OpenTelemetry — observable counters + gauges per policy, labelled by name.
// Lives in the separate r8eotel module so the core stays dependency-free.
_, err := r8eotel.Register(meter, r8e.DefaultRegistry())
```

## Hot Reload

Tune the parameters of patterns a policy already has — at runtime, without a redeploy. `Policy.Reconfigure` applies every non-nil field of a `PolicyConfig` to the live pattern; nil fields are left unchanged:

```go
err := policy.Reconfigure(r8e.PolicyConfig{
    CircuitBreaker: &r8e.CircuitBreakerConfig{FailureThreshold: ptr(3)},
    RateLimit:      ptr(50.0),
})
```

Drive it from a config file via `r8econf`, which re-reads, re-validates, and retunes every already-built policy:

```go
store, _ := r8econf.Load("config.json")
// ... GetPolicy(...) builds policies that auto-register ...
err := store.Reload("config.json") // e.g. on SIGHUP or a ConfigMap change
```

Hot-reload **retunes** existing patterns; it cannot **add or remove** them (the middleware chain is fixed). Configuring an absent pattern returns `ErrPatternAbsent` — rebuild via `GetPolicy`/`NewPolicy` for structural changes. `Registry.Reconfigure(name, cfg)` targets a single registered policy.

## Health & Readiness

Policies report health status, and the registry can expose it over HTTP.

> **Readiness is opt-in.** By **default**, a policy's health does **not** affect the readiness probe — an open circuit breaker reports as unhealthy but does **not** remove the pod from rotation. This is deliberate: a shared downstream dependency degrading would otherwise trip the breaker on *every* replica at once and make Kubernetes pull the whole fleet, turning a dependency blip into a full outage. Gate readiness only on a dependency the pod genuinely cannot serve without, with `WithReadinessImpact()`. Use the probe's `failureThreshold`/`periodSeconds` for hysteresis.

```go
import "net/http"

apiPolicy := r8e.NewPolicy[string]("api-gateway",
    r8e.WithCircuitBreaker(),
)
dbPolicy := r8e.NewPolicy[string]("database",
    r8e.WithCircuitBreaker(),
    r8e.WithReadinessImpact(), // this one DOES gate /readyz when its breaker opens
)

// /readyz gates traffic (503 when a readiness-impacting policy is critical).
http.Handle("/readyz", r8ehttp.ReadinessHandler(r8e.DefaultRegistry()))
// /healthz is informational — full per-policy health, always 200, never gates.
http.Handle("/healthz", r8ehttp.HealthHandler(r8e.DefaultRegistry()))
```

Check health programmatically:

```go
status := apiPolicy.HealthStatus()
fmt.Println(status.Healthy)     // true/false
fmt.Println(status.Conditions)  // all active conditions, e.g. ["rate_limited","bulkhead_full"]
fmt.Println(status.State)       // deterministic most-severe summary: "circuit_open", "healthy", …
fmt.Println(status.Criticality) // CriticalityNone, CriticalityDegraded, CriticalityCritical

report := r8e.DefaultRegistry().Health() // aggregate: "healthy" | "degraded" | "unhealthy"
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

File-based configuration lives in the `r8econf` edge package, so the core
package stays dependency-free:

```go
store, err := r8econf.Load("config.json")
if err != nil {
    log.Fatal(err)
}

// Get a typed policy — config options are merged with code-level options
policy, err := r8econf.GetPolicy[string](store, "payment-api",
    r8e.WithFallback("service unavailable"),
)
if err != nil {
    log.Fatal(err)
}
```

Supported backoff strategies in config: `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

Cache backends can be configured separately via `r8econf.LoadCacheConfig`:

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
cfg, err := r8econf.LoadCacheConfig("caches.json", "pricing")
if err != nil {
    log.Fatal(err)
}
cache := otteradapter.New[string, string](cfg)
sc := r8e.NewStaleCache(cache, cfg.TTL)
```

## Custom Configuration

The exported `PolicyConfig`, `CircuitBreakerConfig`, and `RetryConfig` structs carry both `json` and `yaml` tags, so you can embed them in your own application config and unmarshal from any format. Call `r8e.BuildOptions` to convert a `PolicyConfig` into functional options without going through `r8econf.Load`.

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

r8e includes a [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill file documenting the r8e API, patterns, and idioms for the assistant. To enable it, symlink or copy the skill into your project's `.claude/skills/` directory:

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
go run ./examples/17-httpx-basic/
go run ./examples/18-httpx-retry/
go run ./examples/19-retry-budget/
go run ./examples/20-coalesce/
go run ./examples/21-adaptive-concurrency/
go run ./examples/22-time-budget/
```

## License

MIT
