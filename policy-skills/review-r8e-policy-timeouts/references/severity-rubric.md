# Severity rubric — timeouts / deadlines / latency axis

Calibrated examples. Severity rests on a **demonstrated failure mode**, never on
aesthetic preference or a best-practice cited without a concrete case. When in
doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **No hard deadline under retry + hedge.** `WithRetry(5, …)` + `WithHedge(50ms)`
   with no `WithTimeout` anywhere, on the checkout critical path (p99 900 ms).
   Propagation: a slow tail × 5 attempts (plus hedge fan-out) runs with no
   ceiling — latency is unbounded. Symptom: the checkout request hangs for
   seconds while the user waits. Remediation: add a `WithTimeout` ≈ p99 × 2–4 and
   a `WithTimeBudget` to cap the retry+hedge sum.
2. **Required-companion timeout missing → build panic.** `WithCoalesce(keyFn)`
   (or `RefreshAhead(d<ttl)`) with no `WithTimeout`. Propagation: `NewPolicy`
   panics `ErrCoalesceWithoutTimeout` / `ErrRefreshAheadWithoutTimeout`. Symptom:
   the service cannot start. Remediation: add the required `WithTimeout`.
3. **Worst-case retry chain blows the SLA on a critical path.** `WithRetry(3)`
   with a per-attempt `WithTimeout(2s)` and no `WithTimeBudget`, behind a 500 ms
   SLA. Propagation: 3 × 2 s ≈ 6 s worst case — the call can run 12× the SLA.
   Symptom: a single bad-tail request blows the budget by an order of magnitude.
   Remediation: size `WithTimeout` to fit, add `WithTimeBudget(≤ budget)` to cap
   the sum.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **WithTimeout far looser than p99 and the SLA.** `WithTimeout(2s)` vs p99
   120 ms (~16×) and a stated 500 ms SLA (~4×). A single slow call blows the SLA;
   the ceiling provides almost no real protection. Acceptable only with a written
   reason the ceiling must be that loose; otherwise size it to ≈ p99 × 2–4 ≤ SLA.
2. **No sum-cap on a tight-SLA retry path.** `WithRetry(3)` on a 500 ms-SLA path
   with no `WithTimeBudget`; even with a per-attempt ceiling the SUM can exceed
   the budget. Acceptable only if the per-attempt × n already fits the budget;
   otherwise add `WithTimeBudget`.
3. **Hedge delay below p50.** `WithHedge(50ms)` while p50 is 220 ms → the hedge
   fires on the majority of calls, doubling load rather than trimming the tail.
   Acceptable only on a read with explicit headroom; otherwise size near p95.
4. **Nested timeout mismatch.** The client's own timeout is shorter than
   `WithTimeout`, so the policy deadline never bites. Not a safety hole but the
   policy knob is inert — reconcile them.
5. **r8e deadline not strictly shorter than the callers'.** `WithTimeout(2s)`
   on calls whose callers set a 500 ms deadline (or `WithTimeout(1s)` under a
   1 s edge deadline already partly spent). Propagation: the timeout arms no
   timer; a hanging dependency is ended by the caller's deadline and returns the
   caller's context error; `OnTimeout` never fires and the `Timeouts` counter
   stays at zero while callers time out; under a breaker that makes caller
   cancellations neutral, the hang never trips it (overload axis). Remediation:
   size `WithTimeout` (and `PerAttemptTimeout`, the budget) strictly below the
   time the callers' deadline leaves when the call starts. Scope: requires the
   callers' deadline to be stated; when it is UNKNOWN, see NOTED #4.

## NOTED — observation for awareness, no action required

1. No `AdaptiveTimeout` despite a tight SLA and variable latency — a static
   ceiling works, adaptive would tighten below it from live successes. Flag for
   awareness; the static ceiling is correct.
2. A `WithTimeBudget` would cap the retry sum slightly tighter, but a hard
   `WithTimeout` already bounds the whole call within budget — no demonstrated
   overrun.
3. The call ignores `ctx`, so the timeout can only stop *waiting*, not cancel the
   in-flight work — flag and defer the ctx-honoring defect to
   review-r8e-policy-call (out of this axis's lane to *fix*; note its effect on
   your timeout).
4. The callers' deadline is not stated, and nothing in the policy depends on
   whose deadline ends a hang — flag "confirm `WithTimeout` ends strictly before
   the callers' deadline". (When a classifier makes caller cancellations
   neutral, the overload axis scores it.)

## Anti-calibration (do NOT do this)

- Scoring the retry COUNT or backoff curve here — that is the retry axis.
- Calling a generous `WithTimeout` ≈ p99 × 3 "too loose" with no SLA it breaches
  and no demonstrated overrun — that is at most NOTED, usually nothing.
- Marking BLOCKING for "no AdaptiveTimeout" when a static ceiling already bounds
  the call within budget — adaptive is an optimisation, not a missing bound.
- Scoring the ctx-honoring defect itself as a timeout finding — it is the call
  axis's; you only note its effect on whether your deadline can cancel.
- Re-litigating a stated caller deadline that is several times the r8e deadline
  ("but how much is left?") with no sign that the call runs late in the request:
  take it as binding. Flag the time left only when the margin is thin or the
  context shows the deadline largely spent.
