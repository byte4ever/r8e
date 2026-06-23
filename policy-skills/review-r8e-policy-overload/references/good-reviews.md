# Good reviews — overload / shedding axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together —
a guard's sizing (or absence) is only judgeable against the downstream's capacity,
sharing, and latency. These are the highest-leverage examples — match their
register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. Topology: a SINGLE shared third-party gateway hit by every replica — the exact
   shape where amplification metastabilizes.
2. `WithCircuitBreaker`: **absent**. Nothing fast-fails a sick gateway.
3. `WithRetry(5, ConstantBackoff(200ms))`: every failure re-sends up to 5×.
4. `WithHedge(50ms)`: p50 is 220 ms, so the hedge fires on the **majority** of
   calls — a second concurrent request per call even at baseline.
5. No `WithRetryBudget` / shared budget → retry amplification is uncapped.
6. No `WithAdaptiveThrottle`, no `WithSLO` → no client-side shedding of any kind.
7. `SlowCallRate`: absent (and moot without a breaker) — a brownout (p99 900 ms
   climbing) would be invisible to any failure-only trip too.
8. `WithBulkhead`: **absent** → no cap on concurrent in-flight charges.
9. `WithAdaptiveConcurrency`: absent → no adaptive cap either; in-flight work is
   unbounded.
10. Caller-pool exhaustion path: gateway slows → retries + hedges pile up
    in-flight with no cap → the caller's connection/goroutine pool drains.
11. `RecoveryTimeout` / `HalfOpenMaxAttempts` / `RampRecovery`: N/A — no breaker
    to size.
12. `WithRateLimit`: absent; the gateway emits `429 Retry-After` (a hard
    backpressure signal) and the policy has no rate guard to react with.
13. Sheddability tiers: none stamped; nothing here can be shed by criticality.
14. Amplification factor under brownout: ≈ 5 retries × (1 + hedge) per call across
    every replica simultaneously — load multiplies precisely when the gateway is
    least able to serve it.
15. Fast-fail path on sustained overload: NONE. Once the gateway browns out, the
    policy keeps feeding it; the gateway cannot drain → it does not recover.
16. Note (out of lane): `WithReadinessImpact()` on a shared dep is a fleet-wide
    readiness-flip risk — defer scoring to observability; the metastable
    interaction is mine to flag.

### Phase 2 — Adversarial scenarios
- **Metastable collapse on a brownout.** Trigger: gateway p99 rises (capacity dip,
  GC, DB contention). Propagation: every call now exceeds 50 ms → hedge fires;
  slow/failed calls retry ×5; no breaker, no throttle, no budget → offered load
  multiplies across the whole fleet. Symptom: the gateway saturates and stays
  saturated long after the original trigger clears — unrecoverable without an
  operator dropping traffic. Detectability: gateway saturation + climbing client
  latency; the policy itself reports nothing fast-failing.
- **Caller pool exhaustion.** Trigger: gateway slows to seconds. Propagation: no
  bulkhead → in-flight charges (each with a hedge twin) accumulate unbounded.
  Symptom: the caller's connection pool / goroutines exhaust; unrelated traffic on
  the same process stalls. Detectability: caller goroutine count / pool wait time.
- **Hedge doubles baseline load permanently.** Trigger: steady state (p50 220 ms ≫
  50 ms delay). Propagation: a hedge fires on most calls even when healthy.
  Symptom: ≈2× steady offered load to a shared dep for no latency win on an
  already-fast tail. Detectability: gateway request rate ≈ 2× call rate.
- **429 ignored as backpressure.** Trigger: gateway returns `429 Retry-After`.
  Propagation: no `WithRateLimit` to clamp client rate; the policy keeps offering
  full load. Symptom: the client fights the gateway's explicit throttle. (Honoring
  the per-error `Retry-After` is the retry axis's; having NO rate guard to react to
  sustained 429 is mine.) Detectability: persistent 429 rate.
- **No shedding tier for a non-critical retry.** Trigger: overload. Propagation:
  nothing is sheddable; all traffic competes equally. Symptom: cannot shed
  low-value load to protect checkout. Detectability: none in-policy.

### Phase 3 — Scoring
- BLOCKING: #2+#3+#4+#6 together (shared dep, retried ×5 AND hedged, NO breaker,
  NO shedding → metastable amplification), scenario "metastable collapse";
  #8+#9+#10 (no concurrency cap → caller pool exhaustion), scenario "caller pool
  exhaustion".
- IMPORTANT: #4 (hedge fires on the majority → ~2× steady load on a shared dep
  with no cap to contain it); #12 (no rate guard against sustained 429
  backpressure).
- NOTED: #13 (no sheddability tiers — single-tier traffic); #16
  (readiness-flip → observability's to score).

### Phase 4 — Verdict
Multiple BLOCKING. The defining one: a single shared gateway is retried ×5 and
hedged with NOTHING to fast-fail — a textbook metastable trigger; a transient
brownout becomes a self-sustaining fleet-wide collapse, and with no bulkhead the
caller's own pool drains alongside it. **REJECT.** Remediation: add
`WithCircuitBreaker` (with `SlowCallRate` for the brownout) and/or client-side
shedding so sustained overload fast-fails; cap in-flight with a bulkhead sized to
what the gateway can serve; do not hedge a write at all (call axis), and gate
retries with a budget (retry axis).

### Phase 5 — Meta-critique
1. Most likely leniency: treating the retry/hedge SAFETY of the write as my
   finding — that is the call axis. I scored only the OVERLOAD consequence (no
   fast-fail under amplification), which is squarely mine.
2. Category I could have under-examined: whether the gateway has its OWN
   server-side admission control that would shed for us — but the context gives no
   such guarantee, and a shared third-party we cannot configure is exactly why
   client-side shedding is required.
3. To a reviewer who reached APPROVE: name the control that fast-fails this gateway
   during a brownout. There is none — retry and hedge both ADD load, and the only
   thing that ends a metastable collapse is dropping load, which nothing here does.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. Topology: internal catalog service SHARED by many callers — overload controls
   matter.
2. `WithCircuitBreaker()` is **present** — a fast-fail path exists for a hard-down
   catalog. Good.
3. `SlowCallRate`: **absent**. The breaker trips on failure count only.
4. Catalog's common failure mode is DB contention → a BROWNOUT (slow-but-200), the
   exact mode a failure-only trip misses.
5. `FailureThreshold`: default (5) — plausible for this service but unverified
   against its traffic and error noise.
6. `RecoveryTimeout`: default (30s) — plausible but not traced to the catalog's
   real recovery time.
7. `HalfOpenMaxAttempts` / `RampRecovery`: defaults / absent — re-entry on close
   is not slow-started, but an internal catalog likely tolerates a full return.
8. `WithBulkhead`: **absent** → no cap on concurrent in-flight reads to a shared
   dep.
9. `WithAdaptiveConcurrency`: absent → no adaptive cap either.
10. Caller-pool window: a slow catalog (brownout) → in-flight reads pile up with no
    cap → caller goroutines / pool can exhaust. Mitigated somewhat by `WithTimeout
    (2s)` bounding each attempt, but 2 s of pile-up under burst is still a window.
11. No bulkhead + adaptive-concurrency conflict (only zero of the two present) → no
    build panic. Buildable.
12. `WithRetry(3, …)` present on a shared dep but with no `WithRetryBudget` →
    amplification is capped only by attempt count, not aggregate; the *budget* is
    the retry axis's, but the breaker is the structural backstop here and it can be
    blind to the brownout (see #3/#4).
13. `WithRateLimit`: absent; no hard external quota stated (internal service) → not
    required.
14. Sheddability tiers: none; single-tier read — acceptable.
15. `WithCache` is present (short-circuits hits) — reduces offered load on a hot
    key, easing overload, though cache *correctness* is the fallback axis's.

### Phase 2 — Adversarial scenarios
- **Brownout never trips the breaker.** Trigger: catalog DB contention → p99 climbs
  to seconds but still returns 200. Propagation: no `SlowCallRate` → the
  failure-count breaker never opens; every read waits up to `WithTimeout(2s)`.
  Symptom: latency balloons fleet-wide and the breaker that should have shed the
  brownout stays closed. Detectability: catalog p99 vs a flat circuit-open rate.
- **Caller pile-up under a slow catalog.** Trigger: the same brownout, plus a
  request burst. Propagation: no bulkhead → in-flight reads accumulate (each up to
  2 s) with no cap. Symptom: caller goroutine / pool growth; risk of exhausting the
  caller. Detectability: caller in-flight count vs pool size.
- **Retry on a shared dep, breaker blind.** Trigger: transient 5xx wave.
  Propagation: 3× retries per call; the breaker would normally cap this, but if the
  wave presents as slowness (not errors) it stays closed (#3). Symptom: 3× offered
  load during a partial outage. Detectability: catalog request rate vs call rate.
- **Hard-down catalog handled.** Trigger: catalog returns 5xx outright.
  Propagation: failure count crosses 5 → breaker opens → fast-fail. Symptom: clean
  shed. (A scenario that does NOT fail — the failure-trip path works.)
- **Recovery re-entry.** Trigger: catalog recovers after 30 s. Propagation: breaker
  closes, full traffic returns at once (no ramp). Symptom: a brief re-entry spike;
  an internal catalog likely absorbs it. Detectability: a small request-rate step
  on close. (Bounded — not scored above NOTED.)

### Phase 3 — Scoring
- BLOCKING: none. A breaker is present (a fast-fail path exists), the policy is
  buildable (no concurrency-limiter conflict), and there is no retry+hedge
  amplification on a shared dep with nothing to fast-fail.
- IMPORTANT: #3+#4 (no `SlowCallRate` → the breaker is blind to the catalog's most
  common failure, a brownout). #8+#9+#10 (no bulkhead on a shared dep that can slow
  → caller pile-up window, only partly bounded by the 2 s timeout).
- NOTED: #5/#6 (default `FailureThreshold(5)` / `RecoveryTimeout(30s)` plausible
  but unverified against traffic / recovery time). #7 (no `RampRecovery` — bounded
  on an internal dep). #12 retry amplification → defer budget to retry axis.

### Phase 4 — Verdict
No BLOCKING; two IMPORTANT, both real but accept-able with explicit risk.
**CONDITIONAL APPROVE**, accepted risks: (1) add `SlowCallRate(d, rate)` so the
breaker trips on the catalog's brownout, or confirm in writing the catalog fails
hard (errors, not slowness); (2) size a `WithBulkhead` to the catalog's serveable
capacity, or document why unbounded in-flight is safe given the 2 s ceiling. The
default thresholds are an unverified-but-plausible starting point (NOTED).

### Phase 5 — Meta-critique
1. Most likely leniency: crediting "a breaker is present" as sufficient — it is
   not, because it is blind to the brownout that is this catalog's defining failure
   mode. I escalated that to IMPORTANT rather than waving it through.
2. Under-examined: whether the 2 s `WithTimeout` alone bounds the pile-up enough to
   drop the bulkhead finding — but 2 s × burst is still a real pool-exhaustion
   window, so I kept it IMPORTANT; the *timeout sizing* itself is the timeouts
   axis's.
3. To a REJECT reviewer: there is no amplification trap here — no hedge, retry is
   attempt-capped, the policy builds, and a hard-down catalog DOES fast-fail. The
   gaps are a brownout-blind breaker and a missing cap, both fixable tunings, not a
   metastable structural collapse.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. Topology: internal recommendations service SHARED across the fleet, HTTP pool
   capped at 32 connections — a stated capacity to size against.
2. `WithCircuitBreaker(...)` present on the shared dep → fast-fail path exists.
3. `FailureThreshold(5)`: a stated trip count (not the bare default-by-omission);
   reasonable for this service.
4. `RecoveryTimeout(10s)`: a deliberate recovery window, not a guess.
5. `SlowCallRate(150ms, 0.5)`: present → the breaker trips on a BROWNOUT (slow
   majority above 150 ms) too, not only on outright errors. This closes the
   mediocre fixture's gap.
6. `WithBulkhead(32)`: sized to the STATED 32-connection pool — caps in-flight to
   exactly what the downstream can serve; not guessed.
7. `WithAdaptiveConcurrency`: absent — correctly, since capacity is KNOWN (32);
   using both would panic `ErrConcurrencyLimiterConflict`. No conflict here.
8. No bulkhead + adaptive-concurrency clash → buildable.
9. `WithRetry(3, ExponentialJitterBackoff, MaxDelay(150ms))` is gated by
   `WithRetryBudget(MaxTokens(10), TokenRatio(0.1))` → a partial outage cannot
   amplify aggregate load; no metastable trap. (Budget *tuning* is the retry axis's;
   its mere presence removes the amplification hazard I score.)
10. No `WithHedge` → no duplicate-execution load added; nothing to amplify a
    brownout.
11. `WithRateLimit`: absent; no hard external quota (internal dep) → not required.
12. `WithAdaptiveThrottle` / `WithSLO`: absent — the breaker (with `SlowCallRate`)
    already provides the fast-fail; client-side burn-rate shedding is optional here,
    not required for a best-effort read.
13. `RampRecovery`: absent — a NOTED nice-to-have (ease re-entry on close), but the
    downstream tolerates a full return and recovery is already 10 s.
14. Sheddability tiers: none stamped, but this is single-tier best-effort traffic —
    nothing to protect from being shed first.
15. Fast-fail path under sustained overload: breaker (failure AND brownout) +
    bulkhead reject (`ErrBulkheadFull` past 32 in-flight) + budget-gated retry —
    overload sheds rather than amplifies. The metastable trigger is absent.

### Phase 2 — Adversarial scenarios
- **Brownout.** Trigger: recommender p99 climbs above 150 ms on a majority.
  Propagation: `SlowCallRate(150ms, 0.5)` trips the breaker → fast-fail to the
  documented empty fallback. Symptom: clean shed, page renders without the rail.
  Does NOT fail.
- **Hard-down.** Trigger: 5xx wave. Propagation: failure count crosses 5 → breaker
  opens. Symptom: fast-fail, no pile-up. Does NOT fail.
- **Concurrency burst.** Trigger: a spike of concurrent calls. Propagation:
  `WithBulkhead(32)` caps in-flight at the pool size; the 33rd fast-rejects
  (`ErrBulkheadFull`). Symptom: the caller's pool is never exceeded; excess sheds.
  Does NOT fail.
- **Partial outage + retries.** Trigger: intermittent 5xx. Propagation: retries are
  budget-gated (10 tokens, 0.1 ratio) → amplification is capped; the breaker backs
  it. Symptom: bounded extra load, no storm. Does NOT fail.
- **Recovery re-entry.** Trigger: recommender heals after 10 s. Propagation: breaker
  closes, traffic returns; no ramp, but capacity is known (32) and the bulkhead
  still caps in-flight. Symptom: a brief, bounded re-entry. Does NOT fail.
Fewer than 5 *failing* scenarios are findable: the policy has a breaker that trips
on failures AND brownouts, a bulkhead sized to the stated pool, budget-gated retry,
and no hedge — every overload path either fast-fails or is capped to known
capacity. Its overload behaviour is structurally bounded.

### Phase 3 — Scoring
- BLOCKING: none.
- IMPORTANT: none in this axis's lane.
- NOTED: #13 (`RampRecovery` could ease re-entry, but not required — recovery is
  10 s and capacity is known). #9/#12 (retry budget / throttle tuning are out of
  lane — present and sufficient; defer any tuning to the retry axis).

### Phase 4 — Verdict
Phases 1–2 produced no overload observation above NOTED, and every adversarial
scenario is dismissable in writing: the breaker fast-fails on failures and
brownouts, the bulkhead is sized to the stated 32-connection pool (not guessed),
retry is budget-gated, and there is no hedge to amplify. **APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the context says "good." I
   re-derived the verdict from the controls — `SlowCallRate` present, bulkhead =
   stated pool, budget-gated retry, no hedge — not from the label.
2. Under-examined: whether the stated 32-connection pool is the TRUE downstream
   bottleneck (the service behind it could cap lower) — but the context names 32 as
   the pool, and sizing to a stated capacity is exactly the right move; a tighter
   real limit would be a new fact to reopen on, not a present finding.
3. To a REJECT reviewer: name the overload path that is not handled. A brownout
   trips it, a hard-down trips it, a burst is capped at 32 and sheds, retries are
   budget-gated, and there is no hedge — there is no metastable trigger and no
   uncapped pile-up. RampRecovery is a refinement, not a blocking gap.

FINAL VERDICT: APPROVE
