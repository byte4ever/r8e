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
