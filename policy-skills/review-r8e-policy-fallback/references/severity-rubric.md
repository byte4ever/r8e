# Severity rubric — fallback / cache axis

Calibrated examples. Severity rests on a **demonstrated failure mode**, never on
aesthetic preference or a best-practice cited without a concrete case. When in
doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **Fabricated write success.** `WithFallback(ChargeResult{Status:"ok"})` on a
   non-idempotent charge. Propagation: the charge fails (downstream down / shed) →
   the policy returns a "success" → the caller marks the order paid and ships.
   Symptom: goods shipped with no payment; a silent financial incident.
   Detectability: only via downstream reconciliation — invisible to the policy.
   Remediation: no fallback on a write whose failure must surface; let the error
   propagate.
2. **Colliding cache key.** keyFn drops the tenant id and keys only on the product
   id. Propagation: tenant A's miss populates the entry, tenant B's hit reads
   tenant A's (price/availability) data. Symptom: cross-tenant data leak / wrong
   prices served as authoritative. Detectability: near zero — looks like a valid
   hit. Remediation: include every field of request identity in the key.
3. **Constant non-empty coalesce/cache key.** keyFn returns a fixed string for all
   callers. Propagation: every distinct request collapses onto one entry/in-flight
   call → caller B receives caller A's result. Symptom: universal cross-request
   contamination. Detectability: near zero. Remediation: derive the key from
   request identity.
4. **FallbackFunc masks a genuine error.** A `WithFallbackFunc` returns a cached
   default on EVERY error, including a `400 invalid_request` the caller must act
   on. Propagation: a real validation/auth failure is hidden behind a stale-looking
   "ok". Symptom: the caller never learns the request was malformed. Detectability:
   low. Remediation: discriminate the shed sentinels
   (`errors.Is(err, ErrCircuitOpen/ErrRateLimited/…)`) from genuine errors and
   only degrade on the former.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **ttl far longer than the change interval.** `WithCache(…, 1*time.Hour)` on
   catalog data that changes a few times per hour. Propagation: a price/stock
   change is not reflected for up to an hour → stale price shown as authoritative.
   Acceptable only if the context documents the staleness window as tolerable;
   otherwise shorten the ttl well under the change interval.
2. **Nil-pointer fallback value.** `WithFallback[*Product](nil)` returns a nil
   `*Product` the caller may deref. Propagation: downstream `p.Name` panics on the
   degraded path. Acceptable only if the caller is confirmed to nil-check before
   use; otherwise return a safe zero/empty representation or change the type.
3. **Unknown fallback semantics.** The context does not state whether the degraded
   value is a *correct* authoritative result. Treat as risk until confirmed;
   accepted only if the user explicitly documents the degraded value as acceptable.
4. **Negative cache stickiness.** `NegativeCache(d)` caches a failure that may have
   been transient for the full `d`. Acceptable if only stable failures (404) reach
   the negative cache; document which errors are cached.

## NOTED — observation for awareness, no action required

1. No `WithCoalesce` on a hot read key — a miss can stampede the origin. Flag; the
   *sizing* of the protection (and the `WithTimeout` panic if coalesce is added)
   straddles -timeouts — note and defer.
2. `WithCoalesce`/`RefreshAhead` present without `WithTimeout` — a build-time
   panic. Flag the correctness REQUIREMENT here; the panic itself is owned by
   -timeouts, so NOTE it cross-axis.
3. r8e never caches the fallback value (only genuine successes) — reliance
   confirmed correct; no demonstrated path to a poisoned cache.

## Anti-calibration (do NOT do this)

- Scoring `WithTimeout` *sizing* here — that is the timeouts axis (you only flag
  the coalesce/refresh-ahead REQUIREMENT as a correctness companion).
- Calling a `nil` SLICE fallback unsafe — an empty slice is safe to range; only a
  nil POINTER/MAP the caller derefs is the hazard.
- Marking IMPORTANT for "the ttl could be shorter" with no stated change interval
  and no caller harm — that is at most NOTED.
- Calling absence of a cache a BLOCKING — a missing cache returns correct (if
  slower) results; it is at most NOTED.
- Escalating the nil-POINTER fallback (IMPORTANT #2) to BLOCKING because the
  caller's nil-handling is UNKNOWN — UNKNOWN caller handling is IMPORTANT
  ("confirm the caller nil-checks / return a safe value"), not BLOCKING. Reserve
  BLOCKING for a caller SHOWN to deref unconditionally, a fabricated WRITE
  success, or a confirmed colliding/constant key. The verdict must not depend on
  who wrote the policy.
