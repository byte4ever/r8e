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
4. **A shared guard combined with the policy's own.** `WithSharedBulkhead(bh)`
   with `WithBulkhead(32)` or `WithAdaptiveConcurrency(…)`, or
   `WithSharedCooldown(cd)` with `WithCooldown()` (in code, or a config
   `cooldown` next to a code `WithSharedCooldown`). Propagation: `NewPolicy`
   panics `ErrSharedBulkheadConflict` / `ErrSharedCooldownConflict`
   (`r8econf.GetPolicy` returns the error). Symptom: the service cannot start,
   or the policy is never built. Remediation: keep the shared guard alone in its
   slot; `r8e.ValidateOptions` reports the conflict before `NewPolicy`.
5. **Re-entrant shared bulkhead.** The function run under a policy on a shared
   bulkhead visibly calls another policy on the same bulkhead (one route of an
   API client calling another route of the same client). Propagation: below
   capacity the inner call gets a slot; under load the outer calls hold every
   slot, the inner calls are refused or wait for a slot only an outer call can
   free. Symptom: every sharing policy refuses or queues its callers while those
   calls last. Remediation: do not share the bulkhead across the nested call
   (separate bulkheads, or call the downstream directly). When the call graph is
   UNKNOWN but nesting is plausible, IMPORTANT #14.

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
10. **No cooldown on a quota with long `Retry-After`.** The context states that
    the dependency answers a quota with `429` and hints of minutes or more, or
    that refused calls count against the quota, and the policy has no
    `WithCooldown` / `WithSharedCooldown`. Propagation: every call after the
    first `429` still goes out and gets its own `429` until the hint passes; a
    breaker that counts the `429` opens only for its `RecoveryTimeout`, then
    probes again, inside the hint when the hint is longer. Symptom: the client
    keeps the quota exhausted by its own refused calls (or gets banned, when the
    context says the server does that). Remediation:
    `WithCooldown(MaxCooldown(cap), CooldownIf(is429))`. Scope: with short hints
    (seconds), nothing counted, and a breaker that counts the `429` or a retry
    that does not re-send hinted errors, an absent cooldown is NOTED #9.
11. **Per-policy guard on a limit several policies share.** The context states
    that several routes / policies share one pool or concurrency quota (or one
    rate quota answering `Retry-After`), and each carries its own
    `WithBulkhead(n)` (or its own `WithCooldown`). Propagation: the per-policy
    caps add up above the shared limit, so a burst across routes exhausts the
    pool; or one route's hint stops only that route while the others keep
    sending into the quota. Remediation: one `WithSharedBulkhead(NewBulkhead(
    limit))` / one `WithSharedCooldown(NewCooldown(…))` attached to every
    policy. Scope: requires the sharing to be stated.
12. **Cooldown armed by the wrong errors.** `WithCooldown()` with no `CooldownIf`
    on a dependency that also sends `Retry-After` on responses that are not the
    quota (a `503` from one overloaded instance behind a load balancer).
    Propagation: one instance's hint stops the whole policy (every sharing
    policy) for that hint, up to `MaxCooldown`, while the other instances are
    healthy. Remediation: `CooldownIf` narrowed to the quota's `429` (safe for
    concurrent use). Scope: requires those other hints to be stated; otherwise
    nothing.
13. **Bulkhead that does not cap calls outliving their timeout.** `WithBulkhead(n)`
    sized to a hard downstream cap (a connection pool, a concurrency quota),
    with `WithHedge` and `WithTimeout`, on a call that ignores `ctx` or whose
    `ctx` handling is UNKNOWN. Propagation: the hedge returns on the cut without
    waiting for its attempts, the bulkhead frees the slot, the call runs on; new
    calls take the slot; in-flight work exceeds `n` and the downstream cap.
    Remediation: drop `WithHedge` from that policy, or a call that stops on its
    `ctx`. Scope: when the call honors `ctx`, NOTED #10.
14. **Shared bulkhead whose call graph is unknown.** A `WithSharedBulkhead`
    policy whose function goes through a client that may call another policy on
    the same bulkhead, not shown. The finding is "confirm no policy on this
    bulkhead is called from inside it" (BLOCKING #5 when shown).
15. **Rate-limit bucket above a windowed quota.** The context states a quota per
    window (450 per hour, sliding), and the bucket lets more through in one
    window: `WithRateLimit(0.125, Burst(450))` → up to 900 calls in one hour.
    Propagation: the quota is breached on the first burst after idle; the
    server refuses (or bans). Remediation: `Burst(1)`, or a burst that keeps
    burst + rate × window under the quota. Scope: requires the window to be
    stated; a per-second rate limit with the default bucket is not a finding.

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
8. `MaxCooldown` left at its 1 h default, or not traced to the stated hints: a
   cap below the hints resumes calls early (each new hint re-arms), a cap far
   above lets one bogus hint stop the client that long. Flag the sizing.
9. No cooldown, and the `Retry-After` hints are short (seconds), refused calls
   are not counted, and a breaker counts the `429` (or the retry does not re-send
   hinted errors): a few calls per replica go into the hint before the breaker
   opens. Note `WithCooldown` as an option; no demonstrated harm.
10. `WithBulkhead` with `WithHedge` and `WithTimeout`, on a call that honors
    `ctx`: the slot is freed on the cut, but the call stops on its context right
    after. Note that the bulkhead does not cap calls past their timeout.
11. A shared bulkhead keeps the clock given to `NewBulkhead`; a test that drives
    the policy's `WithClock` does not drive its max wait or CoDel dwell.

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
- Calling an absent cooldown IMPORTANT when the hints are short and the
  context states nothing about quota counting or long hints: it is NOTED #9.
- Counting `ErrCoolingDown` against this policy's own breaker: the cooldown
  runs outside it, so the breaker never sees its refusals. Only an outer
  policy's breaker can (IMPORTANT #7 when the nested refusal is passed on bare).
- Calling an absent or loose `CircuitBreakerClassifier` BLOCKING: the spurious
  trip needs a run of consecutive errors the artifact cannot demonstrate — it is
  IMPORTANT #5 at most (and a classifier that hides a nested policy's
  saturation, IMPORTANT #7 at most). Exception: a classifier that makes EVERY
  error neutral leaves no fast-fail path; score it as an absent breaker
  (BLOCKING #1 when its scope holds).
