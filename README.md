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
- **Observability** — 30 lifecycle hooks, per-policy metrics (counters + live gauges), a JSON endpoint, and an OpenTelemetry bridge (`r8eotel`)
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
| **Concurrency Budget** | Caps concurrent retries/hedges as a fraction of live traffic (with a floor), bounding storm parallelism |
| **Circuit Breaker** | Fast-fail when a dependency is down, auto-recover via half-open probe |
| **Rate Limiter** | Token-bucket throughput control (reject or blocking mode) |
| **Bulkhead** | Semaphore-based concurrency limiting (fixed limit) |
| **Adaptive Concurrency** | Self-tuning concurrency limit from observed latency (Netflix Gradient2) |
| **Adaptive Throttle** | Probabilistic client-side load shedding by the live accept/request ratio (Google SRE), before the breaker trips |
| **Hedged Requests** | Fire a second call after a delay to reduce tail latency |
| **Request Coalescing** | Collapse concurrent identical calls into one shared execution (singleflight), killing cache stampede |
| **Read-Through Cache** | Memoize successful results per key in the chain; fresh hits skip the chain, with refresh-ahead, stale-if-error and negative caching |
| **Stale Cache** | Serve last-known-good value per key on failure (standalone wrapper; superseded by Read-Through Cache for chain use) |
| **Fallback** | Static value or function fallback as last resort |
| **Recover** | Catch panics from the user function and return them as `*PanicError`; lets retry, fallback, or circuit breaker handle them instead of crashing |
| **Chaos Injection** | Probabilistically inject faults, latency, fake outcomes, or behaviors at the core of the chain to exercise your own resilience config (Polly-v8 / Simmy style), gated per call for safe canary chaos |

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

**Adaptive timeout (percentile-driven).** By default the timeout is fixed. `AdaptiveTimeout(...)` instead sizes each call's deadline from a sliding window of recent **successful** latencies — `clamp(percentile × multiplier, floor, ceiling)` — so the deadline tracks the backend's real service time rather than a guessed constant. The duration passed to `WithTimeout` becomes the hard **ceiling** (the adaptive value can only tighten below it, never exceed it) and the fallback used until enough samples accumulate, so a cold or low-traffic policy uses the operator's full timeout.

```go
policy := r8e.NewPolicy[string]("adaptive-timeout",
    r8e.WithTimeout(time.Second,            // hard ceiling + warmup fallback
        r8e.AdaptiveTimeout(
            r8e.AdaptiveTimeoutPercentile(0.99), // default 0.99
            r8e.AdaptiveTimeoutMultiplier(2.0),  // default 2.0 (p99 × 2)
            r8e.AdaptiveTimeoutFloor(20*time.Millisecond), // default: none
            r8e.AdaptiveTimeoutMinSamples(20),   // default 20-sample warmup
        ),
    ),
)
```

Only successful calls feed the window, so a timeout never inflates the percentile that set it. It is the latency→timeout analogue of [adaptive concurrency](#adaptive-concurrency)'s latency→limit. Observability: `Metrics().AdaptiveTimeout` (the timeout the policy would currently apply) and the `r8e.policy.adaptive_timeout` OpenTelemetry gauge; firings still count toward the `Timeouts` counter and the `OnTimeout` hook. See [`examples/35-adaptive-timeout`](examples/35-adaptive-timeout).

**Adaptive hedge delay (percentile-driven).** By default the hedge fires after a fixed delay. `AdaptiveHedge(...)` instead fires it at a sliding-window percentile of recent **successful primary** latencies — `clamp(percentile × multiplier, floor, ceiling)` — so only genuine stragglers (by default the slowest ~5%, Google's tail-at-scale rule) are raced, keeping the redundant load small. The duration passed to `WithHedge` becomes the hard **ceiling** (the adaptive value can only pull the hedge earlier below it, never later) and the warmup fallback used until enough samples accumulate.

```go
policy := r8e.NewPolicy[string]("adaptive-hedge",
    r8e.WithHedge(500*time.Millisecond,        // hard ceiling + warmup fallback
        r8e.AdaptiveHedge(
            r8e.AdaptiveHedgePercentile(0.95), // default 0.95
            r8e.AdaptiveHedgeMultiplier(1.0),  // default 1.0 (fire at p95)
            r8e.AdaptiveHedgeFloor(5*time.Millisecond), // default: none
            r8e.AdaptiveHedgeMinSamples(20),   // default 20-sample warmup
        ),
    ),
    r8e.WithConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5)), // cap the extra load
)
```

Only the **primary** attempt's own completion feeds the window — a winning hedge cancels the primary, whose censored latency is dropped — so a hedge can never bias down the very percentile that set its delay. It is the latency→hedge-delay analogue of the adaptive timeout's latency→timeout, and pairs with the [concurrency budget](#concurrency-budget) to bound how much extra load the hedges add. Observability: `Metrics().AdaptiveHedgeDelay` (the delay the policy would currently apply) and the `r8e.policy.adaptive_hedge_delay` OpenTelemetry gauge; firings still count toward the `HedgesTriggered`/`HedgesWon` counters and the `OnHedgeTriggered`/`OnHedgeWon` hooks. See [`examples/36-adaptive-hedge`](examples/36-adaptive-hedge).

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

**Retry-After:** if a failed attempt's error implements `r8e.RetryAfterProvider`
(`RetryAfter() (time.Duration, bool)`), retry honors that delay (with ±10% jitter,
capped by `MaxDelay`) in place of the computed backoff — the precise wait a server
asked for beats anything you'd guess. Attach a fixed hint to any error with
`r8e.RetryAfterError(err, d)`, or implement the interface yourself; the
[`httpx`](httpx) adapter does it automatically from an HTTP `429`/`503`
`Retry-After` header (delay-seconds or HTTP-date). See
[`examples/23-retry-after`](examples/23-retry-after).

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

**Slow-call rate (brownouts).** Beyond consecutive failures, the breaker can trip on the rate of *slow* calls — a downstream that answers but answers slowly. Enable it with `SlowCallRate(duration, rate)`: a call whose latency exceeds `duration` is "slow", and the breaker opens once that fraction over the recent window reaches `rate`. It is independent of and additive to the failure trip (the breaker opens on whichever fires first), and uses a count-based window tuned with `SlowCallWindow` (default 100) and `SlowCallMinCalls` (default 10). A successful-but-slow call counts; in half-open, a slow probe re-opens just like a failed one. The dedicated `OnSlowCallRateExceeded` hook and the `SlowCallRate` gauge surface the cause. See [`examples/26-slow-call-breaker`](examples/26-slow-call-breaker).

```go
r8e.WithCircuitBreaker(
    r8e.FailureThreshold(5),                  // still trips on failures
    r8e.SlowCallRate(2*time.Second, 0.5),     // …and on >=50% slow calls
)
```

**Adaptive recovery backoff (opt-in).** By default the breaker probes the downstream at a fixed interval (`RecoveryTimeout`). With `RecoveryBackoffMultiplier`, each failed half-open probe doubles (or scales by the configured factor) the wait before the next attempt, reducing pressure on a struggling downstream. `RecoveryMaxBackoff` caps the growth. The backoff resets to the base timeout when the breaker successfully closes. See [`examples/30-recovery-backoff`](examples/30-recovery-backoff).

```go
r8e.WithCircuitBreaker(
    r8e.RecoveryTimeout(5*time.Second),
    r8e.RecoveryBackoffMultiplier(2.0),   // 5s → 10s → 20s → …
    r8e.RecoveryMaxBackoff(60*time.Second),
)
```

**Ramp recovery / slow-start (opt-in).** By default a recovered half-open probe closes the breaker straight to 100% traffic. With `RampRecovery(window)` the breaker instead enters the `CircuitRamping` state and admits a *growing* fraction of traffic over `window` — easing a healing downstream back to load rather than slamming it with the full firehose the instant it looks healthy (Envoy/Istio outlier-detection slow-start). The admitted fraction follows `max(initial, timeFactor^(1/aggression))` where `timeFactor = elapsed/window`: `RampAggression` (default 1.0 = linear, > 1 = faster early) curves it and `RampInitialFraction` (default 0.1) floors it. Shed calls during the ramp return `ErrCircuitRamping`, distinct from `ErrCircuitOpen`; a failed or slow call during the ramp reopens the breaker (and grows the recovery backoff). The `OnCircuitRamping` hook and the `RampRecoveryFraction` gauge surface the ramp. See [`examples/39-ramp-recovery`](examples/39-ramp-recovery).

```go
r8e.WithCircuitBreaker(
    r8e.RecoveryTimeout(200*time.Millisecond),
    r8e.RampRecovery(1*time.Second),   // ramp 10% → 100% over 1s after recovery
    r8e.RampInitialFraction(0.1),
)
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

**Adaptive rate (AIMD).** By default the refill rate is fixed. `AIMD(...)` turns
it into a starting and ceiling value tuned by **additive-increase /
multiplicative-decrease** — the congestion-control law behind TCP. After each
call the policy feeds the outcome back: an outcome that signals server overload
multiplies the rate by `AIMDBackoff` (default `0.9`), any other outcome adds
`AIMDIncrease` back, and the rate is held within `[AIMDMinRate, AIMDMaxRate]`. At
most one adjustment is applied per `AIMDInterval` (default `1s`), so a burst of
rejections backs the rate off once rather than collapsing it.

```go
policy := r8e.NewPolicy[Response]("api",
    r8e.WithRateLimit(100, // starting & ceiling rate
        r8e.AIMD(
            r8e.AIMDMinRate(10),                  // never below 10/s (keep probing)
            r8e.AIMDBackoff(0.5),                 // halve the rate on overload
            r8e.AIMDIncrease(5),                  // add 5/s back per clean interval
            r8e.AIMDInterval(time.Second),        // at most one move per second
        ),
    ),
)
```

By default an outcome is overload only when it is `ErrRateLimited` or carries a
server `Retry-After` hint (an HTTP 429/503 surfaced through the
[`httpx`](httpx) `StatusError`, or any `RetryAfterProvider`); a business error
leaves the rate untouched. Override the signal with `AIMDClassifier(func(error)
bool)`. The numeric parameters are configurable via JSON (`AIMDConfig`, requires
`rate_limit`) and hot-reloadable; the classifier is code-only. Observability: the
`OnRateAdapted` hook (fired with the new rate), the `RateAdaptations` counter, and
the `RateLimit` gauge (the live rate). A `RateLimiter` can drive AIMD standalone
via `NewRateLimiter` + `RecordOutcome`. See
[`examples/32-aimd-rate-limit`](examples/32-aimd-rate-limit).

### Bulkhead

Limit concurrent access to a resource. Returns `r8e.ErrBulkheadFull` when at capacity.

```go
policy := r8e.NewPolicy[string]("bulkhead-example",
    r8e.WithBulkhead(5), // max 5 concurrent calls
)
```

**Bounded FIFO wait.** By default a full bulkhead rejects immediately. With `BulkheadMaxWait(d)` a full bulkhead instead queues callers in FIFO order for up to `d` (timed against the injected `Clock`), handing each freed slot to the head of the queue. The queue is bounded by `BulkheadQueueDepth(n)` (default: the concurrency limit); once it is full, callers are rejected immediately with `ErrBulkheadFull`. A caller that waits the full max-wait gives up with `ErrBulkheadTimeout` (distinct from the immediate `ErrBulkheadFull`); a caller whose context is cancelled while queued returns the context error. Observability: the `OnBulkheadQueued` / `OnBulkheadTimeout` hooks, the `BulkheadTimeouts` counter, and the `BulkheadQueued` gauge. See [`examples/27-bulkhead-wait`](examples/27-bulkhead-wait).

```go
r8e.WithBulkhead(10,
    r8e.BulkheadMaxWait(50*time.Millisecond), // queue a full bulkhead…
    r8e.BulkheadQueueDepth(20),               // …up to 20 waiters
)
```

> The standalone `Bulkhead.Acquire(ctx)` takes a context (it may block on the bounded wait), aligning with `RateLimiter.Allow(ctx)`.

### Hedged Request

Fire a second concurrent call after a delay. The first response wins; the other is cancelled. Reduces tail latency.

```go
policy := r8e.NewPolicy[string]("hedge-example",
    r8e.WithHedge(100*time.Millisecond),
)
```

### Stale Cache

`StaleCache[K, V]` is a standalone, keyed stale-on-error wrapper. On success it stores the result in a pluggable `Cache[K, V]` backend. On failure it serves the last-known-good value for that key (if within TTL).

> **Note:** for use *inside* a policy chain, [Read-Through Cache](#read-through-cache) (`WithCache`) now subsumes this — it adds read-through hits and negative caching on top of the same stale-on-error behaviour, as a first-class composable pattern. `StaleCache` remains for standalone, non-policy use.

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
    → Cache           (read-through — fresh hit short-circuits the chain)
      → Coalesce      (collapse duplicate concurrent calls)
        → Timeout         (global deadline — hard cancel)
          → Time Budget    (total cooperative budget for retry + hedge)
            → Adaptive Throttle  (proportional load shed before the breaker trips)
              → Circuit Breaker  (fast-fail if open)
                → Rate Limiter   (throttle throughput)
                  → Bulkhead     (limit concurrency — fixed, or adaptive)
                    → Retry       (retry transient failures, gated by the retry budget)
                      → Hedge     (innermost — races redundant calls)
                        → fn()    (your function)
```

The retry budget is not a separate stage: it lives inside Retry, throttling
retry attempts against the live success/failure ratio (see [Retry Budget](#retry-budget)).

The cache sits just inside Fallback and outside everything else, so a fresh hit
returns without running coalesce, timeout, or any downstream stage, and a fallback
value is never cached (only a genuine downstream success is). Coalescing sits just
inside the cache, so a burst of misses for a hot key shares one trip through
timeout, circuit breaker, rate limiter, bulkhead, retry, and hedge — while each
caller still gets its own fallback (see [Read-Through Cache](#read-through-cache)
and [Request Coalescing](#request-coalescing)).

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

### Hard deadline propagation

By default the budget leaves `context.Context.Deadline()` **unset**, so a
downstream gRPC/HTTP callee can't see it and shed early. Pass `PropagateDeadline`
to additionally expose the budget as a **hard, clock-driven deadline**:

```go
policy := r8e.NewPolicy[Response]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeBudget(350*time.Millisecond, r8e.PropagateDeadline()),
)
```

Each attempt now runs under a context whose `Deadline()` reports the budget
instant (so a downstream client computes its own wire timeout from it) and whose
cancellation **cancels an in-flight attempt** when the budget expires — surfacing
the same `ErrTimeBudgetExceeded` (wrapping `context.DeadlineExceeded`) as the
cooperative stop path. The deadline is driven by the policy's `Clock`, not the
wall clock, so it stays deterministic under a fake test clock; because a real
context deadline is intrinsically wall-clock, the *propagated value* is only
meaningful to real callees on `RealClock` (production). Config-expressible via
`propagate_deadline` (requires `time_budget`, else
`ErrDeadlinePropagationWithoutBudget`) and hot-reloadable via `Reconfigure`. See
[`examples/28-deadline-propagation`](examples/28-deadline-propagation).

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

## Concurrency Budget

A concurrency budget is the *concurrency-dimension* complement of the retry
budget: where that throttles the retry **rate** over time, this caps how many
retries and hedges may be **in flight at once**. Under a burst of simultaneous
failures many callers retry together, multiplying the load on a struggling
dependency — the budget admits only a bounded share of them and sheds the rest.

A retry or hedge is permitted only while

```
concurrent < max(MinConcurrency, MaxRatio × in-flight executions)
```

The `MaxRatio` term scales the ceiling with live traffic (a busy service tolerates
more concurrent retries than an idle one) and the `MinConcurrency` floor keeps a
low-traffic service from being unable to retry at all. This mirrors failsafe-go's
execution budget; the defaults (`MaxRatio` 0.25, `MinConcurrency` 5) match it.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond)),
    r8e.WithConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5)),
)
```

The first attempt of every call is the baseline and is never gated; only retries
(second and later attempts) and the second concurrent hedge attempt claim a
permit. When the budget is exhausted a retry is suppressed and the call fails
with `ErrConcurrencyBudgetExceeded` (wrapping the last downstream error); an
over-budget hedge is simply not launched (the primary still runs). It composes
with the retry budget — use both to bound retries on *both* axes — and a single
budget can be shared across policies for a process-wide ceiling:

```go
budget := r8e.NewConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5))

a := r8e.NewPolicy[string]("a", r8e.WithRetry(3, strategy), r8e.WithSharedConcurrencyBudget(budget))
b := r8e.NewPolicy[string]("b", r8e.WithHedge(20*time.Millisecond), r8e.WithSharedConcurrencyBudget(budget))
```

A budget requires `WithRetry` or `WithHedge` — configuring one with neither
panics in `NewPolicy` (or `BuildOptions` returns
`ErrConcurrencyBudgetWithoutConsumer`). Shedding is observable via the
`OnConcurrencyBudgetExceeded` hook, the `ConcurrencyBudgetExceeded` /
`ConcurrencyBudgetInUse` metrics, and a degraded `concurrency_budget_exhausted`
health condition (it never gates readiness — first attempts still flow). See
[`examples/33-concurrency-budget`](examples/33-concurrency-budget).

## Read-Through Cache

`WithCache` memoizes successful results in the chain. A fresh hit returns the
cached value and short-circuits the whole policy; a miss executes the chain and
caches a successful result for the TTL. The key comes from the call context via a
key function — the same idiom as [Request Coalescing](#request-coalescing), so one
key function can drive both. Returning an empty key opts a call out of caching.

```go
cache := otter.MustNew[string, r8e.CacheEntry[string]](r8e.CacheConfig{MaxSize: 10_000})

policy := r8e.NewPolicy[string]("catalog",
    r8e.WithCache(cache, keyFromCtx, 30*time.Second,
        r8e.StaleIfError(5*time.Minute),     // serve stale on error past the TTL
        r8e.NegativeCache(2*time.Second),    // briefly cache failures too
        r8e.RefreshAhead(25*time.Second),    // reload hot keys before they expire
    ),
    r8e.WithCoalesce(keyFromCtx),            // collapse the miss stampede
    r8e.WithTimeout(time.Second),
)
```

The underlying `Cache` is parameterised by `CacheEntry[T]` (the wrapper r8e stores
to carry each entry's age and any recorded error), so build the adapter with
`r8e.CacheEntry[T]` as the value type. Freshness is measured against the policy's
`Clock`, not the backing cache's own expiry, so it stays deterministic under a
fake clock.

It unifies four behaviours behind one option:

- **Read-through** — within the fresh TTL, a hit skips the downstream entirely.
- **Refresh-ahead** (`RefreshAhead`) — a hit landing in the tail of the fresh
  window (past the refresh threshold but still fresh) is served immediately and
  additionally kicks off a single coalesced background reload, so a hot key keeps
  serving fresh hits instead of falling through to a synchronous miss at expiry
  (Caffeine `refreshAfterWrite`). The reload runs detached (the caller is not
  blocked) and is deduplicated per key; a failed reload is best-effort (the current
  entry is kept and the next in-window read retries), a successful one fires
  `OnCacheRefreshed`. Because the detached reload loses the caller's deadline, a
  policy whose threshold actually fires must also have a `WithTimeout` to bound it
  (`ErrRefreshAheadWithoutTimeout` otherwise); standalone, bound the loader
  yourself. Set the threshold shorter than the fresh TTL; at or beyond it,
  refresh-ahead is inert (and needs no timeout).
- **Stale-if-error** (`StaleIfError`) — past the fresh TTL a value lingers as a
  stale fallback for the given duration. A call in the stale window re-executes to
  refresh, but if that fails the stale value is served instead of the error
  (RFC 5861 stale-if-error), firing `OnStaleServed`. This subsumes the standalone
  [Stale Cache](#stale-cache) for in-chain use.
- **Negative caching** (`NegativeCache`) — a failure with no stale value to fall
  back on is itself cached for a short TTL, so repeated calls for a known-bad key
  fast-fail with the recorded error instead of hammering the downstream.

`ForceRefresh(ctx)` returns a child context that makes one call bypass the cached
read and repopulate on success. Three misconfigurations panic in `NewPolicy`: a
nil key function (`ErrCacheNilKeyFunc`), a nil cache (`ErrCacheNilCache`), and a
non-positive TTL (`ErrCacheNonPositiveTTL`). Because the cache and key function
are code, caching is code-only — it is deliberately absent from `PolicyConfig`,
`BuildOptions`, and `Reconfigure`, exactly like coalescing.

Observability: the `OnCacheHit` / `OnCacheMiss` / `OnCacheStored` / `OnStaleServed`
/ `OnCacheRefreshed` hooks and the `CacheHits` / `CacheMisses` / `CacheStores` /
`CacheStaleServed` / `CacheRefreshes` counters (hits/(hits+misses) is the hit
ratio). Caching is a healthy optimisation, so it has no health condition — only
metrics. A `ReadThroughCache` can also be used standalone via
`r8e.NewReadThroughCache` (configure clock and hooks with `CacheClock` /
`CacheHooks`). See [`examples/24-read-through-cache`](examples/24-read-through-cache)
and [`examples/38-cache-refresh-ahead`](examples/38-cache-refresh-ahead).

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

## Adaptive Throttle

`WithAdaptiveThrottle` adds Google-SRE **client-side adaptive throttling**: a
probabilistic load shedder that rejects calls locally — before they reach a
struggling backend — in proportion to how heavily that backend is already
rejecting them. It keeps a sliding window of requests attempted versus requests
the backend accepted and, once requests exceed `OverloadRatio` (K) times accepts,
sheds new calls with the SRE probability `max(0, (requests − K·accepts) /
(requests + 1))`. A shed call returns `ErrThrottled` without running any inner
stage.

```go
policy := r8e.NewPolicy[Response]("downstream",
    r8e.WithAdaptiveThrottle(
        r8e.OverloadRatio(2),               // K: tolerate a 2x request/accept gap
        r8e.MaxRejectionRate(0.9),          // always let ≥10% through to probe
        r8e.ThrottleWindow(10*time.Second), // sliding window length
        r8e.MinRequests(10),                // need some traffic before shedding
    ),
)
```

Unlike the binary [Circuit Breaker](#circuit-breaker), the throttler dampens load
**gradually and proportionally**, and it sits just outside the breaker in the
chain — ideally easing a recovering backend back to health before the breaker
ever opens. The probability is capped by `MaxRejectionRate` (default `0.9`) so a
fraction of traffic always probes for recovery, and it recovers on its own as the
failures age out of the window. A locally shed call never reaches the breaker, so
it does not count against it.

By default every error from the inner chain counts as a backend rejection; narrow
that with `ThrottleClassifier(func(error) bool)` so only genuine overload errors
do (a 404 or validation error is then treated as an accept). The numeric
parameters are configurable via JSON (`AdaptiveThrottleConfig`) and hot-reloadable;
the classifier is code-only.

Observability: the `OnThrottled` hook, the `Throttled` counter, and the
`ThrottleProbability` gauge. Shedding surfaces as a degraded `throttling` health
condition (it never gates readiness). A `Throttler` can also be used standalone
with `NewThrottler`, `Allow`, and `Record`. See
[`examples/25-adaptive-throttle`](examples/25-adaptive-throttle).

### Request sheddability

Stamp a context to control how the throttler treats a specific call:

```go
// Critical call — always admitted, even at maximum load.
ctx := r8e.WithSheddability(ctx, r8e.SheddabilityNever)
result, err := policy.Do(ctx, fn)

// Background job — shed first as soon as any load shedding is active.
ctx := r8e.WithSheddability(ctx, r8e.SheddabilityAlways)
result, err := policy.Do(ctx, fn)
```

The three levels are: `SheddabilityNever` (bypass — critical traffic),
`SheddabilityDefault` (zero value — normal SRE probability), and
`SheddabilityAlways` (shed first — background or speculative work). Only the
adaptive throttler reads the stamp; other patterns are unaffected. See
[`examples/29-sheddability`](examples/29-sheddability).

## Recover (panic → error)

`WithRecover` wraps the innermost call and converts any panic into a
`*PanicError` value instead of propagating the panic up the call stack. The
recovered error carries both the original panic value and the goroutine stack
trace captured at recovery time.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRecover(),
    r8e.WithRetry(3, r8e.ConstantBackoff(0)),  // retry the panicking call
    r8e.WithFallback("default"),               // or fall back on panic
    r8e.WithHooks(&r8e.Hooks{
        OnPanic: func(value any) { log.Printf("panic recovered: %v", value) },
    }),
)

_, err := policy.Do(ctx, fn)
if errors.Is(err, r8e.ErrPanic) {
    var pe *r8e.PanicError
    errors.As(err, &pe)
    log.Printf("value=%v\nstack=%s", pe.Value, pe.Stack)
}
```

`WithRecover` sits **innermost** in the chain (inside the hedge fork), so every
hedge goroutine gets its own recovery wrapper and retry sees the recovered error.
The `OnPanic` hook fires for each caught panic. The `PanicsRecovered` counter
increments automatically. Standalone use: `r8e.DoRecover[T](ctx, fn, hooks)`.
See [`examples/31-recover`](examples/31-recover).

## Chaos Injection

`WithChaos` deliberately disturbs the call so a policy's **own** resilience
patterns get exercised — does my retry catch the injected fault? does my timeout
catch the injected latency? It is r8e's take on Polly v8 / Simmy chaos
engineering, with four strategies, each injecting independently on a fraction of
calls:

- **`ChaosFault(prob, err)`** — fail the call with `err` (defaults to `ErrChaosInjected`).
- **`ChaosLatency(prob, d)`** — delay the call by `d` on the policy `Clock`, then proceed.
- **`ChaosOutcome(prob, fn)`** — short-circuit with a fabricated typed result or error.
- **`ChaosBehavior(prob, fn)`** — run a side effect before the call, then proceed.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithTimeout(100*time.Millisecond),
    r8e.WithRetry(4, r8e.ConstantBackoff(time.Millisecond)),
    r8e.WithFallback("default"),
    r8e.WithChaos(
        // 30% of canary calls fail — does the retry absorb it?
        r8e.ChaosFault(0.3, errors.New("injected"), r8e.ChaosEnabled(isCanary)),
        // 10% hang past the timeout — does the timeout catch it?
        r8e.ChaosLatency(0.1, 250*time.Millisecond, r8e.ChaosEnabled(isCanary)),
    ),
)
```

Chaos sits **innermost** in the chain — a simulated misbehaving downstream — so
every other pattern wraps and reacts to it: a retry re-rolls every strategy on
each attempt, a timeout bounds injected latency, and a `WithRecover` catches a
panic thrown by a chaos behavior. Strategies run in the order given, and a fault
or outcome short-circuits the rest, so list a fault **before** a latency to skip
the latency wait when the fault fires (Polly's recommended order).

Gate any strategy per call with `ChaosEnabled(func(ctx) bool)` for safe
canary-style chaos in production: read a feature flag or request header from the
context and return whether this call is subject to chaos — switching chaos off at
runtime without a redeploy. Because the outcome/behavior functions and the
`ChaosEnabled` predicate are code, chaos is code-only: it is deliberately absent
from `PolicyConfig`, `BuildOptions`, and `Reconfigure`, like `WithCoalesce` and
`WithCache`. Latency is measured on the injected `Clock`, so chaos is
deterministic in tests. Observability: the `OnChaosInjected` hook (with the
strategy kind) and the `ChaosInjected` counter, exported as the
`r8e.policy.chaos_injected` OpenTelemetry counter. See
[`examples/37-chaos-injection`](examples/37-chaos-injection).

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

Available hooks on `Hooks` (32): `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnCircuitRamping`, `OnSlowCallRateExceeded`, `OnRateLimited`, `OnRateAdapted`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnBulkheadQueued`, `OnBulkheadTimeout`, `OnTimeout`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`, `OnRetryBudgetExceeded`, `OnTimeBudgetExceeded`, `OnCoalesceLeader`, `OnCoalesceFollower`, `OnConcurrencyRejected`, `OnConcurrencyLimitChanged`, `OnThrottled`, `OnCacheHit`, `OnCacheMiss`, `OnCacheStored`, `OnStaleServed`, `OnCacheRefreshed`, `OnPanic`, `OnConcurrencyBudgetExceeded`, `OnChaosInjected`.

StaleCache has its own hooks configured via `StaleCacheOption`: `OnStaleServed[K,V]` and `OnCacheRefreshed[K,V]` (see [Stale Cache](#stale-cache)).

### Metrics

Beyond callbacks, every policy keeps cumulative counters and live gauges, so you don't have to wire hooks by hand. `Policy.Metrics()` returns a snapshot, and `Registry.Snapshot()` returns one per registered policy:

```go
m := policy.Metrics()
fmt.Println(m.Retries, m.CircuitOpens, m.FallbacksUsed) // counters
fmt.Println(m.CircuitState, m.BulkheadInUse, m.Saturated) // live gauges
```

**Latency percentiles.** Every policy also records each `Do()` call's end-to-end duration into a sliding-window histogram and exposes the recent **p50/p95/p99** — no option to enable, the same always-on instrumentation resilience4j gives its timers. Percentiles surface a slow tail an average hides:

```go
m := policy.Metrics()
fmt.Println(m.LatencyP50, m.LatencyP95, m.LatencyP99) // recent window (~10s)
fmt.Println(m.LatencySamples)                          // 0 ⇒ percentiles not yet meaningful
```

The window is a [DDSketch](https://arxiv.org/abs/1908.10693): percentiles stay within ~2% relative error, old latency ages out, and it is measured on the policy's `Clock` so it is deterministic in tests. Every call counts — successes, failures, and fast-fail rejections — so during overload the lower percentiles drop as instant rejections enter the window. See [`examples/34-latency-percentiles`](examples/34-latency-percentiles). The OpenTelemetry bridge below publishes them as `r8e.policy.latency_p50/p95/p99` gauges (seconds).

Two zero-config bridges expose them:

```go
// JSON debug endpoint (stdlib only).
http.Handle("/metrics", r8ehttp.MetricsHandler(r8e.DefaultRegistry()))

// OpenTelemetry metrics — observable counters + gauges per policy, labelled by name.
// Lives in the separate r8eotel module so the core stays dependency-free.
_, err := r8eotel.Register(meter, r8e.DefaultRegistry())

// OpenTelemetry tracing — root span per Do() call + child span per fn invocation.
// Retry chains and hedge races appear as individual timed children in Jaeger/Tempo.
traced := r8eotel.Trace(policy, otel.GetTracerProvider())
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
go run ./examples/23-retry-after/
go run ./examples/24-read-through-cache/
go run ./examples/25-adaptive-throttle/
go run ./examples/26-slow-call-breaker/
go run ./examples/27-bulkhead-wait/
go run ./examples/28-deadline-propagation/
go run ./examples/29-sheddability/
go run ./examples/30-recovery-backoff/
go run ./examples/31-recover/
go run ./examples/32-aimd-rate-limit/
go run ./examples/33-concurrency-budget/
```

## License

MIT
