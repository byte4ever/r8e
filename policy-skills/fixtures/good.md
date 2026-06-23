# Fixture: GOOD — recommendations read policy

Calibration artifact of *known good quality*. Expected fleet verdict:
**APPROVE** (at most NOTED observations; every adversarial scenario is
dismissable in writing). The positive anchor for calibration.

> The artifact under review is the **service context + the policy code together**.

## Service context

- **Target**: internal recommendations service, `GET /recommend?user={id}`.
- **Operation**: read-only, **idempotent**, no side effects. Safe to retry and to
  hedge.
- **Error taxonomy**: classified at the call boundary — `4xx` marked
  `r8e.Permanent`, `5xx`/timeouts left transient (the r8e default), `429` carries
  a `Retry-After` header honored automatically.
- **Latency**: p50 ≈ 30 ms, p99 ≈ 80 ms. Budget for this call: 300 ms.
- **Criticality**: best-effort. An **empty** recommendations list is an
  explicitly acceptable degraded experience (documented product decision) — the
  page renders without the rail.
- **Topology**: internal service shared across the fleet; the HTTP client pool
  for it is capped at 32 connections.
- **Client contract**: `recommender.Get` forwards the policy `ctx` to its HTTP
  request (`http.NewRequestWithContext`), closes the response body on every exit
  path, cannot panic on a malformed response (validated), and records no
  impression/telemetry side effect — it is a pure read with no internal timeout
  of its own.

## Policy under review

```go
policy := r8e.NewPolicy[[]Item]("recommend-read",
    // Hard ceiling ≈ p99 (80 ms) × generous headroom; bounds retry + the whole call.
    r8e.WithTimeout(300*time.Millisecond),
    // Idempotent read → bounded retries with jittered backoff, capped.
    r8e.WithRetry(3, r8e.ExponentialJitterBackoff(20*time.Millisecond),
        r8e.MaxDelay(150*time.Millisecond)),
    // Gate retries against a token bucket so a partial outage can't amplify load.
    r8e.WithRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1)),
    // Trip on failures AND on a brownout (slow-but-200), recover gently.
    r8e.WithCircuitBreaker(
        r8e.FailureThreshold(5),
        r8e.RecoveryTimeout(10*time.Second),
        r8e.SlowCallRate(150*time.Millisecond, 0.5),
    ),
    // Sized to the downstream connection pool, not guessed.
    r8e.WithBulkhead(32),
    // Empty recs is a documented, safe degraded value — never cached, only on failure.
    r8e.WithFallback[[]Item](nil),
    // Failure modes are observable.
    r8e.WithHooks(&r8e.Hooks{
        OnCircuitOpen:  func() { metrics.Inc("recommend.circuit_open") },
        OnRetry:        func(attempt int, err error) { log.Warn("recommend retry", attempt, err) },
        OnFallbackUsed: func(err error) { metrics.Inc("recommend.fallback") },
    }),
    // Readiness deliberately NOT gated: a shared dependency must not flip the
    // whole fleet's /readyz at once.
)

res, err := policy.Do(ctx, func(ctx context.Context) ([]Item, error) {
    // Honors ctx (cancellation/deadline propagate to the HTTP call); idempotent GET.
    return recommender.Get(ctx, userID)
})
```
