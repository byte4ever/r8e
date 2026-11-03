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
   Retry-After is honored automatically. Same class: an
   `httpx.NewResponseClient` classifier shown returning a plain error for the
   `429` (the adapter adds no `StatusError` and reads no Retry-After on that
   path). When the classifier is not shown, IMPORTANT "confirm it returns a
   `StatusError` for the throttling response".

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
5. **Retry-After cut short by `MaxDelay` or the deadline.** The dependency sends
   `429 Retry-After: 1s` (or more), and `MaxDelay(150ms)` caps the hint, or the
   time left under the deadline is shorter. Propagation: the retry re-sends after
   150 to 165 ms (the capped hint, spread upward by up to 10%), into an explicit
   "back off", from every replica at about the same time. Less severe than
   BLOCKING #4 when a retry budget caps the total, but still a retry against the
   server's own signal. Remediation: `HonorRetryAfter()` (stops on a hint above
   `MaxDelay` and returns the error as is), or no retry of hinted errors
   (`RetryIf` on `RetryAfterProvider`), or raise `MaxDelay` and the budget so the
   hint fits.
6. **`PerAttemptTimeout` that never binds.** `PerAttemptTimeout(500ms)` meant to
   cut hung attempts, under callers whose stated deadline is 500 ms or less.
   Propagation: the per-attempt deadline arms no timer; a hung first attempt
   runs into the caller's deadline and the call returns the caller's context
   error, so the retry never gets its second attempt against a hang. Remediation:
   a `PerAttemptTimeout` strictly below the time the callers' deadline leaves,
   with room for the next attempt. Scope: requires the callers' deadline to be
   stated; when it is UNKNOWN, NOTED "confirm".
7. **A dead route drains the shared parent budget.** Several routes nest their
   retry budgets under one `Parent`, the context says one route can fail on its
   own while the others must keep retrying, and the children have no
   `ShieldParent()` (or the parent is too small to stay above half its capacity
   after a dead child drains `⌈child MaxTokens / 2⌉`). Propagation: the dead
   route's failures, first attempts included, keep debiting the parent; the
   parent falls to half; every sibling loses its retries during that route's
   outage. Remediation: `ShieldParent()` on the children, and a parent sized for
   the drain. Scope: when the context wants one route's storm to throttle its
   siblings, plain `Parent` is the design: not a finding.
8. **`RetryIf` refuses r8e's own per-attempt cut.** `PerAttemptTimeout(200ms)`
   with `RetryIf(func(err error) bool { return !errors.Is(err,
   context.DeadlineExceeded) })`. Propagation: a hung attempt is cut, its error
   (`ErrAttemptTimeout` around the context's `DeadlineExceeded`) matches, and
   the retry stops: the per-attempt timeout never buys a second attempt.
   Remediation: let `errors.Is(err, r8e.ErrAttemptTimeout)` through first; the
   caller's own deadline ends the loop without help.

## NOTED — observation for awareness, no action required

1. The dependency's whole-call timeout / TimeBudget *sizing* that retries must fit
   — flag and defer to review-r8e-policy-timeouts (you confirm FIT, not sizing).
2. Retrying this call also amplifies a write side effect — note and defer the
   duplicate-effect SAFETY verdict to review-r8e-policy-call.
3. `maxAttempts` is a round number but the worst-case sum demonstrably fits the
   budget and the dep is over-provisioned — no demonstrated amplification path.
4. The fn calls a nested r8e policy, so what the retry returns (wrapped after
   exhausted attempts, bare after a `RetryIf` stop, an exhausted budget, a
   `HonorRetryAfter` stop or an armed cooldown) decides what the outer breaker
   counts — note and defer to review-r8e-policy-overload.
5. `HonorRetryAfter()` without `MaxDelay` has no effect (every hint is waited in
   full, up to the deadline); its stop still debits a retry budget, where a
   `RetryIf` refusal does not. Flag the inert or budget-costing form.
6. A cooldown in the policy stops every retry while armed and returns the
   server's error as is, not `ErrRetriesExhausted`. Flag it where the retry
   count was sized for 429s; what consumes the bare error is the fallback and
   observability axes'; whether the cooldown is needed is the overload axis's.
7. A `ShieldParent` child that flaps (successes between failures) is not
   bounded: each success that lifts it above half lets the next failure reach
   the parent. Flag it when the context shows a flapping route.

## Anti-calibration (do NOT do this)

- Scoring the duplicate-side-effect SAFETY of retrying a write here — that is the
  call axis. You score the AMPLIFICATION and the CLASSIFICATION.
- Calling a budget-gated, jittered, classified retry on a shared read BLOCKING —
  amplification is bounded and permanent errors fast-fail.
- Marking IMPORTANT for "could use more attempts" with no failure path — that is at
  most NOTED, usually nothing. A finding needs a concrete storm / waste / overrun.
