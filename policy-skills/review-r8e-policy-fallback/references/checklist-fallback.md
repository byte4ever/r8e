# Checklist — fallback / cache axis

Per-category checks for the degraded VALUE and the cached/coalesced result. Each
check is phrased so a "yes" to the *hazard* is an observation to score. Enumerate;
do not pre-dismiss.

## Fallback value safety (WithFallback / WithFallbackFunc)
- [ ] Is a fallback present at all? (Absence on a read with a documented safe
      degraded value is a missed opportunity, not a hazard; absence on a write is
      correct.)
- [ ] Does the fallback fabricate a SUCCESS for a non-idempotent WRITE? A
      `WithFallback(Result{Status:"ok"})` on a failed charge tells the caller the
      money moved — a silent incident. NEVER fabricate write success.
- [ ] Is the static fallback value safe to CONSUME for its type? A `nil` pointer
      (`WithFallback[*T](nil)`) or `nil` map the caller will deref/index is a
      nil-panic; a `nil` slice is safe to range (empty result).
- [ ] Does the value mislead a caller that treats it as AUTHORITATIVE (a default
      that hides a real distinction — "balance 0" vs "balance unknown")?
- [ ] If using `WithFallbackFunc`, does it DISCRIMINATE the shed sentinels
      (`errors.Is(err, ErrCircuitOpen/ErrRateLimited/ErrBulkheadFull/ErrSLOShed/…)`)
      from a genuine downstream error, when the degraded value is only appropriate
      for one of them? A func that returns the degraded value on EVERY error may
      mask a real failure that must surface.
- [ ] Is the fallback's semantic correctness STATED by the context, or UNKNOWN?
      UNKNOWN is unresolved risk.

## Cache key correctness (WithCache keyFn)
- [ ] Can two DISTINCT requests map to the SAME key (drops a field of request
      identity — id, tenant, user, locale, currency)? A colliding key returns one
      caller's data to another → WRONG results.
- [ ] Does keyFn return an EMPTY or CONSTANT key? Empty opts every call out (the
      cache does nothing); a fixed non-empty key collapses everyone onto one entry
      (worse — universal collision).
- [ ] Is the key derived from request identity in `ctx` / the request, not from a
      pointer address or a mutable field that changes between equivalent requests?

## Cache freshness (ttl / stale-if-error / negative cache)
- [ ] Is the ttl long relative to the data's CHANGE interval? A 1h ttl on data
      that changes a few times per hour serves STALE data as authoritative past
      the change — a UX/correctness defect.
- [ ] Is `StaleIfError(d)` appropriate — does the data tolerate being served stale
      ON FAILURE (RFC 5861), or must a failure surface fresh?
- [ ] Is `NegativeCache(d)` caching the RIGHT failures (a stable 404), not a
      transient that should not stick for `d`?
- [ ] Reliance on r8e NEVER caching the fallback value (only genuine successes):
      is that reliance correct here (a fallback won't poison the cache)?

## Refresh-ahead & coalesce (correctness companions)
- [ ] If `RefreshAhead(d<ttl)` is used, is `WithTimeout` present? Its absence is a
      build-time panic `ErrRefreshAheadWithoutTimeout` (the detached reload is
      unbounded). Flag the REQUIREMENT; defer the timeout *sizing* to -timeouts.
- [ ] Does the `WithCoalesce` keyFn collapse only EQUIVALENT requests? Collapsing
      DISTINCT requests returns one caller's result to another — a correctness bug.
- [ ] Is `WithCoalesce` present without `WithTimeout`? Build-time panic
      `ErrCoalesceWithoutTimeout`. Flag the REQUIREMENT; the panic is owned by
      -timeouts, so NOTE it cross-axis.
- [ ] Is there a HOT key with no `WithCoalesce` so a single miss lets N concurrent
      callers stampede the origin? (Missed protection, not a wrong-result bug.)

## Red lines (any one is BLOCKING on its own)
- A fallback that fabricates a SUCCESSFUL result for a failed non-idempotent WRITE
  — callers believe the side effect succeeded (a silent incident).
- A cache or coalesce keyFn that COLLIDES distinct requests — returning one
  caller's data to another (wrong result served as authoritative).
