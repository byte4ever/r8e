# Fixture: MEDIOCRE — catalog read policy

Calibration artifact of *known mediocre quality*. Expected fleet verdict:
**CONDITIONAL APPROVE** (several IMPORTANT findings across `retry`, `timeouts`,
`fallback`, and `observability`; no BLOCKING). This is the discriminating middle
artifact and the one used for the **attribution-inversion test** — its verdict
must be identical whether it is presented as the work of a staff engineer or an
intern.

> The artifact under review is the **service context + the policy code together**.

## Service context

- **Target**: internal product-catalog service, `GET /products/{id}`.
- **Operation**: read-only, **idempotent**, no side effects. Safe to retry, safe
  to hedge.
- **Error taxonomy**: `404 not_found` is permanent; `5xx` and timeouts are
  transient. No `Retry-After`.
- **Latency**: p50 ≈ 35 ms, p99 ≈ 120 ms. SLA: respond within 500 ms.
- **Criticality**: product pages degrade acceptably if a single item is missing,
  but showing a **wrong/empty** product as if authoritative is a UX defect.
- **Topology**: internal service shared by many callers; catalog data changes a
  few times per hour (price/stock updates).

## Policy under review

```go
policy := r8e.NewPolicy[*Product]("catalog-read",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithCache[*Product](cache, keyFn, 1*time.Hour),
    r8e.WithFallback[*Product](nil),
)

res, err := policy.Do(ctx, func(ctx context.Context) (*Product, error) {
    return catalog.Get(ctx, id) // honors ctx; idempotent
})
```
