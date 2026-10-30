# Checklist — overload / shedding axis

Per-category checks for the policy's overload controls. Each check is phrased so a
"yes" to the *hazard* is an observation to score. Enumerate; do not pre-dismiss.

## Circuit breaker presence & failure trip

- [ ] Is `WithCircuitBreaker` present at all on a REMOTE / shared dependency? Its
      absence on a remote dep the policy retries is the first observation.
- [ ] Is `FailureThreshold` too HIGH to ever trip at this traffic volume (the
      breaker is decorative)?
- [ ] Is `FailureThreshold` too LOW so it thrashes open/closed on normal error
      noise?
- [ ] Is the threshold a *stated* fit to traffic, or an unverified default?
      (default `FailureThreshold(5)` is plausible but UNKNOWN until checked.)

## Outcome classification (what the breaker counts)

- [ ] Do errors that say nothing about the dependency's health reach the
      breaker: business / `Permanent` `4xx` in the error taxonomy, or caller
      cancellations (the call runs under a request context the caller can
      cancel)? With no `CircuitBreakerClassifier`, each counts as a failure, and
      a run of `FailureThreshold` of them opens the breaker on a healthy
      dependency.
- [ ] Does the policy have a rate limiter, bulkhead or adaptive concurrency? They
      run INSIDE the breaker, so their rejections (`ErrRateLimited`,
      `ErrBulkheadFull`, `ErrBulkheadTimeout`, `ErrCoDelShed`,
      `ErrConcurrencyLimited`) reach it as failures though the call never left
      the process. A burst past the bulkhead's size opens the breaker on a
      healthy dependency unless the classifier makes them neutral
      (`IsLocalRejection(err)`).
- [ ] How does the classifier match them? `IsLocalRejection` compares by
      identity: it matches the sentinels exactly as the guards return them.
      `errors.Is(err, ErrBulkheadFull)` (and the like) also matches a WRAPPED
      sentinel: when the fn calls a nested r8e policy, the outer retry wraps its
      rejection once attempts run out (`retries exhausted: bulkhead full`), and
      `errors.Is` makes the downstream's saturation neutral. When the fn is a
      plain client that never returns r8e sentinels, both match the same errors.
- [ ] Does the fn return an r8e sentinel BARE (a nested policy's error passed
      through, or its own refusals written as r8e sentinels)? `IsLocalRejection`
      then reads the downstream's refusal as a local one, also when the outer
      retry passes it on unwrapped (a `RetryIf` returning false, an exhausted
      retry budget): the breaker never counts that saturation. The fn should wrap
      or replace it.
- [ ] Is the classifier right? Business / `Permanent` errors and caller
      cancellations → `OutcomeNeutral` (or success); genuine downstream faults and
      r8e deadlines → failure. A classifier that makes `5xx` or every error
      neutral blinds the breaker.
- [ ] With caller cancellations neutral (`CallerCancellationIsNeutral` or
      `IsCallerCancellation`), does an r8e deadline (`WithTimeout`,
      `PerAttemptTimeout`, a `PropagateDeadline` budget) end a hanging call
      STRICTLY before the callers give up? An r8e deadline on the same instant as
      the callers' or later arms no timer and the deadline is the caller's. If
      the callers' deadline is shorter, equal, or unknown, a hanging dependency
      ends as neutral caller cancellations: it never opens the breaker, and
      `SlowCallRate` never sees it either.
- [ ] With local rejections neutral and a call that ignores `ctx`: hung calls
      keep their bulkhead slots after `WithTimeout` returned, so the bulkhead's
      rejections are the only sign left that the dependency hangs, and they are
      neutral. The breaker never opens.

## Half-open probes (a probe that never returns)

- [ ] Can a half-open probe hang: does the call ignore `ctx`, or is its `ctx`
      handling UNKNOWN? A probe holds its slot until it reports. `WithTimeout`
      does not free it: it runs outside the breaker, answers the caller with
      `ErrTimeout` and leaves the call running. Once hung probes hold every
      `HalfOpenMaxAttempts` slot, the breaker stays half-open and fails every
      call with `ErrCircuitOpen` until restart.
- [ ] Is `HalfOpenProbeLease(d)` set where a probe can hang? Once every slot is
      taken and even the newest probe was admitted more than `d` ago, the next
      call counts the probes as one failed probe and reopens the breaker.
- [ ] Is `d` ABOVE the worst span from a probe's admission to its report? Inside
      a policy that span covers every retry attempt, their backoff, the waits of
      a rate limiter or bulkhead inside the breaker (`BulkheadMaxWait`,
      `RateLimitBlocking`), the classifier and `OnCircuitHalfOpen`. A shorter
      lease counts probes still running as failed once every slot is taken,
      reopening the breaker on a recovering dependency (and growing the
      recovery timeout with `RecoveryBackoffMultiplier`).
- [ ] Is the lease presented as the fix for a call that ignores `ctx`? It is a
      safety net; the fix is a ctx-honoring call under `WithTimeout` (call
      axis).

## Recovery sizing

- [ ] Is `RecoveryTimeout` shorter than the downstream's real recovery time → the
      breaker closes and re-floods a still-sick dependency?
- [ ] Is `RecoveryTimeout` far longer than recovery → the breaker over-sheds long
      after the downstream is healthy?
- [ ] Does `HalfOpenMaxAttempts` let too many probes hit a fragile downstream at
      the moment of close?
- [ ] On a downstream that re-floods on close, is `RampRecovery` /
      `RecoveryBackoff` (slow-start admission) present, or does full traffic
      return instantly?

## Brownout (slow-but-200)

- [ ] Is the real failure mode a BROWNOUT — slow-but-200, DB contention, GC
      pauses — that a failure-count trip never sees?
- [ ] Is `SlowCallRate(duration, rate)` present so the breaker trips on a slow
      majority, not only on outright errors?
- [ ] Is the slow-call duration set near a meaningful latency (e.g. above p99 /
      the SLA), not arbitrarily?

## Bulkhead presence & sizing

- [ ] Is there ANY concurrency cap on in-flight work to this dependency?
- [ ] Is `WithBulkhead(n)` sized to a STATED downstream capacity (pool size,
      connection cap), or guessed?
- [ ] Under a burst, does the bulkhead WAIT (`BulkheadMaxWait(d)`) or fast-REJECT
      (`ErrBulkheadFull`)? Does that match the call's tolerance?
- [ ] Where the queue can build, is `BulkheadCoDel(target, interval)` present to
      shed standing-queue latency instead of unbounded queuing?
- [ ] With NO bulkhead, can in-flight requests pile up and exhaust the caller's
      goroutines / connection pool when the downstream slows?

## Adaptive concurrency & mutual exclusion

- [ ] For UNKNOWN / variable downstream capacity, is `WithAdaptiveConcurrency`
      (Gradient2) used instead of a guessed fixed bulkhead?
- [ ] Are `WithBulkhead` AND `WithAdaptiveConcurrency` BOTH configured? They
      occupy the same concurrency slot → build panic
      `ErrConcurrencyLimiterConflict`. (BLOCKING — the service cannot start.)

## Rate limit vs quota

- [ ] Is there a hard external quota / RPS limit, and is `WithRateLimit(rate)`
      present and set BELOW it?
- [ ] Blocking vs reject: is `RateLimitBlocking()` chosen when the call can wait,
      or fast-reject (`ErrRateLimited`) chosen when it must fail fast? Does the
      mode match?

## Throttle / SLO shedding & sheddability

- [ ] Where the backend signals overload by health, is `WithAdaptiveThrottle`
      (proportional client-side shed) present before the binary breaker trip?
- [ ] Where a numeric SLO exists, is `WithSLO(target, …)` shedding on burn rate?
- [ ] Are criticality tiers stamped (`WithSheddability`) so `Never` traffic is
      protected and `Always` is shed first?

## Metastable amplification (the headline hazard)

- [ ] Does the policy RETRY and/or HEDGE a SHARED dependency with NO breaker and
      NO shedding? Then a brownout makes every replica amplify load (×retries +
      hedge) with nothing to fast-fail, and the downstream cannot recover —
      classic metastable collapse.
- [ ] Even with a retry budget, is there a fast-fail path (breaker / throttle /
      SLO) so a sustained brownout sheds rather than queues?

## Red lines (any one is BLOCKING on its own)

- A shared / remote dependency the policy RETRIES and/or HEDGES with NO circuit
  breaker AND no shedding → metastable amplification under brownout, no recovery.
- `WithBulkhead` and `WithAdaptiveConcurrency` configured together → build panic
  `ErrConcurrencyLimiterConflict`.
