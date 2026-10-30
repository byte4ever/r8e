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
implements `RetryAfterProvider` (httpx `StatusError` does) — **but `MaxDelay` caps
the hint too**, and the deadline still applies. A hint above `MaxDelay` is re-sent
early, into the server's "back off"; when the hints cannot fit, do not retry those
errors (`RetryIf` returning false for a `RetryAfterProvider`). Marking a `429`
`Permanent` also stops the retry: its Retry-After is then never used.

## Deadline attribution — whose deadline ended the call

A deadline r8e sets (`WithTimeout`, a retry `PerAttemptTimeout`, a
`WithTimeBudget` + `PropagateDeadline` budget) is r8e's only when it is
**strictly earlier** than any deadline the caller's context already has. Compare
it with the time the callers' deadline leaves when the call starts, not with its
nominal value.

- **Binding** (strictly earlier): the r8e deadline ends a hanging call with
  `ErrTimeout` (`OnTimeout`) or `ErrTimeBudgetExceeded`
  (`OnTimeBudgetExceeded`), and a `PerAttemptTimeout` ends the attempt so retry
  can try again. For the breaker it is a failure, never a caller cancellation.
- **Not binding** (the caller's deadline comes first or on the same instant):
  the r8e deadline arms no timer. The call ends when the caller's context does
  and returns the caller's context error, with no `OnTimeout` and no
  `OnTimeBudgetExceeded`; `IsCallerCancellation` reports true. A budget that
  `RespectInboundDeadline` tightened to the inbound deadline is the caller's too.
- With a time budget, retry still refuses an attempt that cannot fit, with
  `ErrTimeBudgetExceeded`, while the context is live. Once the context has
  ended, retry returns that end instead of raising the sentinel. Hedge fires no
  hedge once the budget is spent: it waits for the primary and returns its
  result, or the context's error, with no sentinel and no hook.
- Consequence: keep `WithTimeout`, `PerAttemptTimeout` and a budget strictly
  shorter than the callers' deadlines. Otherwise a hanging dependency is never
  attributed to r8e: `OnTimeout` and the `Timeouts` counter stay silent, a
  `WithFallbackFunc` keyed on `ErrTimeout` never sees the hang, and a breaker
  that treats caller cancellations as neutral never opens.

## WithTimeout returns, the call keeps running

`Do` returns as soon as the `WithTimeout` deadline fires or the caller's context
ends, without waiting for the function: the function keeps running on its own
goroutine until it returns (the detached call of `WithCoalesce` and
`RefreshAhead` too). It must synchronise whatever it shares with the caller (a
captured result variable, a buffer, a request-scoped struct). A function that
ignores its context runs until it returns on its own, and holds its bulkhead
slot and, in half-open, its probe slot all that time.

## Circuit-breaker outcomes — the default counts every error

**By default the breaker counts every non-nil error as a failure**, including a
`Permanent` business `4xx` and a call its caller cancelled. `FailureThreshold`
counts *consecutive* failures, so a run of errors that say nothing about the
dependency's health opens the breaker on a healthy dependency: a client probing
missing ids (a run of `404`s), or an upstream redeploy cancelling its in-flight
calls. `Permanent` stops retries; it does not stop the breaker from counting.

- `CircuitBreakerClassifier(fn)` maps each call to `OutcomeSuccess`,
  `OutcomeFailure` or `OutcomeNeutral`. A neutral call touches neither the
  failure counter nor the slow-call window; in half-open it frees the probe slot
  without counting. The caller always gets the real error: never rewrite errors
  or return `(resp, nil)` to keep the breaker from counting them.
- **The breaker also counts the rejections of the stages inside it.** Rate
  limiter, bulkhead and adaptive concurrency run inside the breaker (see the
  ordering above), so `ErrRateLimited`, `ErrBulkheadFull`, `ErrBulkheadTimeout`,
  `ErrCoDelShed` and `ErrConcurrencyLimited` reach it as failures, though the
  call never reached the dependency. A burst past a bulkhead's size then opens
  the breaker on a healthy dependency: make them neutral with
  `IsLocalRejection(err)`. The calls that were admitted still carry the real
  health signal.
- **`IsLocalRejection` matches by identity, not with `errors.Is`.** It is true
  only for one of those five sentinels itself, unwrapped, as the guards return
  it; it is false for `ErrThrottled`, `ErrSLOShed` (their shedders run outside
  the breaker) and `ErrCircuitOpen`. A wrapped sentinel does not match: when the
  function calls a nested r8e policy, the outer retry wraps the nested rejection
  once it runs out of attempts (`retries exhausted: bulkhead full`), and the
  breaker counts it, as it should, since the downstream is saturated. A
  classifier that uses `errors.Is(err, ErrBulkheadFull)` matches that wrapped
  error too and hides the saturation. The other way round, a sentinel the
  function returns bare (a nested policy's error passed through as-is, or its
  own refusals expressed with r8e sentinels) matches `IsLocalRejection`, also
  when the outer retry passes it on unwrapped (it stops on a `RetryIf` returning
  false or an exhausted retry budget). The breaker then never counts the
  downstream's saturation: the function should wrap or replace such errors.
- `CallerCancellationIsNeutral` is ready-made: a call ended by its caller's
  context is neutral, anything else follows the default. Compose your own with
  `IsCallerCancellation(ctx, err)`, e.g. `IsPermanent(err)` → neutral first.
- **Deadline attribution** (see the section above). An r8e deadline that binds
  ends the call as a failure, never a caller cancellation; a caller deadline
  that comes first or on the same instant is the caller's
  (`IsCallerCancellation` reports true). So once caller cancellations are
  neutral, a hanging dependency opens the breaker only if an r8e deadline
  **strictly shorter than the callers'** ends the call. Otherwise every hang ends
  as a neutral caller cancellation, and `SlowCallRate` never sees it either.
- The classifier is called concurrently and, with `WithTimeout`, may run after
  `Do` returned `ErrTimeout`: it must be a pure function of `(ctx, err)`.
- Outcomes are bound to their admission: an outcome from a call admitted before
  the breaker last opened or entered half-open is ignored. The policy does this on
  its own; a standalone breaker gets it through `Acquire` + `CircuitPermit.Done`
  (`Allow`/`Record` keep their old, unbound behaviour). An ignored outcome counts
  in `CircuitStaleOutcomes`, once, even when it is neutral; a current neutral one
  counts in `CircuitNeutralOutcomes` (see Hooks & metrics).
- **A half-open probe that never returns.** A probe holds its slot until it
  reports. A call that ignores its context and hangs keeps its slot until the
  process restarts, and once such calls hold every `HalfOpenMaxAttempts` slot,
  the breaker stays half-open and fails every call with `ErrCircuitOpen`.
  `WithTimeout` does not prevent it: it runs outside the breaker, answers the
  caller with `ErrTimeout` and leaves the call running in its slot.
  `HalfOpenProbeLease(d)` (opt-in, off by default; config
  `half_open_probe_lease`, reconfigurable) bounds it: once every slot is taken
  and even the newest probe was admitted more than `d` ago, the next call counts
  the probes as one failed probe and reopens the breaker (the recovery backoff
  grows if `RecoveryBackoffMultiplier` is set). The lease runs from admission to
  report, which inside a policy covers every retry attempt and its backoff, the
  waits of a rate limiter or bulkhead inside the breaker, the classifier and
  `OnCircuitHalfOpen`. Set `d` above the worst case of that span: probes still
  running after `d` count as failed once every slot is taken, even if they would
  have succeeded. The lease is a safety net; a function that honours its context
  under `WithTimeout` stays the first defence. A panic in `OnCircuitHalfOpen`
  frees the probe slot too.

## Health & readiness

- `NewPolicy(name, …)` auto-registers under `name` for health reporting. An
  **anonymous** `r8e.Do(…)` is NOT registered — no health visibility. Neither
  is a policy with an **empty name** (`policy.Registry()` is nil).
- **A named policy stays registered until `Registry.Unregister` removes it.** A
  service that builds named policies per job, tenant or request must remove each
  one when it is done (`defer policy.Registry().Unregister(policy)`, which works
  whatever registry the policy joined, and returns false for an unnamed one),
  use an empty name, or register them in a registry it drops with them
  (`WithRegistry`). Otherwise the registry grows without bound, and `Health` /
  `CheckReadiness` / `Snapshot` keep listing policies that no longer run, with
  their last state: a dead policy whose breaker was open stays open, since no
  call arrives to probe it. `Unregister` matches by identity: another policy
  with the same name stays.
- Readiness is **opt-in**: by default an open breaker is reported but does NOT
  pull the pod. `WithReadinessImpact()` gates `/readyz`. Gating a **shared**
  dependency risks a fleet-wide readiness flip (every replica's breaker trips at
  once → the whole service goes NotReady together). Prefer leaving shared deps
  informational.

## Hooks & metrics

- A hook runs on the goroutine of its event: hooks may be called concurrently,
  and a hook fired inside a chain that runs on its own goroutine (`WithTimeout`,
  `WithCoalesce`, `RefreshAhead`) or in the losing attempt of `WithHedge` may run
  after `Do` returned. Every hook must be safe for concurrent use and must not
  rely on request-scoped state.
- Metrics counters are automatic. The breaker adds `CircuitNeutralOutcomes`
  (hook `OnCircuitNeutral`, OTel `r8e.policy.circuit_neutral_outcomes`) and
  `CircuitStaleOutcomes` (hook `OnCircuitStaleOutcome`, OTel
  `r8e.policy.circuit_stale_outcomes`); the two do not overlap. A neutral
  outcome feeds neither the failure counter nor the slow-call window, so a rise
  in neutral outcomes can hide a downstream that callers keep abandoning. A
  stale outcome comes from a call still running when the breaker opened or
  entered half-open: each trip adds about as many as were in flight, whatever
  the recovery timeout.
- `r8eotel.Trace` sets `r8e.rejection_reason` on error; a call shed during ramp
  recovery (`ErrCircuitRamping`) reports `circuit_ramping`.

## Code-only vs config-expressible

Absent from `PolicyConfig` / `BuildOptions` / `Reconfigure` (the option carries a
function or closure): `WithCoalesce`, `WithCache`, `WithChaos`, all classifiers
(`RetryIf`, `ThrottleClassifier`, `AIMDClassifier`, `SLOClassifier`,
`CircuitBreakerClassifier`), `WithFallbackFunc`, `Parent`/shared-budget links,
`ChaosEnabled`. (`Reconfigure` of a breaker keeps its classifier.
`HalfOpenProbeLease` is config-expressible as `half_open_probe_lease` and can be
retuned.) When you emit a JSON `PolicyConfig`, these patterns will be **silently
absent** — say so. And `Reconfigure` can only retune patterns the policy ALREADY
has; adding/removing a pattern needs a rebuild (configuring an absent pattern →
`ErrPatternAbsent`).

## Sentinels worth knowing when writing fallbacks / classifiers

`ErrCircuitOpen`, `ErrCircuitRamping`, `ErrRateLimited`, `ErrBulkheadFull`,
`ErrBulkheadTimeout`, `ErrCoDelShed`, `ErrConcurrencyLimited`, `ErrThrottled`,
`ErrSLOShed`, `ErrTimeout`, `ErrTimeBudgetExceeded`, `ErrRetriesExhausted`,
`ErrConcurrencyBudgetExceeded`, `ErrPanic` — all `errors.Is`-matchable even when
wrapped. A fallback that should only fire on *shedding* (not on a genuine
downstream error) must discriminate on these with `errors.Is`, which matches a
wrapped one too; `IsLocalRejection` (identity, five sentinels only) is for the
breaker classifier. A call that the caller's deadline ended carries the
caller's context error, not `ErrTimeout` or `ErrTimeBudgetExceeded` (see
Deadline attribution).
