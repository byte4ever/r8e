# Checklist — retry / amplification axis

Per-category checks for the retry configuration. Each check is phrased so a "yes"
to the *hazard* is an observation to score. Enumerate; do not pre-dismiss.

## Attempts & budget fit
- [ ] What is `maxAttempts`? Is it justified by the transient-fault rate, or a
      round number?
- [ ] Does the WORST-CASE sum (attempts × per-attempt latency + cumulative
      backoff) fit the call's latency budget / SLA? A 5× ConstantBackoff(200ms)
      adds ≥800ms of backoff alone — does the budget allow it?
- [ ] Is `PerAttemptTimeout` set, and does it bound each attempt independently of
      the whole-call deadline?
- [ ] Is `PerAttemptTimeout` STRICTLY shorter than the time the callers' deadline
      leaves when the call starts? One on the same instant or later arms no
      timer: a hung attempt is never cut, it runs into the caller's deadline,
      and the retry never gets its next attempt.
- [ ] With `WithTimeBudget`: the retry refuses an attempt that cannot fit with
      `ErrTimeBudgetExceeded`, but only while the context is live; once the
      caller's context has ended, it returns that context's error instead.

## Backoff & synchronization
- [ ] Is the backoff `ConstantBackoff`? Then every retrier waits the same fixed
      delay → synchronized retry waves (thundering herd) against the dependency.
- [ ] Is it `ExponentialBackoff` WITHOUT jitter? The cohort that failed together
      still retries together — mild synchronization. Prefer
      `ExponentialJitterBackoff`.
- [ ] Is jitter present at all? Absence of jitter on a shared dependency is the
      synchronization hazard.
- [ ] Is `MaxDelay` capping the exponential growth? Unbounded exponential can push
      a late attempt's wait past the call's deadline (the retry never gets to run).

## Error classification (the r8e default is the trap)
- [ ] Does the service have PERMANENT error modes (4xx auth / validation / business
      rejection)? Enumerate them from the taxonomy.
- [ ] With `WithRetry` and NO `Permanent()` / `RetryIf` / httpx classifier, the r8e
      default treats EVERY unclassified error as transient → permanent errors are
      retried `maxAttempts` times. Is classification present?
- [ ] Is a `RetryIf` predicate present, and does it actually EXCLUDE the permanent
      classes (not invert the test, not miss a class, not over-retry)?
- [ ] Are any genuinely-transient errors mis-classified Permanent (fast-fails a
      retriable fault)?

## Amplification on shared dependencies
- [ ] Is the dependency SHARED (third-party / internal hit by every replica)? A
      shared dep amplifies retries fleet-wide.
- [ ] Is a retry budget present (`WithRetryBudget` per-policy, or
      `WithSharedRetryBudget`)? Without it, a partial outage triggers a
      self-amplifying retry storm (every replica piles `maxAttempts`× load).
- [ ] Multiple policies under one gateway: are budgets nested via `Parent` so a
      storm in one leaf throttles its siblings?
- [ ] Is metastable failure possible — retries keep the dependency saturated so it
      never recovers even after the trigger clears?

## Concurrency budget
- [ ] Is `WithConcurrencyBudget` present to cap simultaneous in-flight retries +
      hedges? Without it, retries+hedges can fan out beyond the dependency's
      capacity even when each individual call is bounded.

## Retry-After / server backpressure
- [ ] Does the dependency emit `429` / `503` with a `Retry-After` header?
- [ ] Is it honored automatically — does the error implement `RetryAfterProvider`
      (httpx `StatusError` does)? A fixed backoff that IGNORES Retry-After retries
      into an explicit "back off" signal → throttle stampede.
- [ ] Is the httpx adapter used so 429/503 carry Retry-After AND 4xx classify
      Permanent without hand-written code?
- [ ] Does `MaxDelay`, or the time left under `WithTimeout` / the time budget, sit
      BELOW the Retry-After hints? `MaxDelay` caps the hint too, so the retry
      re-sends early, into the server's explicit "back off". Such errors should
      not be retried (`RetryIf` on `RetryAfterProvider`) unless the hint fits.
- [ ] Is a throttling status (`429`) both marked `Permanent` AND expected to be
      honored? `Permanent` stops the retry, so the Retry-After is never used —
      the taxonomy contradicts itself.

## Cross-axis notes (note once, defer; do NOT score here)
- [ ] Does the fn call a nested r8e policy? Exhausted attempts wrap its rejection
      (`retries exhausted: bulkhead full`), which the outer breaker counts; a
      `RetryIf` returning false or an exhausted retry budget passes it on bare,
      which `IsLocalRejection` makes neutral. Note it, defer the counting to
      review-r8e-policy-overload.
- [ ] Retrying a WRITE also amplifies the side effect — note it, defer the
      duplicate-side-effect SAFETY verdict to review-r8e-policy-call.
- [ ] The whole-call TIMEOUT / TimeBudget *sizing* the retries must fit → defer to
      review-r8e-policy-timeouts (you check FIT against the stated budget, not
      whether the budget itself is sized right).

## Red lines (any one is BLOCKING on its own)
- `WithRetry` on a SHARED dependency with NO retry budget AND no classification:
  a partial outage triggers a self-amplifying retry storm (every replica piles
  `maxAttempts`× load on a dependency that is already failing).
- `ConstantBackoff` with no jitter on a SHARED dependency (synchronized
  thundering-herd retries) combined with no retry budget.
