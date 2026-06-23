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
Fallback > Cache > Coalesce > Timeout > TimeBudget > SLO > AdaptiveThrottle > CircuitBreaker > RateLimiter > Bulkhead/AdaptiveConcurrency > Retry > Hedge > Recover > Chaos.
The retry budget is not a stage; it gates retries from within Retry. The
concurrency budget is likewise not a visible stage; a thin tracker just outside
Retry counts in-flight executions, and Retry/Hedge gate against it. The time
budget stamps a ctx deadline that retry/hedge read. Bulkhead and
AdaptiveConcurrency share the concurrency slot and are mutually exclusive.

## Pattern Options

### Timeout

```go
r8e.WithTimeout(5 * time.Second)
```

Returns `r8e.ErrTimeout` if exceeded.

**Adaptive timeout (percentile-driven):** `r8e.AdaptiveTimeout(opts...)` (a
`TimeoutOption`) sizes each call's deadline from a sliding window of recent
**successful** latencies: `clamp(percentile × multiplier, floor, ceiling)`. The
`WithTimeout` duration is the hard **ceiling** (adaptive only tightens below it,
never exceeds it) and the warmup fallback until `MinSamples` successes accumulate.
Only successes feed the window (a timeout can't inflate its own percentile);
dedicated window, separate from the always-on Do() percentiles. Sub-options:
`AdaptiveTimeoutPercentile` (default 0.99), `AdaptiveTimeoutMultiplier` (default
2.0, must be ≥1), `AdaptiveTimeoutFloor` (default none), `AdaptiveTimeoutMinSamples`
(default 20). Config-expressible (`AdaptiveTimeoutConfig`, requires `timeout` →
`ErrAdaptiveTimeoutWithoutTimeout`) + reconfigurable (the ceiling via `timeout`,
the tunables via `adaptive_timeout`). Observability: `Metrics().AdaptiveTimeout`
gauge + `r8e.policy.adaptive_timeout` OTel gauge; reuses the `Timeouts` counter /
`OnTimeout` hook. Example: `examples/35-adaptive-timeout`.

**Adaptive hedge delay (percentile-driven):** `r8e.AdaptiveHedge(opts...)` (a
`HedgeOption`) fires the hedge at a sliding-window percentile of recent successful
**primary** latencies: `clamp(percentile × multiplier, floor, ceiling)`, so only
genuine stragglers (default p95 ≈ slowest 5%) are raced. The `WithHedge` duration
is the hard **ceiling** (adaptive only pulls the hedge *earlier* below it) and the
warmup fallback until `MinSamples` successes accumulate. Only the **primary**'s own
completion feeds the window — a winning hedge cancels the primary, whose censored
(non-nil-error) latency is dropped — so a hedge never biases down its own delay.
Sub-options: `AdaptiveHedgePercentile` (default 0.95), `AdaptiveHedgeMultiplier`
(default 1.0, must be >0), `AdaptiveHedgeFloor` (default none),
`AdaptiveHedgeMinSamples` (default 20). Shares the `windowedPercentile` estimator
with adaptive timeout. Config-expressible (`AdaptiveHedgeConfig`, requires `hedge`
→ `ErrAdaptiveHedgeWithoutHedge`) + reconfigurable (the ceiling via `hedge`, the
tunables via `adaptive_hedge`). Observability: `Metrics().AdaptiveHedgeDelay` gauge
+ `r8e.policy.adaptive_hedge_delay` OTel gauge; reuses the `HedgesTriggered`/
`HedgesWon` counters and `OnHedgeTriggered`/`OnHedgeWon` hooks. Pairs with
`WithConcurrencyBudget` to cap the extra load. Example: `examples/36-adaptive-hedge`.

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

Add `r8e.PropagateDeadline()` — `r8e.WithTimeBudget(d, r8e.PropagateDeadline())`
— to also expose the budget as a **hard, clock-driven `ctx.Deadline()`** that
downstream gRPC/HTTP callees observe and that **cancels an in-flight attempt** on
expiry (surfacing the same `ErrTimeBudgetExceeded` wrapping
`context.DeadlineExceeded`). The deadline is driven by the policy `Clock` (stays
deterministic under a fake clock); a real ctx deadline is wall-clock, so the
propagated value is only meaningful to real callees on `RealClock`.
Config-expressible via `propagate_deadline` (requires `time_budget`, else
`r8e.ErrDeadlinePropagationWithoutBudget`), hot-reloadable via `Reconfigure`.

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

### Concurrency Budget

```go
r8e.WithConcurrencyBudget(opts ...ConcurrencyBudgetOption)   // per-policy
r8e.WithSharedConcurrencyBudget(*ConcurrencyBudget)          // shared across policies
r8e.NewConcurrencyBudget(opts ...ConcurrencyBudgetOption) *ConcurrencyBudget
```

Concurrency-dimension complement of the retry budget (failsafe-go execution
budget): caps how many retries **and** hedges may be in flight at once. A
retry/hedge is admitted only while `concurrent < max(MinConcurrency, MaxRatio ×
in-flight executions)`. Gates from within Retry and Hedge (no separate priority);
**requires `WithRetry` or `WithHedge`** — neither panics `NewPolicy` (or
`BuildOptions` returns `r8e.ErrConcurrencyBudgetWithoutConsumer`). First attempts
are never gated. When exhausted, a retry is suppressed and the call fails with
**`r8e.ErrConcurrencyBudgetExceeded`** (wrapping the last downstream error); an
over-budget hedge is silently not launched (primary still runs). Composes with the
retry budget. **Options**: `r8e.MaxRatio(r)` (default 0.25, clamped to (0,1]),
`r8e.MinConcurrency(n)` (default 5; 0 disables the floor). Observability:
`OnConcurrencyBudgetExceeded` hook,
`ConcurrencyBudgetExceeded`/`ConcurrencyBudgetInUse` metrics,
`concurrency_budget_exhausted` health condition (degraded). Example:
`examples/33-concurrency-budget`.

### Circuit Breaker

```go
r8e.WithCircuitBreaker(opts ...CircuitBreakerOption)
```

**Options**: `r8e.FailureThreshold(n)` (default 5), `r8e.RecoveryTimeout(d)` (default 30s), `r8e.HalfOpenMaxAttempts(n)` (default 1).

States: closed -> open (fast-fail `r8e.ErrCircuitOpen`) -> half-open -> closed
(or -> ramping -> closed with ramp recovery). State transitions are mutex-guarded
(linearizable); half-open admits at most `HalfOpenMaxAttempts` concurrent probes.

**Slow-call rate** (opt-in, off by default): `r8e.SlowCallRate(duration, rate)`
trips the breaker when the fraction of calls slower than `duration` reaches
`rate` (in (0,1]) over a count-based window — catches brownouts the failure trip
misses. Independent of and additive to the failure trip (opens on whichever
fires first); a slow-but-successful call counts, and in half-open a slow probe
re-opens. Tune with `r8e.SlowCallWindow(n)` (default 100) and
`r8e.SlowCallMinCalls(n)` (default 10). Config-expressible (`SlowCallDuration` +
`SlowCallRateThreshold` must be set together, else `ErrSlowCallConfigIncomplete`;
`SlowCallWindow`, `SlowCallMinCalls`). Observability: `OnSlowCallRateExceeded`
hook, `SlowCallRateExceeded` counter, `SlowCallRate` gauge. Standalone:
`cb.Record(elapsed, err)` (latency-aware; `RecordSuccess`/`RecordFailure` treat
the call as fast).

**Adaptive recovery backoff** (opt-in, default disabled): after each failed
half-open probe, the next recovery wait is `recoveryTimeout × factor^n` where
`n` is the number of consecutive failed probes. First trip always uses the base
`recoveryTimeout` (n=0). Options: `r8e.RecoveryBackoffMultiplier(factor float64)`
(factor ≤ 0 = disabled) and `r8e.RecoveryMaxBackoff(d time.Duration)` (0 = no
cap). Backoff resets to 0 when the breaker successfully closes. Config-
expressible via `RecoveryBackoffMultiplier *float64` and `RecoveryMaxBackoff
*string` fields in `CircuitBreakerConfig` (JSON/YAML). Example:
`examples/30-recovery-backoff`.

**Ramp recovery / slow-start** (opt-in, default disabled): `r8e.RampRecovery(window)`
makes a recovered half-open probe enter the `CircuitRamping` state instead of
closing straight to 100% traffic — admission grows from `RampInitialFraction`
(default 0.1) to full over `window`, easing a healing downstream back to load
(Envoy/Istio slow-start). Fraction = `max(initial, timeFactor^(1/aggression))`,
`timeFactor = elapsed/window`; `r8e.RampAggression(a)` (default 1.0 = linear, >1 =
faster early) curves it. Probabilistic admission (a `sampler` draw vs the
fraction); shed calls return `r8e.ErrCircuitRamping` (distinct from
`ErrCircuitOpen`). A failed/slow call during the ramp reopens the breaker and
bumps the recovery backoff (reaching the ramp does NOT reset `recoveryAttempt`;
only a full close does). The ramp completes lazily in `Allow` once the window
elapses. Config-expressible via `RampRecovery *string` (duration), `RampAggression
*float64`, `RampInitialFraction *float64` (the latter two inert without
RampRecovery, like the slow-call tuners). Observability: `OnCircuitRamping` hook,
`CircuitRamps` counter, `RampRecoveryFraction` gauge. Example:
`examples/39-ramp-recovery`.

### Rate Limiter

```go
r8e.WithRateLimit(rate float64, opts ...RateLimitOption)
```

Token-bucket. `rate` = tokens/sec. Option: `r8e.RateLimitBlocking()` (wait instead of reject).
Returns `r8e.ErrRateLimited` in non-blocking mode.

**Adaptive rate (AIMD):** `r8e.AIMD(opts...)` (a `RateLimitOption`) makes the refill
rate adapt by additive-increase / multiplicative-decrease. The policy feeds each
outcome back: a server-overload outcome multiplies the rate by `AIMDBackoff`
(default 0.9), any other adds `AIMDIncrease` back; held within
`[AIMDMinRate, AIMDMaxRate]`; at most one move per `AIMDInterval` (default 1s). The
configured `rate` is the starting/ceiling value. Sub-options: `AIMDMinRate`,
`AIMDMaxRate`, `AIMDBackoff`, `AIMDIncrease`, `AIMDInterval`, `AIMDClassifier`.
Default overload = `ErrRateLimited` or an error carrying a `Retry-After`
(`RetryAfterProvider`; httpx 429/503); business errors don't slow the rate.
Config-expressible (`AIMDConfig`, requires `rate_limit`) + reconfigurable; the
classifier is code-only. Observability: `OnRateAdapted(rate)` hook,
`RateAdaptations` counter, `RateLimit` gauge. Standalone: `NewRateLimiter` +
`RecordOutcome(err)` + `ReconfigureAIMD`. Example: `examples/32-aimd-rate-limit`.

### Bulkhead

```go
r8e.WithBulkhead(maxConcurrent int, opts ...BulkheadOption)
```

Returns `r8e.ErrBulkheadFull` when all slots occupied (immediate rejection by
default).

**Bounded FIFO wait** (opt-in): `r8e.BulkheadMaxWait(d)` makes a full bulkhead
queue callers in FIFO order for up to `d` (timed against the injected `Clock`),
handing each freed slot to the head of the queue. `r8e.BulkheadQueueDepth(n)`
bounds the queue (default = maxConcurrent); when full, callers are rejected
immediately with `ErrBulkheadFull`. A caller that waits the full max-wait gives
up with `r8e.ErrBulkheadTimeout` (distinct from `ErrBulkheadFull`); a cancelled
ctx returns the context error. Config-expressible (`bulkhead_max_wait` +
`bulkhead_queue_depth`, both requiring `bulkhead`; queue depth requires max-wait,
else `ErrBulkheadQueueWithoutWait` / `ErrBulkheadWaitWithoutBulkhead`),
hot-reloadable. Observability: `OnBulkheadQueued` / `OnBulkheadTimeout` hooks,
`BulkheadTimeouts` counter, `BulkheadQueued` gauge. Standalone admission API is
`Bulkhead.Acquire(ctx) error` (takes a ctx — may block on the wait) + `Release()`
+ `Queued()`.

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

### Adaptive Throttle

```go
r8e.WithAdaptiveThrottle(opts ...ThrottleOption)
```

Google-SRE **client-side adaptive throttling**: a probabilistic load shedder.
Keeps a sliding window of requests vs. backend-accepted requests and, once
requests exceed K·accepts, sheds calls locally with `max(0, (requests −
K·accepts)/(requests+1))`, returning `r8e.ErrThrottled` (a shed call still counts
as a request, never an accept, so sustained rejection raises the probability).
**Options**: `r8e.OverloadRatio(k)` (K, default 2.0), `r8e.MaxRejectionRate(r)`
(cap, default 0.9 — keeps probing for recovery), `r8e.ThrottleWindow(d)` (default
10s), `r8e.MinRequests(n)` (default 10), `r8e.ThrottleClassifier(func(error)
bool)` (which errors are backend rejections; default **all** errors). Sits just
**outside the circuit breaker** (priority `priorityThrottle`) — proportional shed
before the binary trip; a shed call never reaches the breaker. Numeric params are
config-expressible (`AdaptiveThrottleConfig`) + reconfigurable; the classifier is
code-only. Window is `Clock`-driven (deterministic). Observability: `OnThrottled`
hook, `Throttled` counter, `ThrottleProbability` gauge, degraded `throttling`
health condition (never gates readiness). Standalone:
`r8e.NewThrottler(clock, hooks, opts...)` + `Allow(ctx context.Context) error`
(returns `ErrThrottled` or nil) / `Record(err error)`; `RejectionProbability()` /
`Throttling()` snapshots.

**Request sheddability** (`r8e.Sheddability` type, stamped on context):
`r8e.WithSheddability(ctx, s)` / `r8e.SheddabilityFromCtx(ctx)`.
Three levels: `SheddabilityNever` (bypass — critical, always admitted even at max
load), `SheddabilityDefault` (zero value — normal SRE formula),
`SheddabilityAlways` (shed first — as soon as probability > 0). Both the adaptive
throttler and the SLO governor read the stamp; other patterns ignore it. Example:
`examples/29-sheddability`.

### SLO Burn-Rate Governor

```go
r8e.WithSLO(target float64, opts ...SLOOption)
```

**SLO error-budget burn-rate load shedder**: sheds by how fast a *stated*
objective's error budget is burning, not by backend health. `target` is the
success-rate objective (e.g. 0.999 → error budget `1−target`); **burn rate** =
served error rate / error budget (1 = sustainable pace, 14.4 = SRE "fast burn").
Measures the burn rate over a **short and a long sliding window** and sheds only
when **both** exceed `BurnThreshold` (Google-SRE multiwindow rule — fast on a real
burn, ignores a one-window spike). When engaged, sheds with `max(0, 1 −
BurnThreshold/burnRate)` capped at `MaxShedRate`, scaled by the short window;
returns `r8e.ErrSLOShed`. Shedding is **sheddability-aware** (reuses the
`Sheddability` stamp): `Never` always admitted, `Always` shed once active,
`Default` shed with the probability. A shed call is **never recorded**, so shedding
sheddable traffic does not burn budget. **Options**: `r8e.SLOLongWindow(d)`
(default 1m), `r8e.SLOShortWindow(d)` (default 5s, must be < long), `r8e.BurnThreshold(r)`
(default 2.0), `r8e.MaxShedRate(r)` (cap, default 0.9), `r8e.SLOMinRequests(n)`
(default 20, gates the short window), `r8e.SLOClassifier(func(error) bool)` (which
errors burn budget; default **all**). Sits just **outside the adaptive throttler**
(priority `prioritySLO`). An out-of-range target is clamped (not rejected). Numeric
params are config-expressible (`SLOConfig`, with the **required** `target` →
`ErrSLOTargetRequired`) + reconfigurable; the classifier is code-only. Windows are
`Clock`-driven (deterministic). Observability: `OnSLOShed` hook, `SLOShed` counter,
`SLOBurnRate` + `SLOShedProbability` gauges, degraded `slo_burning` health
condition (never gates readiness). Standalone: `r8e.NewSLOGovernor(target, clock,
hooks, opts...)` + `Allow(ctx) error` (returns `ErrSLOShed` or nil) /
`Record(err error)`; `BurnRate()` / `ShedProbability()` / `Shedding()` snapshots;
`Reconfigure(target, opts...)`. Example: `examples/40-slo-governor`.

### Hedge

```go
r8e.WithHedge(delay time.Duration, opts ...HedgeOption) // opts: AdaptiveHedge(...)
```

Fires a second concurrent call after `delay`. Returns first success, cancels the other.

### Recover

```go
r8e.WithRecover()
```

Catches any panic from the user function and converts it to a `*r8e.PanicError`
instead of propagating the panic up the call stack. Sits inside Hedge (only Chaos
injection sits further in) so each hedge goroutine recovers independently and
Retry sees the recovered error.

`PanicError` implements `error` and carries:
- `Value any` — the original panic value
- `Stack []byte` — goroutine stack trace at recovery time

Match with `errors.Is(err, r8e.ErrPanic)`; inspect via `errors.As(err, &pe)`.
Hook: `OnPanic func(value any)`. Counter: `PanicsRecovered`.
Standalone: `r8e.DoRecover[T](ctx, fn, hooks)`.
Example: `examples/31-recover`.

### Chaos Injection (Polly v8 / Simmy)

```go
r8e.WithChaos(strategies ...r8e.ChaosStrategy)
```

Probabilistically disturbs the call so the policy's **own** patterns get exercised
(does my retry catch the injected fault? my timeout the injected latency?). Four
strategies, each injecting independently on a fraction `prob ∈ [0,1]` (clamped):

- `r8e.ChaosFault(prob, err, opts...)` — fail with `err` (nil → `ErrChaosInjected`); short-circuits.
- `r8e.ChaosLatency(prob, d, opts...)` — delay `d` on the policy `Clock`, then proceed (ctx-cancellable).
- `r8e.ChaosOutcome[T](prob, fn, opts...)` — short-circuit with a fabricated `(T, error)` (generic; erased to `any` and asserted back to the policy `T`, panicking on mismatch like `WithFallback`; nil fn is inert).
- `r8e.ChaosBehavior(prob, fn, opts...)` — run a side effect, then proceed (nil fn is inert).

Per-strategy option `r8e.ChaosEnabled(func(ctx) bool)` gates injection per call
(canary kill-switch; nil = always eligible). Sits **innermost** (`priorityChaos`,
inside Recover) — a simulated misbehaving downstream every pattern wraps: Retry
re-rolls each strategy per attempt, Timeout bounds injected latency, Recover
catches a chaos-behavior panic. Strategies run in the **order given**; fault/outcome
short-circuit the rest, so list a fault **before** a latency to skip the wait
(Polly's order). **Code-only** (outcome/behavior/enabled are functions): absent
from `PolicyConfig`/`BuildOptions`/`Reconfigure`, like `WithCoalesce`/`WithCache`
— switch off at runtime via a `ChaosEnabled` predicate. Hook: `OnChaosInjected
func(kind string)` (kind ∈ fault/latency/outcome/behavior). Counter:
`ChaosInjected`. OTel: `r8e.policy.chaos_injected`. Example:
`examples/37-chaos-injection`.

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
    ttl time.Duration, opts ...CacheOption)   // opts: StaleIfError(d), NegativeCache(d), RefreshAhead(d)
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
expiry. Four behaviours: **read-through** (fresh hit), **refresh-ahead**
(`RefreshAhead(d)` — a hit past `d` but still within `ttl` is served immediately AND
kicks off a single coalesced **detached background reload** so a hot key keeps
serving fresh hits instead of falling to a synchronous miss at expiry; Caffeine
`refreshAfterWrite`; failed reload keeps the current entry best-effort, success fires
`OnCacheRefreshed` + counts a store; a FIRING threshold requires a `WithTimeout` to
bound the detached reload → `ErrRefreshAheadWithoutTimeout` (standalone: bound the
loader yourself); inert + no-timeout-needed if `d ≥ ttl`), **stale-if-error** (`StaleIfError(d)` — past `ttl`, a value lingers
`d` as a fallback; a stale call revalidates but serves the stale value + fires
`OnStaleServed` if that fails; RFC 5861), **negative caching** (`NegativeCache(d)` —
a failure with no stale fallback is cached `d` so repeats fast-fail with the
recorded error). `r8e.ForceRefresh(ctx)` bypasses the cached read for one call.
Three `NewPolicy` panics: nil keyFn → `ErrCacheNilKeyFunc`, nil cache →
`ErrCacheNilCache`, ttl ≤ 0 → `ErrCacheNonPositiveTTL`. Code-only (absent from
`PolicyConfig`/`BuildOptions`/`Reconfigure`). No health condition (healthy
optimisation). Standalone via `r8e.NewReadThroughCache[T](cache, ttl, opts...)`
(set clock/hooks with `CacheClock`/`CacheHooks`). Supersedes the standalone
`StaleCache` for in-chain use.

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
`r8e.ErrCircuitOpen`, `r8e.ErrCircuitRamping`, `r8e.ErrRateLimited`, `r8e.ErrBulkheadFull`, `r8e.ErrBulkheadTimeout`, `r8e.ErrConcurrencyLimited`, `r8e.ErrThrottled`, `r8e.ErrSLOShed`, `r8e.ErrTimeout`, `r8e.ErrTimeBudgetExceeded`, `r8e.ErrRetriesExhausted`, `r8e.ErrConcurrencyBudgetExceeded`, `r8e.ErrPanic`.

## Hooks

```go
r8e.WithHooks(&r8e.Hooks{
    OnRetry:            func(attempt int, err error) {},  // attempt is 1-indexed
    OnCircuitOpen:      func() {},
    OnCircuitClose:     func() {},
    OnCircuitHalfOpen:  func() {},
    OnCircuitRamping:   func() {}, // breaker entered slow-start ramp recovery
    OnSlowCallRateExceeded: func() {}, // breaker opened by the slow-call rate
    OnRateLimited:      func() {},
    OnRateAdapted:      func(rate float64) {}, // AIMD moved the rate limiter's refill rate
    OnBulkheadFull:     func() {},
    OnBulkheadAcquired: func() {},
    OnBulkheadReleased: func() {},
    OnBulkheadQueued:   func() {},  // full bulkhead enqueued a caller (bounded wait)
    OnBulkheadTimeout:  func() {},  // queued caller gave up after max-wait
    OnTimeout:          func() {},
    OnHedgeTriggered:   func() {},
    OnHedgeWon:         func() {},
    OnFallbackUsed:     func(err error) {},
    OnRetryBudgetExceeded: func() {},  // retry suppressed by the retry budget
    OnConcurrencyBudgetExceeded: func() {}, // retry/hedge shed by the concurrency budget
    OnTimeBudgetExceeded:  func() {},  // retry stopped early by the time budget
    OnCoalesceLeader:   func() {},     // call ran a shared coalesced execution
    OnCoalesceFollower: func() {},     // call deduplicated into an in-flight one
    OnConcurrencyRejected:     func() {},     // adaptive limiter shed a call
    OnConcurrencyLimitChanged: func(limit int) {}, // adaptive limit retuned
    OnThrottled:   func() {},  // adaptive throttler shed a call locally
    OnSLOShed:     func() {},  // SLO governor shed a call to protect the error budget
    OnCacheHit:    func() {},  // served from cache (fresh value or negative entry)
    OnCacheMiss:   func() {},  // no fresh value; downstream executed
    OnCacheStored: func() {},  // successful result written to cache
    OnStaleServed: func() {},  // stale value served after a downstream failure
    OnCacheRefreshed: func() {}, // refresh-ahead background reload repopulated an entry
    OnPanic:       func(value any) {},  // panic recovered by WithRecover
    OnChaosInjected: func(kind string) {}, // chaos strategy injected (fault/latency/outcome/behavior)
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
`CircuitCloses`, `CircuitHalfOpens`, `CircuitRamps`, `RateLimited`, `BulkheadRejected`,
`BulkheadTimeouts`, `HedgesTriggered`, `HedgesWon`, `FallbacksUsed`,
`RetryBudgetExceeded`, `TimeBudgetExceeded`, `CoalesceLeaders`,
`CoalesceFollowers`, `ConcurrencyRejected`, `Throttled`, `SLOShed`, `RateAdaptations`,
`SlowCallRateExceeded`, `CacheHits`, `CacheMisses`, `CacheStores`,
`CacheStaleServed`, `CacheRefreshes`, `PanicsRecovered`,
`ConcurrencyBudgetExceeded`, `ChaosInjected`) and gauges
(`CircuitState`, `SlowCallRate`, `RampRecoveryFraction`, `BulkheadInUse`, `BulkheadCap`,
`BulkheadQueued`, `RetryBudgetTokens`, `CoalesceInFlight`, `ConcurrencyLimit`,
`ConcurrencyInFlight`, `ThrottleProbability`, `SLOBurnRate`, `SLOShedProbability`,
`RateLimit`, `AdaptiveTimeout`,
`AdaptiveHedgeDelay`, `Saturated`, `Healthy`, `Criticality`).

**Latency percentiles (always on, no option):** every `Do()` duration feeds a
sliding-window DDSketch; `PolicyMetrics` exposes `LatencyP50`, `LatencyP95`,
`LatencyP99` (`time.Duration`, recent ~10s window, ~2% relative error) and
`LatencySamples` (`int64`; 0 ⇒ not yet meaningful). Clock-driven (deterministic
in tests); every call counts, including fast-fail rejections. OTel publishes
`r8e.policy.latency_p50/p95/p99` gauges (seconds). See `examples/34-latency-percentiles`.

Bridges: `r8ehttp.MetricsHandler(reg)` (JSON, stdlib) and
`r8eotel.Register(meter, reg)` (OpenTelemetry observable instruments, separate
module — keeps core dependency-free).

**OTel tracing:** `r8eotel.Trace(policy, tp)` returns a `*TracedPolicy[T]`
decorator (drop-in for `*Policy[T]`): one root span per `Do()` call (named after
the policy) + one child span per fn invocation (initial, retry, hedge). Root span
carries `r8e.policy`, `r8e.attempts`, and `r8e.rejection_reason` on error; child
span carries `r8e.attempt.number` (1-indexed).

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
github.com/byte4ever/r8e/r8eotel    # OpenTelemetry metrics (Register) + tracing (Trace) bridge (separate module)
github.com/byte4ever/r8e/otter      # Otter cache adapter
github.com/byte4ever/r8e/ristretto  # Ristretto cache adapter
```

Examples: `examples/01-quickstart` through `examples/40-slo-governor`.

## Conventions: every feature ships with a documented example (mandatory)

Any new pattern (a new `WithX`) or behaviour/API change MUST land, in the same
change, with a fully documented runnable example — never code-only. This is a
hard gate, not a follow-up. For each new feature provide:

- A numbered example directory `examples/NN-<kebab-name>/` (NN = next free
  number) containing **three** files: `main.go`, `README.md`, `README.fr.md`.
- `main.go` with rich **"why" comments**: a header paragraph stating the problem
  the feature solves (not just the API call), and inline comments explaining the
  intent at each step. Match the comment density of the sibling examples (~30% of
  lines). It must compile, `go vet` clean, and run with stable, readable output.
- Bilingual READMEs on the **house template** (mirror `examples/07-hedge`):
  intro → `## What it demonstrates` → `## How it works` (a mermaid diagram when
  it clarifies the flow) → `## Key concepts` (a table) → `## When to use` →
  `## Run` → `## Expected output`. Both files open with the cross-language link
  (`*[Lire en Français](README.fr.md)*` / `*[Read in English](README.md)*`); the
  FR file uses proper accents.
- Wire the new example into the central example lists in **`README.md` AND
  `README.fr.md`** (the `go run ./examples/...` block).
- Update `README.md`, `README.fr.md` (keep FR in sync), and this `SKILL.md`;
  `doc.go` stays a high-level overview (no per-pattern enumeration).

See [`examples/40-slo-governor`](../examples/40-slo-governor) for the reference
shape (bilingual README on the template + a "why"-commented `main.go`).
