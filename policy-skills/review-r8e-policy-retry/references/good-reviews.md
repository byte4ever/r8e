# Good reviews — retry / amplification axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together.
These are the highest-leverage examples — match their register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. `maxAttempts` = 5 (`WithRetry(5, …)`).
2. Backoff: `ConstantBackoff(200ms)` — fixed delay, no jitter.
3. Jitter: **absent** → every retrier that fails at t re-fires at t+200ms in
   lockstep.
4. `MaxDelay`: absent (irrelevant for constant, but no cap on the schedule).
5. `PerAttemptTimeout`: absent.
6. Service taxonomy has permanent modes: `402 card_declined`, `400
   invalid_request` (the context states retrying never helps).
7. Classification: **none** — no `Permanent()`, no `RetryIf`, no httpx classifier.
8. **Default-transient trap fires:** unclassified → the r8e default retries the 402
   and 400 all 5 times.
9. Dependency is SHARED — a single third-party gateway hit by every replica
   (context: "single shared third-party dependency hit by every replica").
10. Retry budget: **absent** — no `WithRetryBudget` / `WithSharedRetryBudget`.
11. Concurrency budget: absent — nothing caps in-flight retries+hedges.
12. Retry-After: the gateway emits `429 Retry-After`, but the httpx adapter is not
    shown, so it is NOT honored → the fixed 200ms backoff ignores the server's
    signal.
13. Worst-case attempts vs budget: 5 attempts × p50 220ms + 4×200ms backoff ≈
    1.9s; no `WithTimeout` configured to bound it → unbounded retry latency on the
    checkout critical path.
14. Retrying this charge also AMPLIFIES the side effect (double charge) — note;
    the duplicate-effect SAFETY verdict is the call axis's lane.
15. `RetryIf` predicate: none to evaluate.
16. First-attempt-only handling: `402`/`400` should fast-fail on attempt 1 but are
    retried — mishandled.

### Phase 2 — Adversarial scenarios
- **Retry storm on the shared gateway.** Trigger: gateway brownout (5xx spike).
  Propagation: no budget → every replica retries 5× → aggregate load multiplies on
  an already-failing shared dependency → metastable failure (it cannot drain).
  Symptom: the gateway stays down long after the trigger clears. Detectability:
  gateway 5xx + the fleet's outbound RPS spike.
- **Synchronized thundering herd.** Trigger: a transient blip fails a cohort at
  the same instant. Propagation: `ConstantBackoff(200ms)`, no jitter → the whole
  cohort re-fires at t+200ms, t+400ms, … in lockstep. Symptom: 200ms-period load
  waves. Detectability: periodic spikes in the gateway request rate.
- **Declined-card retry waste.** Trigger: `402 card_declined`. Propagation:
  unclassified → 5 retries on a permanently-doomed request. Symptom: 5× load per
  decline + 5× latency surfaced to the user, never succeeding. Detectability:
  gateway 402 rate vs attempt count.
- **429 ignored → throttle stampede.** Trigger: the gateway returns `429
  Retry-After: 2s`. Propagation: the fixed 200ms backoff ignores it (no
  `RetryAfterProvider`) → the client retries 10× sooner than asked. Symptom: the
  throttle never lifts; the client deepens its own overload. Detectability: 429
  rate climbing under retry pressure.
- **Budget overrun.** Trigger: repeated transient 5xx. Propagation: 5 attempts +
  4×200ms backoff ≈ 1.9s with no `WithTimeout` ceiling. Symptom: checkout latency
  blows any reasonable budget on the critical path. Detectability: p99 of the
  charge call.

### Phase 3 — Scoring
- BLOCKING: #2+#3+#9+#10 (ConstantBackoff, no jitter, shared dep, no budget →
  synchronized retry storm), #6+#7+#8 (permanent errors retried 5×), scenarios
  "retry storm", "synchronized thundering herd", "declined-card waste".
- IMPORTANT: #12 (Retry-After ignored — throttle stampede; remediation is the
  httpx classifier), #13 (worst-case attempts unbounded by any timeout — the
  amplification is also a latency blow-up).
- NOTED: #14 (retrying the charge also amplifies the WRITE side effect — defer the
  duplicate-effect SAFETY to the call axis), #11 (no concurrency budget — folds
  into the budget finding).

### Phase 4 — Verdict
Multiple BLOCKING: a shared-dependency retry storm with synchronized
no-jitter waves and no budget, and permanent errors retried 5× by the
default-transient trap. **REJECT.** Remediation: classify `402`/`400` as
`Permanent` (or use the httpx classifier), switch to
`ExponentialJitterBackoff(base)` + `MaxDelay(cap)`, add `WithRetryBudget` (or
`WithSharedRetryBudget`), and honor Retry-After via httpx `StatusError`.

### Phase 5 — Meta-critique
1. Most likely leniency: treating "it's only 5 attempts" as small — but ×fleet on
   a shared dep under a brownout, 5× aggregate is exactly the storm amplifier; no
   leniency warranted.
2. Category I could have under-examined: whether the gateway itself rate-limits
   the client globally (would cap the storm) — the context gives no such limit, so
   I cannot assume one.
3. To a reviewer who reached APPROVE: name the bound on aggregate retry load.
   There is none — no budget, no jitter, no classification, on a shared dep. That
   is the textbook metastable-failure recipe, not a tuning nit.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. `maxAttempts` = 3 (`WithRetry(3, …)`).
2. Backoff: `ExponentialBackoff(100ms)` — grows, but **no jitter**.
3. Jitter: absent → the cohort that failed together still retries together (mild
   synchronization).
4. `MaxDelay`: absent → 100ms, 200ms exponential; with only 3 attempts the
   schedule is short, so the missing cap does not bind here.
5. `PerAttemptTimeout`: absent (a single `WithTimeout(2s)` bounds the whole call —
   sizing is the timeouts axis's lane).
6. Service taxonomy: `404 not_found` is permanent; `5xx` + timeouts transient.
7. Classification: **none** — `404` is unclassified.
8. **Default-transient trap fires:** the permanent `404` is retried 3×.
9. Dependency is SHARED ("internal service shared by many callers").
10. Retry budget: **absent** → a partial outage amplifies aggregate load by 3×
    fleet-wide.
11. Concurrency budget: absent.
12. Retry-After: the context states "No Retry-After" → nothing to honor; not a
    finding.
13. Worst-case attempts vs budget: 3 × p99 120ms + 100ms + 200ms backoff ≈ 660ms
    vs the 500ms SLA — the retry schedule can exceed the SLA on a slow path
    (latency consequence of the un-budgeted retries).
14. This is a read → no write side effect to amplify (bounds the surface).
15. `RetryIf`: none to evaluate.
16. First-attempt handling: `404` should fast-fail but is retried — mishandled.

### Phase 2 — Adversarial scenarios
- **Amplification under partial outage.** Trigger: catalog 5xx spike. Propagation:
  no budget → every caller retries 3× on a shared internal dep. Symptom: 3×
  aggregate load while it is already struggling. Detectability: catalog inbound RPS
  vs error rate. (IMPORTANT, not BLOCKING — an idempotent read, no metastable
  side effect, internal dep typically over-provisioned.)
- **404 retried 3×.** Trigger: `GET` a missing product. Propagation: unclassified
  permanent → 3 attempts. Symptom: 3× latency on every miss + 3× load on the
  not-found path. Detectability: 404 rate vs attempt count.
- **Mild synchronized retries.** Trigger: a cohort fails together on one 5xx blip.
  Propagation: `ExponentialBackoff` with no jitter → the cohort re-fires together
  at +100ms, +200ms. Symptom: small load ripples. Detectability: minor periodic
  bumps. (IMPORTANT/NOTED — internal, small effect.)
- **SLA overrun on a slow path.** Trigger: repeated transient near p99.
  Propagation: 3 attempts + 300ms backoff ≈ 660ms > 500ms SLA. Symptom: the
  retry schedule can breach the SLA. Detectability: p99 of the read.
- **No write to double.** Idempotent read → retry cannot amplify a side effect
  (bounds the surface).

### Phase 3 — Scoring
- BLOCKING: none. An idempotent internal read with bounded attempts — no
  metastable storm, no doomed-write amplification.
- IMPORTANT: #9+#10 (no retry budget on a shared dependency → 3× amplification
  under a partial outage), #6+#7+#8 (the permanent `404` is unclassified → retried
  3×, wasting attempts and 3× latency on a miss).
- NOTED: #2+#3 (exponential without jitter → mild synchronization; prefer
  `ExponentialJitterBackoff`), #4 (no `MaxDelay` — does not bind at 3 attempts),
  #13 (SLA-fit is tight — the *sizing* of the 500ms budget is the timeouts axis;
  the retry's FIT against it is the note here).

### Phase 4 — Verdict
No BLOCKING; two IMPORTANT (no budget on a shared dep; `404` unclassified).
**CONDITIONAL APPROVE**, accepted risks: add `WithRetryBudget` to cap the 3×
amplification on the shared catalog, and classify `404` `Permanent` (or use the
httpx classifier) so misses fast-fail; switch to `ExponentialJitterBackoff` to
remove the residual synchronization.

### Phase 5 — Meta-critique
1. Most likely leniency: waving the missing budget through because the dep is
   "internal and probably fine" — I kept it IMPORTANT, not NOTED, because shared +
   no budget is a real amplifier even when the incident grade is lower than the
   payment fixture.
2. Under-examined: whether the catalog client itself retries internally (nested
   retries would multiply the 3×) — noted as cross-axis / unknown.
3. To a REJECT reviewer: there is no metastable storm here — the call is an
   idempotent read on an internal dep, attempts are bounded at 3, no Retry-After to
   ignore, no write to double. The findings are amplification + waste, fixable
   without a redesign — IMPORTANT, not BLOCKING.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. `maxAttempts` = 3 (`WithRetry(3, …)`).
2. Backoff: `ExponentialJitterBackoff(20ms)` — exponential AND jittered → no
   synchronized cohort.
3. Jitter: **present** → retriers desynchronize.
4. `MaxDelay(150ms)`: present → caps the exponential growth so a late attempt's
   wait cannot balloon past the budget.
5. `PerAttemptTimeout`: not set, but `WithTimeout(300ms)` bounds the whole call
   (sizing is the timeouts axis; here it confirms the retry schedule fits).
6. Service taxonomy: `4xx` permanent, `5xx`/timeouts transient, `429` carries
   Retry-After.
7. Classification: `4xx` marked `r8e.Permanent` → permanent errors fast-fail, no
   wasted attempts. The default-transient trap does NOT fire.
8. Dependency is SHARED ("internal service shared across the fleet").
9. Retry budget: `WithRetryBudget(MaxTokens(10), TokenRatio(0.1))` present → caps
   aggregate retry pressure on the shared dep → no storm.
10. Concurrency budget: not separately set, but a `WithBulkhead(32)` sized to the
    connection pool bounds in-flight load (overload axis's lane; here it confirms
    fan-out is capped).
11. Retry-After: `429` honored automatically via the classified boundary
    (`RetryAfterProvider`) → no throttle stampede.
12. Worst-case attempts vs budget: 3 attempts, capped backoff ≤150ms each, p99
    80ms, all under the 300ms `WithTimeout` → the schedule fits.
13. This is a read → no write side effect to amplify.
14. `RetryIf`: not needed — `Permanent` classification at the boundary covers it.
15. First-attempt handling: a permanent `4xx` fast-fails on attempt 1 (classified);
    transients retry within budget.

### Phase 2 — Adversarial scenarios
- **Shared-dep brownout.** Trigger: recommender 5xx spike across the fleet.
  Propagation: `WithRetryBudget(MaxTokens(10), TokenRatio(0.1))` gates retries —
  once tokens drain, retries stop. Symptom: aggregate retry load is capped; no
  storm. Does not fail.
- **Permanent 4xx.** Trigger: `403`. Propagation: classified `Permanent` → no
  retry, fast surface. Symptom: a single attempt. Does not fail.
- **Synchronized waves?** Trigger: a cohort fails together. Propagation:
  `ExponentialJitterBackoff` desynchronizes them. Symptom: spread-out retries, no
  herd. Does not fail.
- **429 throttle.** Trigger: server `429 Retry-After`. Propagation: honored
  automatically → the client waits as instructed. Symptom: backed-off retry. Does
  not fail.
- **Budget overrun.** Trigger: repeated transients. Propagation: 3 attempts,
  `MaxDelay(150ms)` cap, p99 80ms, all under `WithTimeout(300ms)`. Symptom:
  worst-case fits the deadline. Does not fail.
Fewer than 5 *failing* scenarios are findable: the retry is bounded
(3 attempts), jittered + capped (no synchronization), budget-gated (no storm),
classified (no wasted permanent retries), and Retry-After-honoring (no
stampede) — its amplification surface is structurally bounded.

### Phase 3 — Scoring
- BLOCKING: none.
- IMPORTANT: none in this axis's lane.
- NOTED: the 300ms `WithTimeout` *sizing* and the bulkhead *sizing* are out of
  lane (defer to timeouts / overload); the retry config itself is clean.

### Phase 4 — Verdict
Phases 1–2 produced no retry-amplification observation, and every adversarial
scenario is dismissable in writing. **APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the context says "good." I
   re-derived bounded-ness from the config: jitter + cap (no waves), budget (no
   storm), `Permanent` classification (no wasted retries), Retry-After honored —
   not from the label.
2. Under-examined: whether the recommender client retries internally (nesting
   would multiply attempts) — the context shows a single typed call with no
   internal guard, so no compounding is evident; I would reopen on new evidence.
3. To a REJECT reviewer: name the amplification path. There is none — the retry is
   jittered, capped, budget-gated, classified, and Retry-After-aware on an
   idempotent read; re-running it within a drained token budget changes nothing on
   the shared dependency.

FINAL VERDICT: APPROVE
