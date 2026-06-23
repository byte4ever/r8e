# Good reviews — observability / ops axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together.
These are the highest-leverage examples — match their register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. Policy name is `""` (empty) → effectively anonymous; no meaningful identity in
   the health registry.
2. The policy IS a `NewPolicy` object (not an anonymous `r8e.Do`), so it registers
   — but under an empty name, so it is unfindable in practice.
3. Failure modes the policy actually has: coalesce, retry, hedge, fallback. No
   breaker, no bulkhead, no SLO.
4. `WithHooks` is ABSENT entirely.
5. No `OnRetry` → the 5-attempt retry storm (including on declined cards) surfaces
   to no log/alert.
6. No `OnFallbackUsed` → the fallback `ChargeResult{Status:"ok"}` activating (a
   fabricated success on a financial path) is invisible to alerting.
7. No `OnHedge` → the hedge firing (duplicate charge risk) surfaces nowhere.
8. Metrics counters DO still increment automatically (retry/hedge/fallback) —
   record this; the gap is hooks→alerting, not "no metrics."
9. `WithReadinessImpact()` IS present.
10. The gated dependency is a **third-party payment gateway**, explicitly a
    **single shared dependency hit by every replica** (topology line).
11. Fleet-wide flip: the gateway blips → the readiness condition ties every pod's
    `/readyz` to the shared gateway → every replica flips NotReady together.
12. Symptom of #11: a downstream wobble takes the ENTIRE checkout service NotReady
    at once — a self-inflicted mass outage on the checkout critical path.
13. No `r8eotel.Register` / `r8eotel.Trace` — but the context never states an OTel
    stack, so this is at most NOTED.
14. Code-only patterns present: `WithCoalesce` (closure) and `WithFallback`
    value-vs-func aside — coalesce is absent from any emitted `PolicyConfig` JSON;
    if operators expected JSON to describe the policy, coalescing is invisible.
15. `Reconfigure` could not add a breaker later — only retune existing patterns;
    noted for operability expectations.
16. The empty coalesce key (`return ""`) means coalescing never engages anyway —
    an operability dead pattern (cross-ref fallback axis for key correctness;
    here only that it is dead and unobservable).
17. Of the modes that would page someone (duplicate charge, fabricated success),
    NONE is alertable — all silent.

### Phase 2 — Adversarial scenarios
- **Fleet-wide readiness flip.** Trigger: the shared payment gateway returns 5xx
  for 30 s. Propagation: `WithReadinessImpact()` ties pod readiness to the shared
  gateway → every replica's readiness condition flips together. Symptom: the whole
  checkout service goes NotReady, the LB pulls it all — a full outage from a brief
  blip. Detectability: visible as a total `/readyz` drop, but by then it is an
  incident, not a warning.
- **Silent fallback-fabricated success.** Trigger: gateway down, retries
  exhausted. Propagation: `WithFallback{Status:"ok"}` returns; with no
  `OnFallbackUsed`, nothing fires. Symptom: customers see "charged" while no money
  moved, and ops gets no signal. Detectability: only via downstream reconciliation
  — invisible in-policy.
- **Invisible retry storm.** Trigger: `402 card_declined`. Propagation: 5 retries
  with no `OnRetry`. Symptom: 5× gateway load per decline, no alert. Detectability:
  only the gateway's own 402 rate; the client policy is blind.
- **Unfindable health identity.** Trigger: on-call triages a checkout incident.
  Propagation: the policy registered under `""` → no name to query. Symptom: the
  breaker/retry/readiness state cannot be located by policy name. Detectability:
  the absence itself is the problem.
- **Config-only operability gap.** Trigger: an operator pulls the emitted JSON to
  "see the policy." Propagation: `WithCoalesce` (code-only) is silently absent.
  Symptom: the operator believes there is no coalescing when there is (a dead one).
  Detectability: none from JSON.

### Phase 3 — Scoring
- BLOCKING: #9+#10+#11+#12 and scenario "fleet-wide readiness flip" —
  `WithReadinessImpact()` on a shared third-party gateway ties the whole fleet's
  readiness to a single downstream blip (mass self-inflicted outage).
- IMPORTANT: #1+#2 (empty name → no health identity); #4–#7 + scenario "silent
  fallback / invisible retry storm" (no hooks on present failure modes → the
  duplicate-charge / fabricated-success / retry-storm events are invisible to
  alerting).
- NOTED: #13 (OTel — context doesn't mention it), #14–#16 (code-only coalesce
  absent from JSON; the dead empty key is a fallback-axis concern).

### Phase 4 — Verdict
A BLOCKING readiness red line (shared-dep gating → fleet-wide flip) plus two
IMPORTANT operability holes (anonymous identity, no hooks on any failure mode).
**REJECT.** Remediation: remove `WithReadinessImpact()` from this shared-dependency
policy (leave it informational); give the policy a real name; wire
`WithHooks{OnRetry, OnHedge, OnFallbackUsed}` to metrics/logs so the duplicate-charge
and fabricated-success paths are alertable.

### Phase 5 — Meta-critique
1. Most likely leniency: treating the readiness gate as "merely a misconfiguration"
   rather than a fleet-flip outage path — I scored it BLOCKING because the topology
   line states the gateway is shared by every replica, so the flip is correlated.
2. Category I could have under-examined: whether there is a separate readiness
   condition unrelated to the gateway that would dilute the flip — there is no
   breaker here, but the explicit `WithReadinessImpact()` on the only dependency
   makes the correlation total.
3. To a reviewer who reached APPROVE: name how the fleet recovers when one gateway
   blip pulls every replica's `/readyz` simultaneously — it does not, it is a mass
   outage; that is BLOCKING regardless of how clean the rest reads.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. Policy name is `"catalog-read"` — non-empty, meaningful → auto-registered for
   health. Good.
2. Built as a `NewPolicy` object, not an anonymous `r8e.Do` → registered.
3. Failure modes the policy actually has: timeout, retry, circuit breaker, cache,
   fallback.
4. `WithHooks` is ABSENT entirely.
5. No `OnCircuitOpen` → the breaker tripping (the catalog being treated as down)
   surfaces to no log/alert.
6. No `OnRetry` → retry bursts on a partial outage are not surfaced.
7. No `OnFallbackUsed` → the `nil` fallback (a missing/empty product served as
   authoritative) activating is invisible to alerting.
8. Metrics counters for breaker/retry/cache/fallback DO increment automatically —
   record; the gap is hooks→alerting, not "no metrics."
9. `WithReadinessImpact()` is ABSENT.
10. The dependency is an internal product-catalog service **shared by many callers**
    (topology line). Not gating readiness on a shared dep is the CORRECT choice —
    avoids the fleet-wide flip. Not a finding; the opposite would be.
11. Is the catalog a dependency whose loss should pull THIS pod? No — product pages
    "degrade acceptably" per the criticality line, so leaving readiness ungated is
    right.
12. No `r8eotel.Register` / `r8eotel.Trace`. The context does not state an OTel
    stack → at most NOTED.
13. Code-only patterns present: `WithCache` (cache + keyFn closure). When a
    `PolicyConfig` JSON is emitted, the cache pattern is silently absent — operators
    reading JSON would not see it.
14. `Reconfigure` could retune the breaker/timeout/retry it already has, but could
    not add a pattern — fine here, nothing is being added.
15. No `WithChaos` → no kill-switch concern.
16. Of the modes that matter (breaker open = catalog deemed down; fallback = serving
    a possibly-empty product authoritatively), NONE is alertable today — all rely on
    out-of-band metric scraping.

### Phase 2 — Adversarial scenarios
- **Silent breaker open.** Trigger: catalog 5xx crosses the failure threshold.
  Propagation: breaker opens; with no `OnCircuitOpen`, nothing logs/pages.
  Symptom: catalog reads fast-fail and no human is told the catalog is down.
  Detectability: only the automatic `circuit_open` counter, IF someone alerts on
  it out of band.
- **Unannounced fallback serving.** Trigger: catalog down + retries exhausted.
  Propagation: `WithFallback(nil)` returns a nil product; no `OnFallbackUsed`.
  Symptom: pages render degraded and ops has no signal of how often. Detectability:
  automatic fallback counter only.
- **Readiness correctly ungated.** Trigger: catalog blips. Propagation: readiness
  is NOT gated → pods stay Ready, traffic still served from cache/fallback.
  Symptom: graceful degradation, no fleet flip. (A scenario that does NOT fail —
  bounds the surface; the correct choice.)
- **Operator reads JSON, misses the cache.** Trigger: operator inspects an emitted
  `PolicyConfig`. Propagation: `WithCache` is code-only → absent from JSON.
  Symptom: the operator believes there is no caching layer. Detectability: none
  from JSON; only from the code.
- **Health is findable.** Trigger: incident triage. Propagation: named
  `"catalog-read"` → its breaker state is queryable. Symptom: operable. (Does not
  fail — the naming is correct.)

### Phase 3 — Scoring
- BLOCKING: none. Readiness is correctly NOT gated on a shared dep; the name is
  present; there is no chaos.
- IMPORTANT: #4–#7 + scenarios "silent breaker open" / "unannounced fallback" —
  NO `WithHooks` at all, so the breaker opening, the retries, and the fallback
  activations are not surfaced to logs/alerting. (The automatic metric counters
  still increment — NOTED — but nothing is wired to alert on them.) Accepted only
  if the team confirms it alerts on those automatic counters out of band.
- NOTED: #12 (no OTel; context doesn't mention it), #13 (cache is code-only,
  absent from emitted JSON — flag for operator expectations).

### Phase 4 — Verdict
No BLOCKING. One IMPORTANT cluster: the policy has real failure modes (breaker,
retry, fallback) and wires NO hooks, so those events are invisible to logging /
alerting. Readiness is correctly ungated and the policy is named. **CONDITIONAL
APPROVE**, accepted risk: wire `WithHooks{OnCircuitOpen, OnRetry, OnFallbackUsed}`
to logs/metrics, OR confirm in writing that alerting consumes the automatic
counters out of band; document that `WithCache` is code-only and absent from any
emitted JSON.

### Phase 5 — Meta-critique
1. Most likely leniency: accepting "metrics are automatic" as if it closes the gap
   — it does not; counters incrementing with no alert wired is still operationally
   blind, which is why this stays IMPORTANT, not NOTED.
2. Under-examined: whether the cache's negative/stale behavior needs its own hook
   — that drifts toward fallback-axis territory, so I left it NOTED.
3. To a REJECT reviewer: nothing here is a fleet-flip or an undisableable chaos
   fault; the readiness choice is correct and the policy is named — the only real
   gap is hooks→alerting, which is fixable without a redesign, hence CONDITIONAL,
   not REJECT.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. Policy name is `"recommend-read"` — non-empty, meaningful → auto-registered for
   health. Findable by an operator.
2. Built as a `NewPolicy` object, not an anonymous `r8e.Do` → registered.
3. Failure modes the policy actually has: timeout, retry, retry-budget, circuit
   breaker (failure + slow-call), bulkhead, fallback.
4. `WithHooks` IS present.
5. `OnCircuitOpen` wired → `metrics.Inc("recommend.circuit_open")` — the breaker it
   actually has is observable.
6. `OnRetry` wired → `log.Warn(...)` with attempt and err — retries surface to
   logs.
7. `OnFallbackUsed` wired → `metrics.Inc("recommend.fallback")` — fallback
   activations are alertable.
8. The three wired hooks match the three high-signal modes the policy has (breaker,
   retry, fallback). Coverage of the modes that matter is complete.
9. Metrics counters also increment automatically — the hooks ADD event-level
   logging/alerting on top.
10. `WithReadinessImpact()` is ABSENT — and a comment states it is deliberately NOT
    gated because the dependency is shared across the fleet (avoids the fleet-wide
    `/readyz` flip). The right call, and documented.
11. The topology line confirms the recommendations service is shared across the
    fleet → not gating readiness is correct.
12. Recommendations are best-effort (an empty list is an acceptable degraded UX) →
    the dependency's loss should NOT pull the pod; ungated readiness is consistent.
13. No `r8eotel.Register` / `r8eotel.Trace`. The context does not state an OTel
    stack → at most NOTED (could add `r8eotel.Trace` if spans are wanted later).
14. Code-only patterns: no `WithCoalesce`, no `WithCache`, no classifiers closures,
    no `WithFallbackFunc` (it uses a static `WithFallback[[]Item](nil)` value) — so
    the config-vs-code JSON gap is minimal here; the retunable knobs (timeout,
    retry, breaker, bulkhead) are all config-expressible.
15. No `WithChaos` → no kill-switch concern.
16. Of the modes that page someone (breaker open, fallback storm), BOTH are
    alertable via the wired hooks. Nothing high-signal is silent.

### Phase 2 — Adversarial scenarios
- **Breaker open is announced.** Trigger: recommender 5xx crosses threshold.
  Propagation: `OnCircuitOpen` fires → `recommend.circuit_open` counter. Symptom:
  alertable; ops knows recs are down. Does not fail.
- **Fallback storm is visible.** Trigger: downstream down, retries exhausted.
  Propagation: `OnFallbackUsed` fires → `recommend.fallback` counter. Symptom: the
  fallback rate is graphable/alertable. Does not fail.
- **Shared-dep blip does not flip the fleet.** Trigger: recommender blips.
  Propagation: readiness is deliberately ungated → pods stay Ready, page renders
  without the rail. Symptom: graceful degradation, no fleet-wide `/readyz` flip.
  Does not fail (the documented, correct choice).
- **Operator finds the policy.** Trigger: incident triage. Propagation: named
  `"recommend-read"` → its breaker/retry state is queryable by name. Symptom:
  operable. Does not fail.
- **Retry burst is logged.** Trigger: a partial outage drives retries.
  Propagation: `OnRetry` logs each attempt+err (budget-gated). Symptom: visible in
  logs; no silent amplification. Does not fail.
Fewer than 5 *failing* scenarios are findable: the policy is named (registered for
health), wires hooks to every high-signal mode it has, deliberately and correctly
leaves readiness ungated on a shared dependency, and carries no code-only pattern
that would silently vanish from config — its operability gaps are structurally
bounded to "could optionally add OTel tracing."

### Phase 3 — Scoring
- BLOCKING: none.
- IMPORTANT: none in this axis's lane.
- NOTED: #13 — no `r8eotel.Trace`; the context doesn't mention an OTel stack, so
  adding it is optional, not required.

### Phase 4 — Verdict
Phases 1–2 produced no operability observation, and every adversarial scenario is
dismissable in writing. The policy is named, hooked to the modes it has, and
correctly ungated on a shared dependency (with the rationale in a comment).
**APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the fixture is labelled "good." I
   re-derived operability from the artifact (named, three hooks matching three
   modes, ungated shared dep with stated rationale), not from the label.
2. Under-examined: whether a hook the policy lacks for a present mode (e.g. an
   `OnBulkheadRejected` for the bulkhead) is a gap — but a saturated bulkhead on a
   best-effort recs path is low-signal, and the high-signal modes (breaker,
   fallback) are covered, so it is at most NOTED, not a finding.
3. To a REJECT reviewer: name the operational blind spot. There is none that
   matters — the breaker, retries, and fallback are all surfaced, the policy is
   findable by name, and the readiness choice actively PREVENTS the fleet-flip
   outage rather than causing it.

FINAL VERDICT: APPROVE
