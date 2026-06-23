# Severity rubric — retry / amplification axis

Calibrated examples. Severity rests on a **demonstrated failure mode**, never on
aesthetic preference or a best-practice cited without a concrete case. When in
doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **Retry storm on a shared dependency, no budget, no classification.**
   `WithRetry(5, …)` hits a shared third-party gateway with no `WithRetryBudget`
   and no `Permanent()`/`RetryIf`. Propagation: gateway brownout → every replica
   retries 5× → aggregate load multiplies on an already-failing dependency →
   metastable failure (it never recovers). Remediation: add `WithRetryBudget`
   (or `WithSharedRetryBudget`) AND classify permanent errors.
2. **ConstantBackoff synchronized thundering herd, no budget.**
   `WithRetry(5, ConstantBackoff(200ms))` on a shared dep with no budget.
   Propagation: all retriers that failed at t wait exactly 200ms and re-fire
   together → synchronized waves hammer the dependency in lockstep. Remediation:
   `ExponentialJitterBackoff(base)` + `MaxDelay(cap)` + a retry budget.
3. **Permanent errors retried `maxAttempts`×.** The taxonomy has permanent 4xx
   (e.g. `402 card_declined`, `400 invalid_request`) but nothing classifies them.
   Propagation: the r8e default treats unclassified errors as transient → each
   permanent failure burns all 5 attempts. Symptom: 5× load per hopeless request +
   5× the latency surfaced to the user, never succeeding. Remediation: classify
   the permanent classes `Permanent` (or `RetryIf`), or use the httpx classifier.
4. **Retry-After ignored under throttling.** A fixed backoff retries into `429
   Retry-After` without honoring it (error not a `RetryAfterProvider`).
   Propagation: the server says "back off N seconds," the client retries sooner →
   throttle stampede deepens the overload. Remediation: use httpx `StatusError` so
   Retry-After is honored automatically.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **No retry budget on a shared but idempotent-read dependency.** No storm-grade
   incident (the call is a benign read), but a partial outage still amplifies
   aggregate load by `maxAttempts`× across the fleet. Accept only with a documented
   reason the amplification is tolerable (e.g. the dep is over-provisioned).
2. **Exponential without jitter.** `ExponentialBackoff` on a shared dep —
   the cohort that failed together still retries together. Mild synchronization;
   accept only if the cohort is small or the dep is internal and over-provisioned;
   prefer `ExponentialJitterBackoff`.
3. **Permanent error retried (low-volume read).** A permanent `404` is unclassified
   → retried `maxAttempts`×. Wastes attempts and adds `maxAttempts`× latency on a
   miss; not load-bearing if 404s are rare, but should be classified.
4. **No `MaxDelay` cap.** Unbounded exponential growth could push a late attempt's
   wait toward the budget edge. Acceptable if `maxAttempts` is small enough that
   the cap never binds — show the arithmetic.

## NOTED — observation for awareness, no action required

1. The dependency's whole-call timeout / TimeBudget *sizing* that retries must fit
   — flag and defer to review-r8e-policy-timeouts (you confirm FIT, not sizing).
2. Retrying this call also amplifies a write side effect — note and defer the
   duplicate-effect SAFETY verdict to review-r8e-policy-call.
3. `maxAttempts` is a round number but the worst-case sum demonstrably fits the
   budget and the dep is over-provisioned — no demonstrated amplification path.

## Anti-calibration (do NOT do this)

- Scoring the duplicate-side-effect SAFETY of retrying a write here — that is the
  call axis. You score the AMPLIFICATION and the CLASSIFICATION.
- Calling a budget-gated, jittered, classified retry on a shared read BLOCKING —
  amplification is bounded and permanent errors fast-fail.
- Marking IMPORTANT for "could use more attempts" with no failure path — that is at
  most NOTED, usually nothing. A finding needs a concrete storm / waste / overrun.
