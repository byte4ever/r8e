# r8e policy — hard constraints (the spine)

These are the structural rules a policy MUST obey. Most are enforced by a
`NewPolicy` panic or a `BuildOptions` / `Reconfigure` error; the rest are
semantic traps that compile but misbehave. Both the authoring matrix and every
reviewer lean on this file. (Canonical signatures: `../r8e/SKILL.md`.)

## Ordering is automatic — never hand-order

Options are auto-sorted by priority, outermost → innermost:

```
Fallback > Cache > Coalesce > Timeout > TimeBudget > SLO > AdaptiveThrottle
  > CircuitBreaker > RateLimiter > Bulkhead/AdaptiveConcurrency > Retry > Hedge
  > Recover > Chaos
```

The order you list options in is irrelevant. RetryBudget / ConcurrencyBudget /
TimeBudget are not visible stages — they gate from within Retry/Hedge. A policy
that "arranges options carefully for ordering" reveals a misunderstanding.

## Required-companion options (omitting the companion is a build error)

| Option | Requires | Failure if missing |
|---|---|---|
| `WithCoalesce` | `WithTimeout` | panic `ErrCoalesceWithoutTimeout` (detached ctx has no deadline) |
| `WithCache` + `RefreshAhead(d<ttl)` | `WithTimeout` | panic `ErrRefreshAheadWithoutTimeout` (detached reload unbounded) |
| `WithTimeBudget` | `WithRetry` or `WithHedge` | panic `ErrTimeBudgetWithoutConsumer` |
| `PropagateDeadline()` | `WithTimeBudget` | `ErrDeadlinePropagationWithoutBudget` |
| `RespectInboundDeadline()` | `WithTimeBudget` | `ErrInboundDeadlineWithoutBudget` |
| `WithRetryBudget` / `WithSharedRetryBudget` | `WithRetry` | panic `ErrRetryBudgetWithoutRetry` |
| `WithConcurrencyBudget` | `WithRetry` or `WithHedge` | panic `ErrConcurrencyBudgetWithoutConsumer` |
| `AdaptiveTimeout(…)` | `WithTimeout` | `ErrAdaptiveTimeoutWithoutTimeout` |
| `AdaptiveHedge(…)` | `WithHedge` | `ErrAdaptiveHedgeWithoutHedge` |
| `WithSLO(target)` | a `target` is required | `ErrSLOTargetRequired` (config path) |
| `SlowCallRate(d, r)` (config) | both `SlowCallDuration` + `SlowCallRateThreshold` | `ErrSlowCallConfigIncomplete` |
| `BulkheadMaxWait` / `BulkheadCoDel` | `WithBulkhead` | `ErrBulkheadWaitWithoutBulkhead` |
| `BulkheadQueueDepth` | `BulkheadMaxWait` | `ErrBulkheadQueueWithoutWait` |
| `BulkheadCoDel(target,interval)` (config) | both target + interval | `ErrBulkheadCoDelConfigIncomplete` |

Also: `WithCoalesce` nil keyFn → `ErrCoalesceNilKeyFunc`; `WithCache` nil keyFn →
`ErrCacheNilKeyFunc`, nil cache → `ErrCacheNilCache`, ttl ≤ 0 →
`ErrCacheNonPositiveTTL`.

## Mutual exclusion

- `WithBulkhead` and `WithAdaptiveConcurrency` occupy the SAME concurrency slot →
  both together panic `ErrConcurrencyLimiterConflict`. Choose one: a fixed
  bulkhead (known, static downstream capacity) or the Gradient2 adaptive limiter
  (unknown/variable capacity).

## Error classification — the default is dangerous

**Unclassified errors are transient (retriable).** Only `r8e.Permanent(err)`
stops retries. So a policy with `WithRetry` and no classification **retries
everything**, including auth failures, validation `4xx`, and other permanent
errors. A retry-bearing policy against a service with permanent error modes MUST
classify them (`RetryIf` or `Permanent()` at the call boundary, or the httpx
classifier). `Retry-After` (429/503) is honored automatically when the error
implements `RetryAfterProvider` (httpx `StatusError` does).

## Health & readiness

- `NewPolicy(name, …)` auto-registers under `name` for health reporting. An
  **anonymous** `r8e.Do(…)` is NOT registered — no health visibility.
- Readiness is **opt-in**: by default an open breaker is reported but does NOT
  pull the pod. `WithReadinessImpact()` gates `/readyz`. Gating a **shared**
  dependency risks a fleet-wide readiness flip (every replica's breaker trips at
  once → the whole service goes NotReady together). Prefer leaving shared deps
  informational.

## Code-only vs config-expressible

Absent from `PolicyConfig` / `BuildOptions` / `Reconfigure` (the option carries a
function or closure): `WithCoalesce`, `WithCache`, `WithChaos`, all classifiers
(`RetryIf`, `ThrottleClassifier`, `AIMDClassifier`, `SLOClassifier`),
`WithFallbackFunc`, `Parent`/shared-budget links, `ChaosEnabled`. When you emit a
JSON `PolicyConfig`, these patterns will be **silently absent** — say so. And
`Reconfigure` can only retune patterns the policy ALREADY has; adding/removing a
pattern needs a rebuild (configuring an absent pattern → `ErrPatternAbsent`).

## Sentinels worth knowing when writing fallbacks / classifiers

`ErrCircuitOpen`, `ErrCircuitRamping`, `ErrRateLimited`, `ErrBulkheadFull`,
`ErrBulkheadTimeout`, `ErrCoDelShed`, `ErrConcurrencyLimited`, `ErrThrottled`,
`ErrSLOShed`, `ErrTimeout`, `ErrTimeBudgetExceeded`, `ErrRetriesExhausted`,
`ErrConcurrencyBudgetExceeded`, `ErrPanic` — all `errors.Is`-matchable even when
wrapped. A fallback that should only fire on *shedding* (not on a genuine
downstream error) must discriminate on these.
