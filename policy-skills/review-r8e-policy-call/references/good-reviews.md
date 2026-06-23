# Good reviews — call / Do axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together.
These are the highest-leverage examples — match their register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. Operation class: **write** (charges a card, moves money) — stated.
2. `WithRetry(5, …)` wraps the write.
3. The call sends **no** `Idempotency-Key` (the gateway supports one).
4. `WithHedge(50ms)` wraps the same write.
5. The fn signature is `func(_ context.Context)` — the policy ctx is **discarded**.
6. `gateway.Charge(amount, card)` takes no ctx → cannot be cancelled.
7. On `WithTimeout` firing (none is even configured here), the charge would still
   commit downstream → orphaned side effect.
8. No resource-cleanup visible, but the charge's network effect is the concern,
   not a body.
9. Partial-failure: a 5xx after the gateway has captured but before it returns →
   retry re-captures.
10. Re-execution determinism: each attempt is a fresh charge — non-deterministic
    money movement.
11. Hedge goroutines both call `gateway.Charge` → two concurrent charges; no
    shared captured state, but the *external* effect is duplicated.
12. Error taxonomy: `402 card_declined` / `400 invalid_request` are permanent but
    nothing classifies them → retry burns 5 attempts on a declined card.
13. `429 Retry-After` is emitted but, without the httpx adapter shown, it is
    unclear it is honored (defer the honoring mechanics to retry axis).
14. Panic surface: not assessable; `WithRecover` absent.
15. The policy is also unbuildable — `WithCoalesce` without `WithTimeout` panics
    (noted; structural, cross-axis).
16. Empty coalesce key (`return ""`) opts every call out of coalescing anyway.

### Phase 2 — Adversarial scenarios
- **Double charge via retry.** Trigger: transient 5xx on a captured charge.
  Propagation: retry re-sends the charge with no idempotency key. Symptom:
  customer charged twice. Detectability: only via downstream reconciliation —
  invisible to the policy.
- **Double charge via hedge.** Trigger: primary > 50 ms (p50 is 220 ms, so the
  hedge fires on the **majority** of calls). Propagation: two concurrent charges.
  Symptom: systematic duplicate captures. Detectability: none in-policy.
- **Orphaned charge after caller cancels.** Trigger: caller's request ctx
  cancelled. Propagation: the fn ignores ctx, the charge commits. Symptom:
  customer charged for an abandoned checkout. Detectability: none.
- **Declined-card retry storm.** Trigger: `402 card_declined`. Propagation:
  unclassified → 5 retries. Symptom: 5× load on the gateway per decline, latency
  to the user. Detectability: gateway 402 rate.
- **Build-time panic.** Trigger: `NewPolicy` runs. Propagation: `WithCoalesce`
  without `WithTimeout` → `ErrCoalesceWithoutTimeout` panic. Symptom: the service
  cannot start. Detectability: immediate (but only at boot).

### Phase 3 — Scoring
- BLOCKING: #2+#3 (retried write, no key), #4 (hedged write), #5+#6+#7 (ctx
  ignored → orphaned commit), scenario "double charge via retry/hedge", "orphaned
  charge".
- IMPORTANT: #12 (permanent errors unclassified — burns load; remediation is the
  retry axis's but the *unsafety* originates in the call's taxonomy).
- NOTED: #13 (defer 429 honoring to retry), #15/#16 (structural/coalesce — cross
  axis).

### Phase 4 — Verdict
Multiple BLOCKING (every one a path to charging a customer twice or for an
abandoned request). **REJECT.** Remediation: this call must not be retried or
hedged without a server-honored idempotency key, and it must receive and respect
the policy ctx so cancellation actually stops the charge.

### Phase 5 — Meta-critique
1. Most likely leniency: treating "the hedge rarely fires" as mitigation — but
   p50 220 ms ≫ 50 ms delay, so it fires constantly; no leniency warranted.
2. Category I could have under-examined: whether the gateway *itself* dedups
   without a client key — but the context says the key must be sent, so no.
3. To a reviewer who reached APPROVE: a single transient blip double-charges a
   real customer with no in-policy detection — that is a financial incident, not a
   style nit. APPROVE is indefensible.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. Operation class: **read / idempotent** — stated; no side effects.
2. `WithRetry(3, …)` on an idempotent read → re-execution harmless.
3. No `WithHedge` → no duplicate-execution concern.
4. The fn is `func(ctx context.Context)` and passes ctx to `catalog.Get(ctx, id)`
   → cancellation propagates. Good.
5. Resource cleanup: `catalog.Get` is a typed client call; no raw body handling
   shown → not a leak surface here.
6. Return type `*Product`: does `catalog.Get` return `(nil, nil)` on `404`, or
   `(nil, errNotFound)`? **UNKNOWN** — the contract is ambiguous.
7. If `(nil, nil)` on not-found, the policy hands an authoritative `nil *Product`
   downstream; with `WithFallback(nil)` too, callers may nil-deref.
8. Re-execution determinism: a read may observe a price/stock change between
   attempts; acceptable for a catalog read.
9. Error taxonomy: `404` permanent vs `5xx` transient — not classified here (act
   on it: retry axis).
10. Partial-failure: none (read).
11. Panic surface: nil-deref *downstream* of a `(nil,nil)` return — depends on #6.
12. Nested guards: the client's own timeout vs `WithTimeout(2s)` unknown — cross
    axis (timeouts).
13. No shared captured state.
14. `ctx` honored end to end.
15. No idempotency-key concern (read).

### Phase 2 — Adversarial scenarios
- **Nil-deref on not-found.** Trigger: `404` returned as `(nil, nil)`.
  Propagation: policy returns `nil *Product`; caller `p.Name` panics. Symptom:
  request 500s. Detectability: panic logs — but only if #6 resolves to `(nil,nil)`.
- **Stale price under retry.** Trigger: price changes between attempt 1 and 2.
  Propagation: attempt 2 returns the new price. Symptom: minor inconsistency.
  Detectability: none; acceptable for a catalog.
- **Cancellation honored.** Trigger: caller cancels. Propagation: ctx threaded →
  `catalog.Get` returns ctx.Err. Symptom: clean abort. (A scenario that does NOT
  fail — bounds the failure surface.)
- **Permanent error retried.** Trigger: `404`. Propagation: unclassified → 3
  attempts. Symptom: 3× latency on a miss; defer the *fix* to retry axis, but the
  call's taxonomy enables it. Detectability: 404 rate.
- **No side-effect duplication.** Idempotent read → retry cannot double anything
  (bounds the surface).

### Phase 3 — Scoring
- BLOCKING: none. Retry on an idempotent ctx-honoring read is safe.
- IMPORTANT: #6/#7 — the `(nil, nil)` contract ambiguity. If the call can return
  a nil-without-error, the policy propagates an authoritative nil. Accepted only
  if confirmed `catalog.Get` always errors on not-found.
- NOTED: #9 (classification → retry axis), #12 (timeout sizing → timeouts axis),
  #8 (benign read staleness).

### Phase 4 — Verdict
No BLOCKING; one IMPORTANT resting on an UNKNOWN contract. **CONDITIONAL
APPROVE**, accepted risk: confirm `catalog.Get` returns an error (not `(nil,
nil)`) on not-found; if it can return a bare nil, this becomes BLOCKING (nil-deref
path) and the fallback/return contract must change.

### Phase 5 — Meta-critique
1. Most likely leniency: waving through the `(nil,nil)` ambiguity as "probably
   errors" — I refused to, and made it the accepted risk.
2. Under-examined: whether `catalog.Get` itself retries internally (nested) —
   noted as cross-axis.
3. To a REJECT reviewer: the call is an idempotent, ctx-honoring read — retry is
   genuinely safe; the only real hazard is the nil contract, which is a
   conditional, not a demonstrated failure until #6 resolves.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. Operation class: **read / idempotent**, no side effects — stated.
2. `WithRetry(3, …)` on an idempotent read → safe.
3. `WithRetryBudget` present → amplification handled (cross-axis; not a call
   safety issue).
4. No `WithHedge` → no duplicate concern (and would be safe anyway for a read).
5. The fn is `func(ctx context.Context)` passing ctx to `recommender.Get(ctx,
   userID)` → cancellation/deadline propagate end to end.
6. Error taxonomy: 4xx marked `r8e.Permanent`, 5xx/timeouts transient, 429 carries
   Retry-After honored automatically — classified at the boundary. The call's
   taxonomy is explicit.
7. Resource cleanup: typed client call; nothing raw to leak.
8. Re-execution determinism: a recommendations read is naturally idempotent;
   re-execution returns an equivalent list.
9. No shared captured state; no hedge goroutines.
10. Fallback returns `nil []Item`; the context documents empty recs as an
    acceptable degraded UX, and an empty slice is safe to range — no nil-deref.
11. Partial-failure: none (read).
12. Panic surface: none demonstrable; inputs are a userID.
13. Nested guards: `WithTimeout(300ms)` bounds the call; the client honors ctx, so
    the policy deadline bites (sizing → timeouts axis).
14. `429` handled without manual code (RetryAfterProvider).
15. The op is safe to run zero, one, or many times.

### Phase 2 — Adversarial scenarios
- **Cancellation.** Trigger: caller cancels. Propagation: ctx → `recommender.Get`
  aborts. Symptom: clean. Does not fail.
- **Retry of a transient 5xx.** Trigger: one 5xx. Propagation: idempotent
  re-execution, budget-gated. Symptom: a second harmless GET. Does not fail.
- **Permanent 4xx.** Trigger: `403`. Propagation: classified Permanent → no
  retry. Symptom: fast surface of a real error. Does not fail.
- **Empty fallback.** Trigger: downstream down + retries exhausted. Propagation:
  `nil []Item` returned; page renders without the rail (documented). Symptom:
  degraded but correct. Does not fail.
- **429 throttle.** Trigger: server 429 + Retry-After. Propagation: honored
  automatically. Symptom: backed-off retry. Does not fail.
Fewer than 5 *failing* scenarios are findable: the call is an idempotent,
ctx-honoring, classified read with a documented-safe degraded value — its
failure modes are structurally bounded to "returns an error or an empty list,"
both of which the caller tolerates.

### Phase 3 — Scoring
- BLOCKING: none.
- IMPORTANT: none in this axis's lane.
- NOTED: timeout/retry/overload *sizing* are out of lane (defer); the call itself
  is clean.

### Phase 4 — Verdict
Phases 1–2 produced no call-safety observation, and every adversarial scenario is
dismissable in writing. **APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the context says "good." I
   re-derived safety from the call's properties (idempotent, ctx-honored,
   classified), not from the label.
2. Under-examined: whether `recommender.Get` has a hidden side effect (logging a
   recommendation impression as a billable event). The context says no side
   effects; if that were false, the read would become a write and retry/budget
   would need re-evaluation — I would reopen on new evidence.
3. To a REJECT reviewer: name the unsafe execution. There is none — re-running an
   idempotent ctx-bounded read changes nothing observable.

FINAL VERDICT: APPROVE
