# Checklist — call / Do axis

Per-category checks for the wrapped call. Each check is phrased so a "yes" to the
*hazard* is an observation to score. Enumerate; do not pre-dismiss.

## Idempotency & operation class
- [ ] Is the operation class (read/idempotent vs write/side-effecting) STATED? If
      UNKNOWN, that is the first observation — unresolved risk.
- [ ] Does a "read" actually have hidden side effects (writes a log/charge,
      triggers a job, mutates a counter)? Then it is a write.
- [ ] Is the operation safe to execute *more than once* with the same result?

## Retry-safety (interaction with WithRetry)
- [ ] Is `WithRetry` present while the op mutates state?
- [ ] If so, does the call send an idempotency key the server dedups on? (No key
      + retry on a write = duplicate effect.)
- [ ] Could a retry after a *partial* application leave inconsistent state
      (some sub-steps done, others not)?
- [ ] Does re-execution re-read state that the first attempt changed
      (read-after-write hazard)?

## Hedge-safety (interaction with WithHedge)
- [ ] Is `WithHedge` present while the op has side effects? Two concurrent
      executions run the write twice.
- [ ] If hedging a write with an idempotency key: does the key collapse
      *concurrent* duplicates, not just sequential ones?
- [ ] Do the two hedge goroutines share captured state (maps, slices, struct
      fields, a buffer) mutated without synchronisation?

## Context honoring & cancellability
- [ ] Does the wrapped fn accept the policy `ctx` (signature `func(ctx) (T,
      error)`), or does it ignore it (`func(_ context.Context)`)?
- [ ] Is `ctx` actually threaded into the downstream I/O (HTTP request, DB query,
      RPC)? A call that builds its own `context.Background()` defeats the policy.
- [ ] On timeout/cancel, does the downstream operation actually STOP, or does it
      run to completion while the caller already got `ErrTimeout` (orphaned side
      effect)?
- [ ] Is there blocking/unbounded work (infinite read, no deadline) the policy
      cannot interrupt?

## Resource cleanup (every exit path)
- [ ] Response body / connection / file / lock released on the success path?
- [ ] On the ERROR path, the timeout path, the cancel path, the panic path?
- [ ] Does a retry that drains+reuses a body conflict with a permanent path that
      hands the body to the caller (httpx contract)?

## Error taxonomy (enumerate; acting on it is the retry/fallback axes)
- [ ] What error types does the call actually return?
- [ ] Are permanent failures (4xx auth/validation, business rejections)
      distinguishable from transient ones — or are they all `error`?
- [ ] Does the call ever return a NIL error with an empty/zero result that
      downstream treats as authoritative?

## Panic & determinism
- [ ] Can the fn panic (nil deref, type assertion, map write race)? Is
      `WithRecover` present if so?
- [ ] Does the fn use a token-refresh / RNG / time-based branch that behaves
      differently across retries/hedges?

## Nested guards
- [ ] Does the call have its OWN internal timeout/retry/breaker that double-wraps
      or fights the policy's (e.g. an `http.Client.Timeout` shorter than
      `WithTimeout`, so the policy's deadline never bites)?

## Red lines (any one is BLOCKING on its own)
- Retry or hedge on a non-idempotent write with no server-side dedup.
- A timeout/cancel that returns while the write commits downstream.
- A response body / connection leaked on a non-happy path under load.
