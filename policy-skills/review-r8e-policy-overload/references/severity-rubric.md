# Severity rubric — overload / shedding axis

Calibrated examples. Severity rests on a **demonstrated failure mode**, never on
aesthetic preference or a best-practice cited without a concrete case. When in
doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **Metastable amplification: retry/hedge on a shared dep, no breaker, no
   shedding.** `WithRetry(5, …)` and `WithHedge` wrap a single shared third-party
   gateway with no `WithCircuitBreaker` and no throttle/SLO. Propagation: a gateway
   brownout (p99 climbs) makes every replica re-send ×5 plus a hedge with nothing
   to fast-fail; offered load multiplies, the gateway stays saturated and never
   recovers. Symptom: fleet-wide unrecoverable collapse that outlives the trigger.
   Remediation: add a breaker (with `SlowCallRate` for the brownout) and/or
   client-side shedding so sustained overload fast-fails.
   Scope: this red line requires NO breaker at all on the retried/hedged shared
   dep. A breaker that is PRESENT — even failure-count-only — is a fast-fail path;
   its insufficiency for brownouts is IMPORTANT #1, not this BLOCKING. (Metastable
   collapse must be *demonstrated* by the visible config, not asserted from a
   plausible brownout the present breaker might still catch.)
2. **Bulkhead + adaptive concurrency together.** `WithBulkhead(32)` and
   `WithAdaptiveConcurrency(…)` both configured. Propagation: they occupy the same
   concurrency slot → `NewPolicy` panics `ErrConcurrencyLimiterConflict`. Symptom:
   the service cannot start. Remediation: pick ONE — fixed bulkhead for known
   static capacity, Gradient2 for unknown/variable.
3. **No concurrency cap on a dep that can slow.** No bulkhead, no adaptive
   concurrency, on a remote dep whose p99 can spike. Propagation: when the
   downstream slows, in-flight requests pile up unbounded; the caller's goroutines
   / connection pool are exhausted. Symptom: the caller stalls and takes down
   unrelated traffic. Remediation: cap in-flight to the downstream's serveable
   capacity.
   Scope: requires NO relief path — no breaker AND an amplifying retry/hedge on a
   non-degradable or critical path. On a DEGRADABLE read where a breaker IS present
   and the chain falls back acceptably, an absent bulkhead is IMPORTANT #2, not
   this BLOCKING: the breaker opening plus the fallback bound the pile-up.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **Breaker present but no `SlowCallRate`.** `WithCircuitBreaker()` (defaults)
   on a dep whose real failure mode is a BROWNOUT (slow-but-200). The failure-count
   trip never sees a slow majority, so the breaker never opens during the most
   common outage. Accepted only if confirmed the dep fails hard (errors, not
   slowness) — document it.
2. **Bulkhead absent on a shared dep that can slow.** No `WithBulkhead`; in-flight
   work can pile up. Less severe than #3 BLOCKING when retry is budget-gated and a
   tight timeout bounds each attempt, but still a pool-exhaustion window — size a
   bulkhead to the pool or document why unbounded is safe.
3. **Recovery sizing unverified.** `RecoveryTimeout(30s)` / `FailureThreshold(5)`
   are plausible defaults but not traced to the downstream's recovery time or
   traffic. Acceptable as a starting point; flag that they are unverified.
4. **Burst goes to unbounded wait.** `BulkheadMaxWait` set very long (or queue
   discipline absent) so a burst queues for seconds instead of fast-rejecting.
   Acceptable if the caller tolerates the wait; otherwise prefer reject / CoDel.
5. **Breaker counts errors that say nothing about the dependency's health.** The
   artifact shows such errors reaching a breaker whose classifier (or lack of
   one) counts them: business / `Permanent` errors in the taxonomy (`404
   not_found`, validation `4xx`), caller cancellations, or the rejections of a
   rate limiter / bulkhead / adaptive concurrency inside the breaker
   (`ErrBulkheadFull` and the like). Propagation: each counts as a failure;
   `FailureThreshold` consecutive ones (a client probing missing ids, an upstream
   redeploy cancelling its in-flight calls, a burst past the bulkhead's size)
   open the breaker, which then fences off a healthy dependency for
   `RecoveryTimeout`. Remediation: `CircuitBreakerClassifier` with those errors
   neutral, e.g. `IsPermanent(err)` or `IsLocalRejection(err)` →
   `OutcomeNeutral`, else `CallerCancellationIsNeutral(ctx, err)`. Accepted only
   with the risk documented. Scope: requires the artifact to show such errors
   exist; when nothing does, an absent classifier is NOTED #5.
6. **Neutral caller cancellations with no strictly shorter r8e deadline.** The
   classifier makes caller cancellations neutral, and no r8e deadline in view
   (`WithTimeout`, `PerAttemptTimeout`, a `PropagateDeadline` budget) is
   strictly shorter than the callers'. An r8e deadline on the same instant or
   later arms no timer, so the deadline is the caller's. Propagation: a hanging
   dependency is ended by the callers' deadline, every such call is neutral, the
   breaker never opens, and `SlowCallRate` never sees those calls. Remediation:
   a `WithTimeout` strictly below the time the callers' deadline leaves. When
   the callers' deadline is UNKNOWN, the finding is "confirm it is above the r8e
   deadline".
7. **A nested policy's saturation counted as a local rejection.** The fn calls a
   nested r8e policy (or returns r8e sentinels of its own), and either the
   classifier matches the local-rejection sentinels with `errors.Is`, or the fn
   passes the nested rejection through bare. Propagation: the nested policy's
   bulkhead or rate limiter turns calls away because the downstream behind it
   is saturated; `errors.Is` matches the wrapped `retries exhausted: bulkhead
   full`, and `IsLocalRejection` matches a bare one; either way the outer
   breaker calls it neutral and never opens on that saturation. Remediation:
   `IsLocalRejection` in the classifier, and the fn wraps or replaces the
   nested errors. Scope: requires the artifact to show the nesting or the
   sentinels; with a plain client, `errors.Is` on the sentinels is NOTED #6.
8. **A hung probe can hold the breaker half-open.** The fn ignores `ctx`
   (`func(_ context.Context)`, or a client that takes none) under a breaker with
   no `HalfOpenProbeLease`. Propagation: the dependency hangs while the breaker
   probes it; the probes never report; `WithTimeout` answers the callers but
   leaves the probes in their slots; the breaker stays half-open and fails every
   call with `ErrCircuitOpen` until restart. With local rejections neutral, the
   bulkhead filling up with hung calls never trips it either. Remediation:
   `HalfOpenProbeLease(d)` with `d` above the worst probe span, as a safety net;
   the ctx-honoring fix is the call axis's. Scope: when the context states the
   call honors `ctx` under `WithTimeout`, an absent lease is not a finding;
   when the `ctx` handling is UNKNOWN, it is NOTED #7 (the call axis carries the
   "confirm").
9. **Probe lease shorter than a probe's span.** `HalfOpenProbeLease(1s)` while a
   probe can take three attempts (`WithRetry(3, …, PerAttemptTimeout(500ms))`)
   plus their backoff and a `BulkheadMaxWait(500ms)` inside the breaker, with no
   shorter `WithTimeout` around them. Propagation: on a recovering
   dependency, slow-but-successful probes outlive the lease; once every slot is
   taken, the next call counts them as a failed probe and reopens the breaker,
   and with `RecoveryBackoffMultiplier` the recovery timeout grows on every
   cycle. Remediation: `d` above the worst admission-to-report span.

## NOTED — observation for awareness, no action required

1. `RampRecovery` could ease re-entry on close, but the downstream tolerates a
   full-traffic return — nice-to-have, not required.
2. A `WithRateLimit` could pre-empt a known quota, but the quota is far above peak
   and the breaker already covers overload — no demonstrated breach.
3. The retry is already budget-gated (`WithRetryBudget`) — amplification is capped;
   the *budget tuning* is the retry axis's, note and defer.
4. Sheddability tiers are absent but all traffic here is single-tier — no `Never`
   traffic to protect.
5. The breaker has no `CircuitBreakerClassifier`, but nothing in the context shows
   business errors or caller cancellations reaching it — the default counting
   fits; note the option for when such errors appear.
6. The classifier matches the local-rejection sentinels with `errors.Is`, and the
   fn is a plain client that returns no r8e sentinel: it matches the same
   errors as `IsLocalRejection`; note the identity form for when a nested policy
   appears.
7. No `HalfOpenProbeLease`, and whether the call honors `ctx` is UNKNOWN below
   the artifact's boundary — note "confirm the call honors `ctx`, or add the
   lease"; the call axis scores the `ctx` itself.

## Anti-calibration (do NOT do this)

- Scoring "no retry budget" or backoff tuning here — that is the retry axis.
- Scoring the readiness-flip of `WithReadinessImpact` here — that is observability's
  (you may NOTE the metastable interaction, not score the wiring).
- Calling an absent breaker BLOCKING on a dedicated, non-shared, non-retried dep
  with no amplification path — at most IMPORTANT, often NOTED.
- Marking IMPORTANT for "could add a breaker" with no overload/brownout path — that
  is at most NOTED.
- Calling an absent `HalfOpenProbeLease` BLOCKING, or a finding at all when the
  call honors `ctx` under `WithTimeout`: it is an opt-in safety net, and the
  `ctx` defect behind a hung probe is the call axis's.
- Calling an absent or loose `CircuitBreakerClassifier` BLOCKING: the spurious
  trip needs a run of consecutive errors the artifact cannot demonstrate — it is
  IMPORTANT #5 at most (and a classifier that hides a nested policy's
  saturation, IMPORTANT #7 at most). Exception: a classifier that makes EVERY
  error neutral leaves no fast-fail path; score it as an absent breaker
  (BLOCKING #1 when its scope holds).
