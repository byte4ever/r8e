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
- [ ] Does the func use `IsLocalRejection` to spot shedding? It matches only six
      bare sentinels (not `ErrCircuitOpen`, `ErrThrottled`, `ErrSLOShed`, nor a
      wrapped one): it is for the breaker classifier. A fallback uses
      `errors.Is`.
- [ ] With a cooldown (`WithCooldown` / `WithSharedCooldown`): does a func meant
      for shedding handle `ErrCoolingDown` (the policy's own refusal while the
      server's Retry-After holds)? And, under `WithRetry`, the server's error
      that the loop returns AS IS when the cooldown or `HonorRetryAfter` stops
      it: a func keyed on `ErrRetriesExhausted` misses it.
- [ ] Does the func degrade only on `ErrTimeout` / `ErrTimeBudgetExceeded`? A
      call that the callers' deadline ends first, or on the same instant,
      returns the caller's context error (`context.DeadlineExceeded`), not the
      r8e sentinel: the degraded value is never served for that hang. Is the
      r8e deadline strictly shorter than the callers'?
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
- [ ] If `NegativeCache(d)` is there to shield a hung or slow origin, is
      `WithTimeout` shorter than the callers' deadlines? A call whose own
      context is done when it returns (cancelled or past its deadline) is never
      cached, so without that timeout the hung origin is asked again on every
      call. Do not flag a caller's cancellation as poisoning the negative
      cache: it is never cached.
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
- [ ] Is `WithCache` or `WithCoalesce` on an httpx `Client`? The shared value is
      an `*http.Response`: every caller gets the same response and the same body
      stream (one reads it, the others read nothing or interleave).

## Detached calls & base context (WithBaseContext)
- [ ] With `WithCoalesce` or `RefreshAhead` and no `WithBaseContext`: the
      detached calls ignore the caller's cancellation and outlive the client's
      shutdown, bounded only by `WithTimeout`. Does the context say shutdown must
      stop in-flight work (resources closed under them)?
- [ ] With `WithBaseContext(ctx)`: does `ctx` end only when EVERY holder of the
      policy stops (the client's own lifetime), or earlier (a request context, a
      per-job context on a policy shared across jobs)? Once it has ended, every
      coalesced call that misses the cache fails at once with `context.Canceled`,
      for every holder and for good; a cache hit, `StaleIfError` or
      `WithFallback` still answers; calls with no coalescing key still run.
- [ ] Is the policy with a base context wrapped by another policy or cache? This
      policy stores no negative entry once the base has ended, but the wrapper
      caches the `context.Canceled` it returns.

## Red lines (any one is BLOCKING on its own)
- A fallback that fabricates a SUCCESSFUL result for a failed non-idempotent WRITE
  — callers believe the side effect succeeded (a silent incident).
- A cache or coalesce keyFn that COLLIDES distinct requests — returning one
  caller's data to another (wrong result served as authoritative).
- `WithCache` or `WithCoalesce` on an httpx `Client` (one `*http.Response` and
  body stream shared by every caller).
- `WithBaseContext` given a context visibly shorter-lived than the policy's
  holders (every coalesced miss fails with `context.Canceled` once it ends).
