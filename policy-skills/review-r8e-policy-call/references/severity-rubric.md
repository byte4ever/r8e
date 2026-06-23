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
   httpx transient/permanent body contract).
5. **Shared mutation across hedge goroutines.** The fn appends to a captured slice;
   two hedge executions race it. Propagation: data race → corrupt result or panic
   under `-race`/production. Remediation: no shared mutable capture, or no hedge.

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

## NOTED — observation for awareness, no action required

1. The fn returns a typed error but the policy does not yet classify it — flag and
   defer to review-r8e-policy-retry (out of this axis's lane to *act on*).
2. The call could expose request identity for a future cache key — defer to
   review-r8e-policy-fallback.
3. A panic is theoretically possible but the inputs are fully validated upstream
   and `WithRecover` is present — no demonstrated path.

## Anti-calibration (do NOT do this)

- Scoring "no retry budget" here — that is the retry axis.
- Calling an idempotent read's retry BLOCKING — re-execution is harmless.
- Marking IMPORTANT for "could be cleaner" with no failure path — that is at most
  NOTED, usually nothing.
