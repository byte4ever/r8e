# Good reviews — timeouts / deadlines / latency axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together.
These are the highest-leverage examples — match their register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. Hard-deadline presence: there is **no** `WithTimeout` anywhere in the policy.
2. `WithRetry(5, ConstantBackoff(200ms))` is present — re-execution with no
   ceiling.
3. `WithHedge(50ms)` is present — a second concurrent execution per slow call.
4. Path criticality: **checkout critical path** — unbounded latency here is a
   user-facing incident.
5. Worst-case chain: p99 900 ms × 5 attempts + 4 × 200 ms backoff ≈ **5.3 s** with
   no cap, and that is before the hedge fan-out adds concurrent tails.
6. `WithTimeout` vs p99: cannot size what does not exist — there is no ceiling to
   compare against the 900 ms p99.
7. `WithTimeout` vs SLA/budget: no SLA is stated, but a financial checkout call
   with no deadline at all is unbounded by construction.
8. No `WithTimeBudget` to cap the retry+hedge sum — nothing bounds the total.
9. Per-attempt vs total: neither exists; every attempt runs to the gateway's own
   (unknown) ceiling.
10. Hedge delay vs latency: delay = 50 ms while **p50 = 220 ms** → the hedge fires
    on the *majority* of calls (its sizing is wrong AND there is no deadline to
    bound the hedged tails).
11. No `AdaptiveTimeout` — moot, there is no base `WithTimeout` for it to ride.
12. No `AdaptiveHedge` — the 50 ms delay is static and mis-sized.
13. `WithCoalesce(func(ctx) string { return "" })` is present.
14. `WithCoalesce` requires `WithTimeout`; `WithTimeout` is absent → `NewPolicy`
    panics `ErrCoalesceWithoutTimeout`. The policy **cannot build**.
15. No `PropagateDeadline` / `RespectInboundDeadline` — no fan-out budget either,
    moot given (14) and the absent budget.
16. No httpx deadline header wiring — moot, no budget to propagate.
17. The fn ignores the policy `ctx`, so even a hypothetical `WithTimeout` could
    only stop *waiting*, not cancel the charge (defer the ctx-honoring defect to
    review-r8e-policy-call; note its effect on cancellability here).

### Phase 2 — Adversarial scenarios
- **Unbounded latency on checkout.** Trigger: a slow gateway tail (p99 900 ms or
  worse). Propagation: 5 retries with no ceiling + 4 × 200 ms backoff ≈ 5 s, plus
  hedge tails; nothing caps it. Symptom: the checkout request hangs for seconds.
  Detectability: upstream latency dashboards — but only after users wait.
- **Build-time panic.** Trigger: `NewPolicy` runs. Propagation: `WithCoalesce`
  without `WithTimeout` → `ErrCoalesceWithoutTimeout` panic. Symptom: the service
  cannot start at all. Detectability: immediate, at boot.
- **Hedge load doubling.** Trigger: any call slower than 50 ms (p50 is 220 ms, so
  ~all of them). Propagation: the hedge fires constantly, each tail unbounded.
  Symptom: ~2× load on a shared third-party gateway with no latency ceiling.
  Detectability: gateway request-rate, after the fact.
- **No bound on the retry chain under a partial outage.** Trigger: the gateway
  browns out (slow 200s). Propagation: every attempt runs long, 5× per call, no
  `WithTimeBudget`. Symptom: latency stacks fleet-wide. Detectability: only via
  aggregate latency, no in-policy ceiling fires.
- **Timeout cannot ever bite.** Trigger: even if a `WithTimeout` were added, the
  fn ignores ctx. Propagation: the deadline stops waiting but the charge runs on.
  Symptom: an orphaned charge after the caller gave up. Detectability: none
  in-policy (the ctx defect is the call axis's to fix; here it means no deadline
  could rescue the latency).

### Phase 3 — Scoring
- BLOCKING: #1+#2+#3+#4+#5 (retry+hedge on the checkout critical path with NO hard
  deadline → unbounded latency), and #13+#14 (`WithCoalesce` without `WithTimeout`
  → `NewPolicy` panics, the policy cannot build). Scenarios "unbounded latency on
  checkout" and "build-time panic".
- IMPORTANT: #10 (hedge delay 50 ms ≪ p50 220 ms → load doubling) — but it is
  moot under the two BLOCKINGs since the policy cannot build and has no ceiling.
- NOTED: #17 (ctx ignored → defer to call axis), #15/#16 (no propagation — moot).

### Phase 4 — Verdict
Two independent BLOCKINGs: the policy cannot even build (`ErrCoalesceWithoutTimeout`),
and were it fixed, retry + hedge on the checkout critical path have no hard
deadline and no time budget — latency is unbounded. **REJECT.** Remediation: add
`WithTimeout` ≈ p99 × 2–4 (so ~2 s–3.6 s, then tightened to the checkout SLA) AND
a `WithTimeBudget` capping the retry+hedge sum; the `WithCoalesce` either gains
its required `WithTimeout` or is removed.

### Phase 5 — Meta-critique
1. Most likely leniency: treating "5.3 s isn't *that* long" as tolerable — but on
   a synchronous checkout path with no ceiling, the real worst case is the
   gateway's own tail × 5 with hedge fan-out, effectively unbounded; no leniency
   warranted.
2. Category I could have under-examined: whether an inbound request deadline
   already bounds this from above — but no `RespectInboundDeadline`/budget is
   present, so the policy itself imposes no bound regardless.
3. To a reviewer who reached APPROVE: name the ceiling. There is none — and the
   policy panics at `NewPolicy` before it can run. Unbuildable plus unbounded on a
   money-moving critical path is not approvable.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. Hard-deadline presence: `WithTimeout(2*time.Second)` is present — the call is
   bounded.
2. `WithRetry(3, ExponentialBackoff(100ms))` is present.
3. No `WithHedge` → no hedge-delay sizing concern.
4. Latency profile: p50 ≈ 35 ms, p99 ≈ 120 ms, both stated.
5. `WithTimeout` vs p99: 2 s ÷ 120 ms ≈ **16×** p99 — far looser than the ≈ p99 ×
   2–4 range. A single slow call can run ~2 s.
6. Stated SLA: **respond within 500 ms**. `WithTimeout(2s)` is **4× the SLA** — one
   slow call blows the SLA on its own.
7. Worst-case retry chain: per-attempt window is the full 2 s; 3 attempts ≈
   **6 s** (plus exponential backoff waits) vs a 500 ms SLA — wildly over budget.
8. No `WithTimeBudget` to cap the retry SUM — nothing keeps the total inside 500 ms.
9. Per-attempt vs total: `WithTimeout` is the per-call ceiling but each retry can
   take the whole 2 s; there is no total cap.
10. `AdaptiveTimeout`: absent. Latency is fairly stable (35/120 ms) but the SLA is
    tight — adaptive would tighten the ceiling below 2 s from live successes.
11. No `PropagateDeadline`/`RespectInboundDeadline` — this is an internal read,
    not a stated fan-out or ingress boundary; no propagation required here.
12. `WithCache` is present but with no `RefreshAhead`, so no required-`WithTimeout`
    companion is triggered beyond the one already present (and it is present).
13. No `WithCoalesce` → no `ErrCoalesceWithoutTimeout` exposure.
14. Nested client timeout: `catalog.Get`'s own timeout vs `WithTimeout(2s)` is
    UNKNOWN — could fire first and make the 2 s inert, but cannot be confirmed.
15. The fn honors ctx, so the deadline can actually cancel — the timeout will bite
    once it is sized correctly (ctx-honoring itself is the call axis's to score).

### Phase 2 — Adversarial scenarios
- **One slow call blows the SLA.** Trigger: a tail call near 2 s (or the gateway
  browning out). Propagation: `WithTimeout(2s)` lets it run to 2 s. Symptom: a
  500 ms-SLA read returns in ~2 s — SLA breach on a single call. Detectability:
  SLA latency dashboards.
- **Retry chain × budget.** Trigger: two transient 5xx then success. Propagation:
  attempt 1 (up to 2 s) + backoff + attempt 2 (up to 2 s) + … ≈ up to 6 s.
  Symptom: a read that should be ≤ 500 ms takes seconds. Detectability: p99 on the
  catalog read, after the fact.
- **No sum-cap under a partial outage.** Trigger: the catalog browns out.
  Propagation: every call burns its 3 attempts at up to 2 s each, no
  `WithTimeBudget`. Symptom: latency stacks across many callers. Detectability:
  aggregate latency; no in-policy total ceiling fires.
- **Cache hit short-circuits.** Trigger: a warm key. Propagation: `WithCache`
  returns before the timeout matters. Symptom: fast, correct. (A scenario that
  does NOT fail — bounds the surface to cold/miss paths.)
- **Adaptive would help but is absent.** Trigger: stable 35/120 ms latency with a
  tight SLA. Propagation: the static 2 s ceiling never tightens toward the real
  distribution. Symptom: the ceiling is decorative under normal latency; only the
  SLA breach above bites. Detectability: comparing the ceiling to observed p99.

### Phase 3 — Scoring
- BLOCKING: none. The call IS bounded (`WithTimeout` present), and on a degrade-able
  internal read an over-loose ceiling and unbounded retry SUM blow the SLA but do
  not produce an unbounded or unbuildable policy.
- IMPORTANT: #5+#6 (`WithTimeout(2s)` ≈ 16× p99 and 4× the 500 ms SLA — a single
  slow call breaches the SLA); #7+#8 (no `WithTimeBudget`, worst-case chain ≈ 6 s
  ≫ 500 ms budget). Both rest on the stated SLA and concrete chain math.
- NOTED: #10 (no `AdaptiveTimeout` despite a tight SLA — static ceiling works,
  adaptive would tighten it); #14 (nested client timeout UNKNOWN — defer the
  cross-cut, reconcile if confirmed).

### Phase 4 — Verdict
No BLOCKING — the call is bounded and the path degrades acceptably. Two IMPORTANT
findings resting on the stated 500 ms SLA: the 2 s ceiling is ~4× the SLA so a
single slow call breaches it, and with no `WithTimeBudget` the worst-case 3-attempt
chain (≈ 6 s) is an order of magnitude over budget. **CONDITIONAL APPROVE**,
accepted risks: size `WithTimeout` to ≈ p99 × 2–4 within the 500 ms SLA (~250–400 ms)
AND add `WithTimeBudget(≤ 500ms)` to cap the retry sum; until then a single slow
call or a brownout breaches the SLA.

### Phase 5 — Meta-critique
1. Most likely leniency: accepting "it has a `WithTimeout`, so it is bounded" — it
   is bounded at 2 s, but the *stated* bound is 500 ms, so the policy is bounded to
   the wrong number; I scored that, not the mere presence.
2. Under-examined: whether `catalog.Get` has its own shorter timeout that would
   mask the 2 s (noted UNKNOWN as cross-cut) — if it fires at, say, 300 ms the
   IMPORTANT findings soften, but that cannot be assumed.
3. To a REJECT reviewer: the call is bounded and the path degrades acceptably
   (`WithFallback`, `WithCache`); a mis-sized ceiling and a missing sum-cap on an
   internal read are real SLA risks but not an unbounded or unbuildable policy —
   IMPORTANT, not BLOCKING.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. Hard-deadline presence: `WithTimeout(300ms)` is present — the call is bounded.
2. Latency profile: p50 ≈ 30 ms, p99 ≈ 80 ms; budget for this call: **300 ms** —
   all stated.
3. `WithTimeout` vs p99: 300 ms ÷ 80 ms ≈ **3.75×** p99 — squarely in the ≈ p99 ×
   2–4 range. Tight enough to protect, loose enough not to false-trip.
4. `WithTimeout` vs budget: 300 ms **equals** the stated 300 ms budget — the
   ceiling is the budget.
5. `WithRetry(3, ExponentialJitterBackoff(20ms), MaxDelay(150ms))` — jittered,
   capped backoff.
6. Worst-case chain vs the ceiling: the single hard `WithTimeout(300ms)` bounds
   the WHOLE call including all 3 retries and their capped backoff — the chain
   cannot exceed 300 ms regardless of attempt count.
7. No `WithHedge` → no hedge-delay sizing concern (and a read would be safe to
   hedge anyway).
8. No `AdaptiveHedge` — moot, no hedge.
9. `AdaptiveTimeout`: absent. Latency is stable (30/80 ms) and the static 300 ms
   ceiling already fits the budget — adaptive would only tighten an already-correct
   ceiling. Not required.
10. `WithTimeBudget`: absent. A sum-cap would bound the retry total slightly
    tighter, but the hard `WithTimeout(300ms)` already caps the whole call within
    budget — no demonstrated overrun.
11. `WithRetryBudget` present — retry amplification handled (cross-axis; not a
    latency bound concern).
12. No `WithCoalesce` / `RefreshAhead` → no required-`WithTimeout` companion panic
    exposure (and `WithTimeout` is present anyway).
13. No fan-out / ingress boundary stated → no `PropagateDeadline` /
    `RespectInboundDeadline` required here.
14. Nested client timeout: not stated as a competing timeout; the fn honors ctx so
    the policy's 300 ms deadline propagates to the HTTP call and bites.
15. The fn honors ctx (cancellation/deadline propagate end to end) — the timeout
    can actually cancel in-flight work, so the 300 ms ceiling is real, not just a
    stop-waiting (the ctx-honoring itself is the call axis's, but it ENABLES the
    deadline to bite here).

### Phase 2 — Adversarial scenarios
- **A slow tail.** Trigger: a call past 300 ms. Propagation: `WithTimeout(300ms)`
  fires; ctx is honored so the HTTP call is cancelled. Symptom: clean `ErrTimeout`
  at the budget, fallback to empty recs. Does not fail (bounded at the budget).
- **Retry chain length.** Trigger: two transient 5xx then success. Propagation:
  attempts + jittered backoff (capped at 150 ms) all run UNDER the single 300 ms
  ceiling — the ceiling, not the attempt count, bounds the total. Symptom: the call
  still returns within budget or surfaces `ErrTimeout`. Does not fail.
- **No sum-cap.** Trigger: a brownout exhausting retries. Propagation: the 300 ms
  `WithTimeout` caps the whole chain; absence of `WithTimeBudget` changes nothing
  because the hard ceiling already bounds the sum. Symptom: bounded at 300 ms then
  empty fallback. Does not fail (a `WithTimeBudget` is at most a marginal tighten).
- **Static ceiling vs variable latency.** Trigger: latency stays at 30/80 ms.
  Propagation: the static 300 ms ceiling fits the budget; no `AdaptiveTimeout`
  needed. Symptom: correct bound. Does not fail.
- **Cancellation propagates.** Trigger: the caller cancels. Propagation: ctx → the
  HTTP call aborts under the 300 ms ceiling. Symptom: clean abort. Does not fail.
Fewer than 5 *failing* scenarios are findable: the call has a hard deadline equal
to its stated budget and ≈ p99 × 3.75, the single ceiling bounds the entire retry
chain, ctx is honored so the deadline truly cancels, and there is no fan-out or
ingress boundary requiring propagation — its latency surface is structurally
bounded to "returns within 300 ms or surfaces `ErrTimeout`."

### Phase 3 — Scoring
- BLOCKING: none. The call is hard-bounded at the budget and the ceiling caps the
  whole retry chain.
- IMPORTANT: none in this axis's lane.
- NOTED: a `WithTimeBudget` would cap the retry sum slightly tighter, but the hard
  `WithTimeout(300ms)` already bounds it (#10); no `AdaptiveTimeout` despite stable
  latency — not required since the static ceiling already fits the budget (#9);
  retry-budget/bulkhead/breaker *sizing* are out of lane (defer).

### Phase 4 — Verdict
Phases 1–2 produced no latency-bound observation: a hard `WithTimeout(300ms)`
equal to the budget and ≈ p99 × 3.75 caps the whole call including the 3 capped,
jittered retries, and ctx is honored so the deadline truly bites. Every adversarial
scenario is dismissable in writing. **APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the context says "good" and the
   comments already explain the sizing. I re-derived the bound from the numbers
   (300 ms = budget ≈ p99 × 3.75; single ceiling caps the chain), not from the
   label or the comments.
2. Under-examined: whether `recommender.Get` carries its OWN internal timeout that
   could fire before 300 ms and make the ceiling inert — the context does not state
   one and the fn honors the policy ctx, so the 300 ms deadline propagates; I would
   reopen on evidence of a competing client timeout.
3. To a REJECT reviewer: name the unbounded path. There is none — the single hard
   `WithTimeout(300ms)` bounds the entire retry chain within the stated budget, and
   the honored ctx makes that deadline actually cancel the in-flight call.

FINAL VERDICT: APPROVE
