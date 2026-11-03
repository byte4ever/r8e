# r8e policy — hard constraints (the spine)

These are the structural rules a policy MUST obey. Most are enforced by a
`NewPolicy` panic or a `BuildOptions` / `Reconfigure` error; the rest are
semantic traps that compile but misbehave. Both the authoring matrix and every
reviewer lean on this file. (Canonical signatures: `../r8e/SKILL.md`.)

## Ordering is automatic — never hand-order

Options are auto-sorted by priority, outermost → innermost:

```
Fallback > Cache > Coalesce > Cooldown > Timeout > TimeBudget > SLO
  > AdaptiveThrottle > CircuitBreaker > RateLimiter
  > Bulkhead/AdaptiveConcurrency/SharedBulkhead > Retry > Hedge > Recover > Chaos
```

The order you list options in is irrelevant. RetryBudget / ConcurrencyBudget /
TimeBudget are not visible stages — they gate from within Retry/Hedge. A policy
that "arranges options carefully for ordering" reveals a misunderstanding. The
cooldown (own or shared) sits outside the timeout and the breaker, so its
refusals never reach this policy's breaker; a cache hit and a fallback, outside
it, still answer while it is armed. A shared bulkhead takes the same slot as
`WithBulkhead`.

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
| `rate_limit_burst` (config) | `rate_limit` | `ErrBurstWithoutRateLimit` |

Also: `WithCoalesce` nil keyFn → `ErrCoalesceNilKeyFunc`; `WithCache` nil keyFn →
`ErrCacheNilKeyFunc`, nil cache → `ErrCacheNilCache`, ttl ≤ 0 →
`ErrCacheNonPositiveTTL`; `Burst(n)` with `n` outside `[1, MaxBurst]` → panic
`ErrRateLimitBurstOutOfRange`; `MaxCooldown(d)` with `d ≤ 0` → panic
`ErrCooldownNonPositiveMax` (`NewCooldown`, `NewPolicy`; an error from
`Cooldown.SetMaxCooldown`, `BuildOptions` and `Reconfigure`).

`r8e.ValidateOptions(opts...)` returns the misconfiguration `NewPolicy` would
panic with (conflicts, range errors, nil cache or key function), or nil. It
cannot see a fallback, cache or chaos value whose type does not match `T`.
`r8econf.GetPolicy` runs it and returns the error instead of panicking.

## Mutual exclusion

- `WithBulkhead` and `WithAdaptiveConcurrency` occupy the SAME concurrency slot →
  both together panic `ErrConcurrencyLimiterConflict`. Choose one: a fixed
  bulkhead (known, static downstream capacity) or the Gradient2 adaptive limiter
  (unknown/variable capacity).
- `WithSharedBulkhead(*Bulkhead)` takes that slot too: with `WithBulkhead` or
  `WithAdaptiveConcurrency` it panics `ErrSharedBulkheadConflict`. A
  `Reconfigure` carrying any bulkhead setting (size, max wait, queue depth,
  CoDel) for such a policy returns that error and applies nothing (retune with
  `Bulkhead.Reconfigure`, which changes it for every sharing policy). The shared
  retry / concurrency budgets differ: the last option wins, and any policy's
  `Reconfigure` retunes the budget for all.
- `WithSharedCooldown(*Cooldown)` excludes `WithCooldown`: both panic
  `ErrSharedCooldownConflict`. `r8econf.GetPolicy` returns that error when the
  config sets `cooldown` and the code adds `WithSharedCooldown`; a `Reconfigure`
  that changes the cap of a shared cooldown returns it too (use
  `Cooldown.SetMaxCooldown`, which changes it for every sharing policy).

## Error classification — the default is dangerous

**Unclassified errors are transient (retriable).** Only `r8e.Permanent(err)`
stops retries. So a policy with `WithRetry` and no classification **retries
everything**, including auth failures, validation `4xx`, and other permanent
errors. A retry-bearing policy against a service with permanent error modes MUST
classify them (`RetryIf` or `Permanent()` at the call boundary, or the httpx
classifier). `Retry-After` (429/503) is honored automatically when the error
implements `RetryAfterProvider` (httpx `StatusError` does) — **but `MaxDelay` caps
the hint too**, and the deadline still applies. The wait is the hint capped by
`MaxDelay`, then spread upward by up to 10%: a hint at or below `MaxDelay` is a
floor (waits `[d, 1.1·d]`, never early); a hint above it waits `[MaxDelay,
1.1·MaxDelay]` and is re-sent early, into the server's "back off". When the hints
cannot fit, two ways stop the early re-send:

- `HonorRetryAfter()` (config `retry.honor_retry_after`): when the raw hint
  exceeds `MaxDelay`, the retry stops and `Do` returns the last error as is (no
  `ErrRetriesExhausted`), so the caller still reads the full hint. Hints that fit
  are still retried. The failure still debits a retry budget. No effect without
  `MaxDelay` (every hint is then waited in full, up to the deadline).
- `RetryIf` returning false for a `RetryAfterProvider`: no hinted error is
  retried, whatever its hint; the budget is untouched.

Marking a `429` `Permanent` also stops the retry: its Retry-After is then never
used. `httpx.NewResponseClient` reads no `Retry-After` header and adds no
`StatusError`: its classifier keeps the hint only by returning
`&httpx.StatusError{Response: resp, StatusCode: resp.StatusCode}` (or a
`RetryAfterError`). Since v0.13.0, `Permanent(err).Error()` and
`Transient(err).Error()` are `err`'s message unchanged (no `permanent:` /
`transient:` prefix): read the mark with `IsPermanent` / `IsTransient`, never
from the text.

## Cooldown — the client stops until the server's Retry-After

`WithCooldown(opts...)` refuses calls locally with `ErrCoolingDown` until the
deadline a server's Retry-After hint gave. `WithSharedCooldown(NewCooldown(…))`
does it across every policy sharing one quota (code-only).

- Any failed attempt whose error carries a `RetryAfterProvider` arms it, for the
  hint capped by `MaxCooldown(d)` (default 1 h). `CooldownIf(fn)` narrows which
  errors arm it (e.g. only a `429`); `fn` runs on every goroutine of every
  sharing policy, so it must be safe for concurrent use. A later hint only
  extends the deadline; a success does not disarm it.
- `ErrCoolingDown` is a local rejection (`IsLocalRejection` is true for it). The
  cooldown runs outside its own policy's breaker, which never sees it; the
  breaker of an outer policy can, from a nested policy.
- Inside `WithRetry`, no retry leaves while it is armed, whoever armed it: the
  loop returns the last error as is, never wrapped with `ErrRetriesExhausted`,
  so the breaker counts the server's real error.
- Health: condition `cooling_down`, degraded, never critical (it never pulls a
  pod, even with `WithReadinessImpact`). Hooks `OnCoolingDown` (counter
  `CoolingDown`) and `OnCooldownArmed(d)`.
- Config `cooldown` is the cap: it builds an own cooldown and is reloadable on
  one; a policy without a cooldown → `ErrPatternAbsent`. `WithCooldown` reads
  the policy clock; a `NewCooldown` uses `CooldownClock` (default real clock).

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
- At the binding `WithTimeout` deadline, the rule is decided when the timeout
  fires: a caller that cancelled first keeps the call (its context error); a
  later cancel does not take it back; an error the function returns once the
  binding timeout has passed (it stopped on the propagated deadline with its own
  client or socket error) is `ErrTimeout` with one `OnTimeout`; a failed call
  the caller ended returns the caller's context error, not the function's; an
  error returned before the caller's context reports its end stays the
  function's; a success seen before the timeout stays a success. The same rule
  decides a `PerAttemptTimeout` cut and a hard time budget.
- A failed attempt cut by `PerAttemptTimeout` carries `ErrAttemptTimeout` around
  its own error, fires `OnAttemptTimeout(attempt)` and counts in
  `AttemptTimeouts`. Nothing is added to the chain: `errors.Is(err,
  context.DeadlineExceeded)` holds only when the attempt's own error matches it
  (the context's error, net/http's response-header timeout; a socket deadline
  error does not). Detect the cut with `errors.Is(err, ErrAttemptTimeout)`. An
  attempt that the caller, `WithTimeout` or a time budget ended first does not
  carry it. A cut `Permanent` error is wrapped too and still stops the retry.
- A time budget's gate compares the next backoff alone with what is left, so an
  attempt can start with little of the budget left. Cooperative (no
  `PropagateDeadline`), that attempt runs past the budget, bounded only by
  `WithTimeout`. With `WithTimeout` about equal to the budget, the timeout cuts
  it: `ErrTimeout`, without the previous attempt's error. The budget's deadline
  binds only strictly before the timeout's, so `PropagateDeadline` changes
  nothing while `WithTimeout` ≤ budget. With `PropagateDeadline` and a
  `WithTimeout` longer than the budget, the budget cuts on time with
  `ErrTimeBudgetExceeded`, which wraps the cut attempt's error only when it was
  the last attempt allowed (otherwise `context.DeadlineExceeded`). Earlier
  attempts' errors reach only `OnRetry`. Without `WithHedge`, a success that
  comes back after the budget's cut stays a success; with `WithHedge` the call
  fails with `ErrTimeBudgetExceeded`.
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

A bulkhead in the same policy runs inside the timeout, so it also caps the calls
still running after `Do` returned, **except with `WithHedge`**: the hedge returns
on the cut without waiting for its attempts, and the slot is freed while the
function still runs. A bulkhead around `Do` or in an outer policy frees its slot
when `Do` returns and does not cap them either.

The detached calls (the shared call of `WithCoalesce`, the `RefreshAhead`
reload) ignore the caller's cancellation and deadline; only `WithTimeout` bounds
them. `WithBaseContext(ctx)` (code-only, not reloadable) ties them to a context
the policy's owner controls: cancelling it stops them with its cause, e.g. on
client shutdown. Once it has ended, for every holder of the policy and for
good: every coalesced call that misses the cache fails at once with
`context.Canceled` (a cache hit, `StaleIfError` or `WithFallback` still
answers), reloads stop silently, calls with no coalescing key still run, and
this policy stores no negative entry; a policy or cache wrapping it still caches
the `context.Canceled` it returns. Set it only on a policy whose holders all
stop with its owner.

## Shared bulkhead

`WithSharedBulkhead(NewBulkhead(n, …))` caps the calls in flight of several
policies together (e.g. every route of one API). Each policy reports its own
calls to its own hooks and counters; the gauges (`BulkheadInUse`,
`BulkheadCap`, `BulkheadQueued`, `CoDelLoad`) and the `bulkhead_full` /
`bulkhead_overloaded` conditions describe the same bulkhead in every policy:
aggregate them with max, not sum. It keeps the clock given to `NewBulkhead` (a
policy's `WithClock` does not reach it). **Not re-entrant**: a function run
under one sharing policy must not call, directly or indirectly, any policy on the
same bulkhead, itself included. Below capacity the inner call gets a slot; once
outer calls hold every slot, the inner call is refused or waits for a slot only
an outer call can free, and the callers of every sharing policy are refused or
wait while it lasts.

## httpx response bodies

- A response body is read on the attempt's context. `WithTimeout`, `WithHedge`,
  `PerAttemptTimeout` and `WithTimeBudget` + `PropagateDeadline` end that
  context when they return, so after `Do` the caller reads only the bytes
  already buffered, then `context.Canceled`. The `StandardHTTPClient` /
  `AggressiveHTTPClient` presets include `WithTimeout`. To read a whole body
  after `Do`, bound the call with a deadline on the context passed to `Do`.
- Never build an httpx `Client` with `WithCoalesce` or `WithCache` (every
  sharing caller gets the same `*http.Response` and body stream), nor with a
  static `WithFallback` (it drops the `StatusError` that keeps a permanent
  response's body open, which then leaks its connection until the caller's
  context ends). Use `WithFallbackFunc` and close `StatusError.Response.Body`.
- Under `WithRetry`, `Do` returns a nil response with any error: a permanent
  response's body is reached, and closed, through `errors.As(err, &se)` and
  `se.Response`.
- `httpx.NewResponseClient(name, hc, classify, opts...)` classifies from the
  status, headers and, for a status ≥ 300, the first `MaxClassifiedBody` (64 KiB)
  of the body (put back for the caller). `classify` runs inside the attempt,
  maybe concurrently with itself and after `Do` returned: keep it stateless or
  synchronised, and never modify `body`.

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
  only for one of those five sentinels or `ErrCoolingDown` itself, unwrapped, as
  the guards return it; it is false for `ErrThrottled`, `ErrSLOShed` (their
  shedders run outside the breaker) and `ErrCircuitOpen`. The own cooldown also
  runs outside the breaker, so only an outer policy's breaker can meet
  `ErrCoolingDown`, from a nested policy. A wrapped sentinel does not match:
  when the function calls a nested r8e policy, the outer retry wraps the nested
  rejection once it runs out of attempts (`retries exhausted: bulkhead full`),
  and the breaker counts it, as it should, since the downstream is saturated. A
  classifier that uses `errors.Is(err, ErrBulkheadFull)` matches that wrapped
  error too and hides the saturation. The other way round, a sentinel the
  function returns bare (a nested policy's error passed through as-is, or its
  own refusals expressed with r8e sentinels) matches `IsLocalRejection`, also
  when the outer retry passes it on unwrapped (it stops on a `RetryIf` returning
  false, an exhausted retry budget, a `HonorRetryAfter` stop or an armed
  cooldown). The breaker then never counts the downstream's saturation: the
  function should wrap or replace such errors.
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
  informational. A condition that is only degraded (`cooling_down`,
  `bulkhead_full`, `retry_budget_exhausted`, …) never pulls a pod.

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
- v0.13.0 adds `AttemptTimeouts` (hook `OnAttemptTimeout`, OTel
  `r8e.policy.attempt_timeouts`) and `CoolingDown` (hook `OnCoolingDown`, OTel
  `r8e.policy.cooling_down`); `OnCooldownArmed(d)` has no counter.
- `r8e.ErrorCode(err)` returns the stable snake_case code of the sentinel `err`
  carries (`"circuit_open"`, `"attempt_timeout"`, `"cooling_down"`, …), `""` for
  none. The envelopes `ErrRetriesExhausted`, `ErrTimeBudgetExceeded` and
  `ErrConcurrencyBudgetExceeded` yield to the cause they wrap (a retried panic is
  `"panic_recovered"`). It reaches a sentinel through `Unwrap` only: an error
  type that matches a sentinel through its own `Is` method carries no code. Use
  it for log fields and metric labels, not `err.Error()`.
- `r8eotel.Trace` sets `r8e.rejection_reason` to `ErrorCode(err)` (`"error"`
  when there is none). Since v0.13.0, a panic or chaos fault retried to
  exhaustion reports `panic_recovered` / `chaos_injected`, no longer
  `retries_exhausted`; `attempt_timeout` and `cooling_down` are new values; a
  call shed during ramp recovery reports `circuit_ramping`.

## Code-only vs config-expressible

Absent from `PolicyConfig` / `BuildOptions` / `Reconfigure` (the option carries
a function, a closure or a shared object): `WithCoalesce`, `WithCache`,
`WithChaos`, all classifiers (`RetryIf`, `ThrottleClassifier`, `AIMDClassifier`,
`SLOClassifier`, `CircuitBreakerClassifier`, `CooldownIf`), `WithFallbackFunc`,
`Parent`/`ShieldParent`/shared-budget links, `WithSharedBulkhead`,
`WithSharedCooldown`, `WithBaseContext`, `ChaosEnabled`. (`Reconfigure` of a
breaker keeps its classifier. `HalfOpenProbeLease` is config-expressible as
`half_open_probe_lease` and can be retuned. v0.13.0 adds `cooldown` (the cap),
`rate_limit_burst` (retunable only on a policy built with a burst, else
`ErrPatternAbsent`; removing the field keeps the current burst until a rebuild)
and `retry.honor_retry_after`.) When you emit a JSON `PolicyConfig`, these
patterns will be **silently absent** — say so. And `Reconfigure` can only retune
patterns the policy ALREADY has; adding/removing a pattern needs a rebuild
(configuring an absent pattern → `ErrPatternAbsent`).

## Sentinels worth knowing when writing fallbacks / classifiers

`ErrCircuitOpen`, `ErrCircuitRamping`, `ErrRateLimited`, `ErrBulkheadFull`,
`ErrBulkheadTimeout`, `ErrCoDelShed`, `ErrConcurrencyLimited`, `ErrCoolingDown`,
`ErrThrottled`, `ErrSLOShed`, `ErrTimeout`, `ErrAttemptTimeout`,
`ErrTimeBudgetExceeded`, `ErrRetriesExhausted`, `ErrConcurrencyBudgetExceeded`,
`ErrPanic`, `ErrChaosInjected` — all `errors.Is`-matchable even when wrapped. A
fallback that should only fire on *shedding* (not on a genuine downstream error)
must discriminate on these with `errors.Is`, which matches a wrapped one too;
`IsLocalRejection` (identity, six sentinels only) is for the breaker classifier.
A call that the caller's deadline ended carries the caller's context error, not
`ErrTimeout` or `ErrTimeBudgetExceeded` (see Deadline attribution). A retry
stopped by `HonorRetryAfter` or an armed cooldown returns the server's last
error as is, not `ErrRetriesExhausted`.
