# Good reviews — fallback / cache axis (few-shot calibration)

Three complete worked reviews at the calibrated register: a REJECT, a CONDITIONAL
APPROVE, and an APPROVE. Each runs phases 1–5 in order and ends with a single
`FINAL VERDICT:` line. The artifact is the service context + the policy together.
These are the highest-leverage examples — match their register and severity.

---

## Review A — `../../fixtures/poor.md` (payment charge) → REJECT

### Phase 1 — Enumeration
1. Fallback present: `WithFallback(ChargeResult{Status: "ok"})` — a static value.
2. The call is a **non-idempotent write** (charges a card) — stated.
3. The fallback fabricates a SUCCESSFUL charge result for a charge that FAILED:
   `Status: "ok"` is returned whenever the chain yields an error (downstream down,
   shed, retries exhausted).
4. The context is explicit: "A failed charge MUST surface to the caller — a silent
   'success' is a financial incident." The fallback directly violates this.
5. Static fallback value safety: `ChargeResult{Status:"ok"}` is not a nil-deref
   hazard, but its *semantic* content is the hazard — it asserts payment success.
6. No `WithFallbackFunc` → no sentinel discrimination; the static value fires on
   EVERY error, including a permanent `402 card_declined` (the customer's card was
   declined, yet the caller is told "ok").
7. Cache present? No `WithCache` → no ttl / staleness / negative-cache surface.
8. Coalesce present: `WithCoalesce(func(ctx) string { return "" })`.
9. Coalesce key is the EMPTY string for every call → every call opts OUT of
   coalescing (the coalescer does nothing).
10. Worse hypothetical: were the key non-empty-but-constant, distinct payments
    would collapse onto one in-flight charge → one customer's charge result
    returned to another. The empty key avoids that only by disabling coalescing.
11. A correct coalesce key here is in any case dubious — distinct charges are NOT
    equivalent requests; coalescing payments is a correctness trap by nature.
12. Coalesce requires `WithTimeout` — none is configured → build-time panic
    `ErrCoalesceWithoutTimeout` (cross-axis: -timeouts owns the panic; noted here).
13. Never-cache-a-fallback: no cache, so the `Status:"ok"` value cannot poison a
    cache — but it is returned to the caller directly, which is the whole problem.
14. ttl vs change interval: N/A (no cache).
15. StaleIfError / NegativeCache / RefreshAhead: none configured.
16. Miss stampede: no cache key concern; the duplicate-execution hazard
    (retry/hedge of the charge) is the call axis's lane, not ours.

### Phase 2 — Adversarial scenarios
- **Fabricated payment success.** Trigger: gateway 5xx (or breaker open) on a
  charge. Propagation: chain errors → `WithFallback` substitutes
  `ChargeResult{Status:"ok"}`. Symptom: the caller marks the order paid and ships
  goods with no payment captured. Detectability: only downstream reconciliation —
  invisible to the policy.
- **Declined card reported as paid.** Trigger: `402 card_declined` (permanent).
  Propagation: the error reaches the fallback → `Status:"ok"` returned. Symptom: a
  customer whose card was declined is treated as having paid. Detectability: none
  in-policy.
- **Shed reported as paid.** Trigger: the call is shed (rate-limited / bulkhead
  full / SLO). Propagation: a shed sentinel reaches the static fallback (no
  discrimination) → `Status:"ok"`. Symptom: a charge that never even ran is
  reported successful. Detectability: none.
- **Coalescing is inert.** Trigger: any two concurrent charges. Propagation: the
  empty key opts both out → no collapse. Symptom: the coalescer adds nothing (a
  pointless option), not a wrong result. Detectability: trivial (it never fires).
- **Build-time panic on boot.** Trigger: `NewPolicy` runs with `WithCoalesce` and
  no `WithTimeout`. Propagation: `ErrCoalesceWithoutTimeout` panic. Symptom: the
  service cannot start. Detectability: immediate at boot (cross-axis; -timeouts
  owns the fix).

### Phase 3 — Scoring
- BLOCKING: #3+#4 (fabricated write success — the static `Status:"ok"` returned for
  a failed/declined/shed charge), scenarios "fabricated payment success",
  "declined card reported as paid", "shed reported as paid". This is the red line:
  a fallback fabricating success for a failed non-idempotent write.
- IMPORTANT: #6 (no sentinel discrimination — but moot once the value itself is
  rejected; the whole fallback must go).
- NOTED: #9/#10/#11 (empty coalesce key is inert; a non-empty key would be a
  correctness trap), #12 (coalesce-without-timeout panic — cross-axis, -timeouts).

### Phase 4 — Verdict
A static fallback that asserts payment success for a charge that failed, was
declined, or was shed — exactly the silent financial incident the context forbids.
**REJECT.** Remediation: remove `WithFallback` entirely; a failed charge must
surface its error to the caller. Remove `WithCoalesce` (distinct payments are not
equivalent requests; the empty key proves it is doing nothing anyway).

### Phase 5 — Meta-critique
1. Most likely leniency: treating the fabricated "ok" as a mere observability gap
   ("just log it") — but the value is *returned to the caller as truth*, so it is a
   correctness/financial defect, not a logging nit.
2. Category I could have under-examined: whether some upstream layer re-validates
   the charge before shipping — but the context says the failed charge must surface
   *to this caller*, so I cannot assume a downstream safety net.
3. To a reviewer who reached APPROVE: name the value returned when the gateway is
   down — `Status:"ok"` for a charge that never captured money. That is a shipped
   order with no payment. APPROVE is indefensible.

FINAL VERDICT: REJECT

---

## Review B — `../../fixtures/mediocre.md` (catalog read) → CONDITIONAL APPROVE

### Phase 1 — Enumeration
1. Fallback present: `WithFallback[*Product](nil)` — a static nil pointer.
2. Static fallback value safety: the value is a `nil *Product`. A caller that
   derefs the degraded result (`p.Name`, `p.Price`) nil-panics.
3. The context: "showing a wrong/empty product as if authoritative is a UX defect"
   — a nil product is the empty case; whether the caller nil-checks before deref is
   **UNKNOWN** from the policy.
4. No `WithFallbackFunc` → no sentinel discrimination; the nil fires on every error
   (acceptable for a read IF the caller nil-checks; the hazard is the deref).
5. Fallback fabricates write success? No — this is an idempotent read; a nil
   "no product" is not a fabricated success. The write red line does not apply.
6. Cache present: `WithCache[*Product](cache, keyFn, 1*time.Hour)`.
7. keyFn collision risk: `keyFn` is not shown in the policy; the key contents are
   **UNKNOWN**. If it keys on the product id alone and the catalog is multi-tenant
   or locale-varying, distinct requests could collide. Flag the unknown.
8. Empty/constant key: not visibly empty (a `keyFn` is passed), but its contents
   are unverified — note as a thing to confirm.
9. ttl vs change interval: ttl = **1 hour**; the context states catalog data
   "changes a few times per hour (price/stock updates)." The ttl is far longer than
   the change interval → stale prices/stock served as authoritative for up to an
   hour.
10. The context calls a wrong/empty product shown as authoritative a UX defect; a
    stale price served from a 1h cache is precisely that.
11. `StaleIfError(d)`: not configured → on a downstream failure the chain falls to
    the nil fallback rather than serving a slightly-stale cached product. (A
    stale-if-error window might be preferable, but its absence is not unsafe.)
12. `NegativeCache(d)`: not configured → a 404 is not negatively cached (fine).
13. `RefreshAhead(d)`: not configured → no detached reload; no `WithTimeout`
    companion needed for it. (`WithTimeout(2s)` is present anyway.)
14. Coalesce present? No `WithCoalesce`. A hot product id on a miss lets N
    concurrent callers stampede the catalog at once.
15. Never-cache-a-fallback: r8e caches only genuine successes, so the nil fallback
    never enters the cache — the reliance (if any) is correct; no poisoned entry.

### Phase 2 — Adversarial scenarios
- **Nil-deref on the degraded path.** Trigger: catalog down + chain exhausted.
  Propagation: `WithFallback[*Product](nil)` returns `nil`; the caller derefs
  `p.Name`. Symptom: request 500s. Detectability: panic logs — but only if the
  caller does not nil-check (UNKNOWN).
- **Stale price served as authoritative.** Trigger: a price update lands 5 minutes
  after an entry is cached. Propagation: the 1h ttl keeps the old price for ~55
  more minutes. Symptom: customers shown (and possibly charged at) a wrong price —
  the documented UX defect. Detectability: low; looks like a valid hit.
- **Cross-request collision (conditional).** Trigger: keyFn drops a request-identity
  field (tenant/locale) — UNKNOWN. Propagation: caller B reads caller A's cached
  product. Symptom: wrong product/price. Detectability: near zero. Conditional on
  the unverified keyFn.
- **Miss stampede on a hot id.** Trigger: a popular product expires; 50 concurrent
  requests miss together. Propagation: no coalesce → 50 origin calls at once.
  Symptom: a latency/load spike on the catalog at each expiry. Detectability: a
  load spike correlated with ttl boundaries.
- **Negative result not cached.** Trigger: a stable 404. Propagation: no
  `NegativeCache` → every request re-asks the origin. Symptom: repeated misses on a
  known-absent id (minor). Detectability: 404 rate. (Bounds the surface — not
  unsafe.)

### Phase 3 — Scoring
- BLOCKING: none. The nil fallback is not a *write* fabrication, and the keyFn
  collision is conditional on an UNKNOWN, not demonstrated.
- IMPORTANT: #9/#10 (1h ttl on data that changes a few times per hour → stale
  authoritative prices, the documented UX defect); #2/#3 (nil `*Product` fallback
  the caller may deref — unsafe value for a pointer type unless the caller
  nil-checks).
- NOTED: #7/#8 (verify keyFn includes full request identity — conditional
  collision), #14 (no coalesce → miss stampede on a hot id), #11 (no
  `StaleIfError` — a missed degradation, not unsafe), #12 (no negative cache).

### Phase 4 — Verdict
No BLOCKING; two IMPORTANT (stale-authoritative ttl, nil-deref fallback value) and
the conditional keyFn risk. **CONDITIONAL APPROVE.** Accepted risks, to be
resolved in writing: (1) shorten the ttl well under the catalog change interval (or
document the staleness window as tolerable); (2) confirm the caller nil-checks the
degraded `*Product` (else return a safe representation — if a bare nil can be
deref'd this becomes BLOCKING); (3) confirm keyFn includes every field of request
identity (tenant/locale) — if it collides, BLOCKING.

### Phase 5 — Meta-critique
1. Most likely leniency: waving the 1h ttl through as "caches are supposed to be
   stale" — but the context names stale-authoritative data a UX defect and gives a
   sub-hour change interval, so the ttl is mis-sized against a stated requirement.
2. Under-examined: the keyFn contents (not shown). I refused to assume it is
   correct and made the collision a conditional accepted risk rather than a pass.
3. To a REJECT reviewer: the nil fallback is a *read*'s empty case, not a fabricated
   write success, and the collision is unverified, not demonstrated — neither clears
   the BLOCKING bar on the evidence shown; they are conditionals to resolve.

FINAL VERDICT: CONDITIONAL APPROVE

---

## Review C — `../../fixtures/good.md` (recommendations read) → APPROVE

### Phase 1 — Enumeration
1. Fallback present: `WithFallback[[]Item](nil)` — a static nil slice.
2. Static fallback value safety: a `nil []Item` is SAFE to range — `for _, x :=
   range recs` over nil iterates zero times. No nil-deref (slices are not deref'd
   like pointers).
3. The context documents an EMPTY recommendations list as "an explicitly acceptable
   degraded experience … the page renders without the rail." The degraded value is
   semantically correct and documented.
4. Fallback fabricates write success? No — this is an idempotent read; an empty
   list asserts nothing false. The write red line does not apply.
5. No `WithFallbackFunc` → the static empty slice fires on every error; that is
   correct here because empty recs is acceptable for ANY failure (shed or
   downstream), per the context.
6. Cache present? No `WithCache` in this policy → no keyFn, ttl, staleness, or
   negative-cache surface to mis-configure.
7. keyFn collision risk: N/A (no cache, no coalesce keyFn).
8. Empty/constant key: N/A.
9. ttl vs change interval: N/A (no cache).
10. `StaleIfError` / `NegativeCache` / `RefreshAhead`: none — N/A without a cache.
11. Coalesce present? No `WithCoalesce` → no coalesce-key correctness or
    `WithTimeout` companion concern.
12. Coalesce/refresh-ahead `WithTimeout` companions: not applicable (neither
    option present). `WithTimeout(300ms)` exists anyway.
13. Never-cache-a-fallback: with no cache, the nil-slice fallback cannot poison
    anything; the invariant holds trivially.
14. Miss stampede: no cache here, so no per-key miss-stampede surface in THIS axis.
    (Aggregate retry pressure is handled by `WithRetryBudget` — a retry-axis
    concern, not ours.)
15. The degraded value is safe to consume zero, one, or many times — ranging a nil
    slice is idempotent and harmless.

### Phase 2 — Adversarial scenarios
- **Empty fallback consumed.** Trigger: downstream down + retries exhausted.
  Propagation: `nil []Item` returned; the caller ranges it → zero items → the page
  renders without the rail. Symptom: degraded but correct UX (documented). Does NOT
  fail.
- **Fallback on a shed.** Trigger: breaker open (`ErrCircuitOpen`). Propagation:
  the static empty slice fires; empty recs is acceptable for ANY failure per the
  context. Symptom: rail omitted. Does NOT fail (no discrimination needed when the
  degraded value suits every error).
- **No cache to mis-key.** Trigger: any request. Propagation: there is no
  `WithCache`/`WithCoalesce`, so there is no key to collide and no entry to go
  stale. Symptom: every result is freshly computed and correct. Does NOT fail
  (bounds the surface).
- **No write to fabricate.** Trigger: any failure. Propagation: an idempotent read
  returning an empty list asserts no false success. Symptom: none. Does NOT fail.
- **Fallback never cached.** Trigger: a failure populates the fallback. Propagation:
  r8e caches only genuine successes, and there is no cache anyway, so no empty list
  is ever persisted as a hit. Symptom: the next success recomputes fresh. Does NOT
  fail.
Fewer than 5 *failing* scenarios are findable: with no cache/coalesce key to
mis-configure and a documented-safe nil-slice degraded value on an idempotent read,
the fallback/cache surface is structurally bounded to "returns an empty list,"
which the context explicitly accepts.

### Phase 3 — Scoring
- BLOCKING: none.
- IMPORTANT: none in this axis's lane.
- NOTED: the value relies on r8e never caching the fallback (confirmed correct; no
  cache present anyway); no cache key correctness issue exists because there is no
  cache.

### Phase 4 — Verdict
Phases 1–2 produced no fallback/cache observation, and every adversarial scenario
is dismissable in writing. The nil-slice fallback is safe to range, semantically
correct, and documented; there is no cache/coalesce key to mis-configure.
**APPROVE.**

### Phase 5 — Meta-critique
1. Most likely leniency: rubber-stamping because the context says "good." I
   re-derived safety from the value's properties (a nil slice is safe to range, not
   deref'd like a pointer) and the documented acceptance of empty recs, not from the
   label.
2. Under-examined: whether the caller ever JSON-marshals the result expecting
   `[]` and gets `null` from a nil slice (an API-shape nit) — the context frames the
   consumer as a page that omits a rail on empty, so this does not produce a wrong
   result; if a downstream contract required `[]` not `null` I would reopen.
3. To a REJECT reviewer: name the wrong or unsafe value returned. There is none —
   an empty recommendations list is the documented degraded result, a nil slice is
   safe to range, and no cache key exists to collide.

FINAL VERDICT: APPROVE
