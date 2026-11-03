# Severity rubric — call / Do axis

Calibrated examples. Severity rests on a **demonstrated failure mode**, never on
aesthetic preference or a best-practice cited without a concrete case. When in
doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **Retried write, no idempotency key.** `WithRetry(5, …)` wraps `gateway.Charge`
   on a non-idempotent endpoint with no `Idempotency-Key`. Propagation: transient
   5xx → retry → the card is charged twice. Remediation: remove retry, OR send an
   idempotency key the gateway dedups on.
2. **Hedged write.** `WithHedge(50ms)` on a charge. Propagation: primary slow →
   hedge fires → two concurrent charges. Remediation: no hedge on side-effecting
   calls.
3. **Timeout cannot cancel.** The fn builds its own `context.Background()` (or the
   underlying client ignores ctx). Propagation: `WithTimeout` fires, caller gets
   `ErrTimeout`, the write still commits downstream — an orphaned side effect the
   caller believes failed. Remediation: thread the policy ctx into the call.
4. **Leaked response body on the error path.** The fn returns early on non-2xx
   without closing `resp.Body`. Propagation: under load, fd/connection exhaustion
   → the whole client stalls. Remediation: `defer resp.Body.Close()` (mind the
   httpx transient/permanent body contract). Same class: an httpx `Client` with a
   static `WithFallback`, which drops the `StatusError` keeping a permanent body
   open; or, under `WithRetry`, a caller that closes a permanent body only
   through `resp`, which `Do` returns nil with any error (close
   `StatusError.Response.Body`, found with `errors.As`).
5. **Shared mutation across hedge goroutines, or with the caller after a
   timeout.** The fn appends to a captured slice; two hedge executions race it.
   Or, under `WithTimeout`, the fn writes a captured variable the caller reads
   once `Do` returned `ErrTimeout`, while the fn is still running. Propagation:
   data race → corrupt result or panic under `-race`/production. Remediation:
   no shared mutable capture (return the value through the fn's result), or no
   hedge. Same class: an `httpx.NewResponseClient` classifier that writes state
   of its own (a map, a counter) or modifies `body`; it runs concurrently with
   itself and after `Do` returned.
6. **httpx body read after `Do`, cut by an r8e deadline.** The caller decodes
   `resp.Body` after `client.Do(ctx, req)` on an httpx `Client` built with
   `WithTimeout(2s)` (or `WithHedge`, `PerAttemptTimeout`, a `PropagateDeadline`
   budget, a `StandardHTTPClient` preset). Propagation: those end the attempt's
   context when they return; net/http cuts the body; the decoder reads the
   buffered start, then `context.Canceled`. Symptom: every response larger than
   the buffer fails to decode, on the happy path. Remediation: bound the call
   with a deadline on the ctx passed to `Do`, and drop the r8e deadline from that
   client.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **Unknown idempotency.** The service context does not state whether the op is a
   write, and the policy retries. Treat as risk until confirmed; accepted only if
   the user explicitly confirms idempotency in writing.
2. **Partial-write retry window.** A multi-step write is retried as a whole; a
   crash between steps could double a sub-step. Acceptable only if each sub-step is
   itself idempotent — document it.
3. **Read-after-write inside a retry.** Re-execution re-reads state the first
   attempt mutated; the second result differs. Acceptable if the caller tolerates
   it; document.
4. **Nested timeout mismatch.** The client's own timeout is shorter than the
   policy's, so `WithTimeout` never bites. Not a safety hole but the policy knob is
   inert — reconcile them.
5. **A hung call under a breaker.** The fn ignores `ctx` (`func(_
   context.Context)`, or a client that takes none) on a read under
   `WithCircuitBreaker`. Propagation: the dependency hangs while the breaker
   probes it; the probes never return; `WithTimeout` answers the callers but
   leaves the probes in their slots; the breaker stays half-open and fails every
   call with `ErrCircuitOpen` until restart. Remediation: thread `ctx` into the
   call; the `HalfOpenProbeLease` safety net is the overload axis's. When the
   client's `ctx` handling is UNKNOWN, the finding is "confirm it forwards
   `ctx`". (On a write, the ignored `ctx` is already BLOCKING #3.)
6. **httpx body read after `Do`, not shown.** The policy is an httpx `Client`
   with `WithTimeout`, `WithHedge`, `PerAttemptTimeout` or a `PropagateDeadline`
   budget, and whether the callers read the body after `Do` is UNKNOWN. The
   finding is "confirm the body is read inside the call, or move the deadline to
   the ctx passed to `Do`". Shown reading it after `Do`: BLOCKING #6.

## NOTED — observation for awareness, no action required

1. The fn returns a typed error but the policy does not yet classify it — flag and
   defer to review-r8e-policy-retry (out of this axis's lane to *act on*).
2. The call could expose request identity for a future cache key — defer to
   review-r8e-policy-fallback.
3. A panic is theoretically possible but the inputs are fully validated upstream
   and `WithRecover` is present — no demonstrated path.
4. The fn returns an r8e sentinel bare (a nested policy's error passed through).
   Flag that it should wrap or replace it, and defer what the breaker counts to
   review-r8e-policy-overload.
5. The error the caller gets at a deadline is not the fn's own (`ErrTimeout` at
   a binding `WithTimeout`, the caller's context error once it ended,
   `ErrAttemptTimeout` around a `PerAttemptTimeout` cut). Enumerate it, and
   defer what matches on it to the timeouts, retry and fallback axes.

## Anti-calibration (do NOT do this)

- Scoring "no retry budget" here — that is the retry axis.
- Scoring the absence or the sizing of `HalfOpenProbeLease` here — that is the
  overload axis. You score the ignored `ctx` that lets a probe hang.
- Calling an idempotent read's retry BLOCKING — re-execution is harmless.
- Marking IMPORTANT for "could be cleaner" with no failure path — that is at most
  NOTED, usually nothing.
