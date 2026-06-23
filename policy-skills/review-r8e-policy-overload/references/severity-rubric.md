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

## NOTED — observation for awareness, no action required

1. `RampRecovery` could ease re-entry on close, but the downstream tolerates a
   full-traffic return — nice-to-have, not required.
2. A `WithRateLimit` could pre-empt a known quota, but the quota is far above peak
   and the breaker already covers overload — no demonstrated breach.
3. The retry is already budget-gated (`WithRetryBudget`) — amplification is capped;
   the *budget tuning* is the retry axis's, note and defer.
4. Sheddability tiers are absent but all traffic here is single-tier — no `Never`
   traffic to protect.

## Anti-calibration (do NOT do this)

- Scoring "no retry budget" or backoff tuning here — that is the retry axis.
- Scoring the readiness-flip of `WithReadinessImpact` here — that is observability's
  (you may NOTE the metastable interaction, not score the wiring).
- Calling an absent breaker BLOCKING on a dedicated, non-shared, non-retried dep
  with no amplification path — at most IMPORTANT, often NOTED.
- Marking IMPORTANT for "could add a breaker" with no overload/brownout path — that
  is at most NOTED.
