# Fixture: POOR — payment charge policy

Calibration artifact of *known poor quality*. Expected fleet verdict: **REJECT**
(multiple BLOCKING across the `call`, `retry`, `timeouts`, `fallback`, and
`observability` axes). Used by the per-axis `good-reviews.md` and by the
end-to-end calibration run.

> The artifact under review is the **service context + the policy code together**.
> A policy can only be judged against the call it wraps.

## Service context

- **Target**: third-party payment gateway, `POST /v1/charges` (Stripe-like).
- **Operation**: charges a customer's card. **Non-idempotent write** — each call
  moves money. The gateway supports an `Idempotency-Key` header, but the call
  below does not send one.
- **Error taxonomy**: `402 card_declined` and `400 invalid_request` are
  permanent (retrying never helps); `409 conflict` and `5xx` are transient. The
  gateway returns `429` with a `Retry-After` header under throttling.
- **Latency**: p50 ≈ 220 ms, p99 ≈ 900 ms.
- **Criticality**: on the checkout critical path. A failed charge MUST surface to
  the caller — a silent "success" is a financial incident.
- **Topology**: a single shared third-party dependency hit by every replica.

## Policy under review

```go
// Charge a customer's card through the payment gateway.
policy := r8e.NewPolicy[ChargeResult](
    "", // no name
    r8e.WithCoalesce(func(ctx context.Context) string { return "" }),
    r8e.WithRetry(5, r8e.ConstantBackoff(200*time.Millisecond)),
    r8e.WithHedge(50*time.Millisecond),
    r8e.WithFallback(ChargeResult{Status: "ok"}),
    r8e.WithReadinessImpact(),
)

res, err := policy.Do(ctx, func(_ context.Context) (ChargeResult, error) {
    // Ignores the policy ctx entirely; sends no idempotency key.
    return gateway.Charge(amount, card)
})
```
