# Fixture: GOOD — recommendations read policy

Calibration artifact of *known good quality*. Expected fleet verdict:
**APPROVE** (at most NOTED observations; every adversarial scenario is
dismissable in writing). The positive anchor for calibration.

> The artifact under review is the **service context + the policy code together**.

## Service context

- **Target**: internal recommendations service, `GET /recommend?user={id}`.
- **Operation**: read-only, **idempotent**, no side effects. Safe to retry and to
  hedge.
- **Error taxonomy**: classified at the call boundary — `4xx` other than `429`
  marked `r8e.Permanent`, `5xx`/timeouts left transient (the r8e default). A
  `429` carries a `Retry-After` of at least 1 s (its error implements
  `r8e.RetryAfterProvider`); no other response carries one.
- **Latency**: p50 ≈ 30 ms, p99 ≈ 80 ms. Budget for this call: 300 ms.
- **Callers**: the call runs under the inbound HTTP request's context, which the
  client can cancel (page navigated away) and which carries a 1 s deadline set by
  the edge proxy. This call is the request's first downstream call, so at least
  900 ms of that deadline are left when it starts, well above this call's
  300 ms budget.
- **Criticality**: best-effort. An **empty** recommendations list is an
  explicitly acceptable degraded experience (documented product decision) — the
  page renders without the rail.
- **Topology**: internal service shared across the fleet; the HTTP client pool
  for it is capped at 32 connections.
- **Client contract**: `recommender.Get` forwards the policy `ctx` to its HTTP
  request (`http.NewRequestWithContext`), closes the response body on every exit
  path, cannot panic on a malformed response (validated), and records no
  impression/telemetry side effect — it is a pure read with no internal timeout
  of its own. It is a plain HTTP client, not built on another r8e policy, so it
  never returns an r8e sentinel error.
- **Lifecycle**: the policy is built once at service start and shared by every
  request; it is never rebuilt per request, so it registers once.

## Policy under review

```go
// Built once at service start and shared by every request (registered once).
policy := r8e.NewPolicy[[]Item]("recommend-read",
    // Hard ceiling ≈ p99 (80 ms) × generous headroom; bounds retry + the whole call.
    r8e.WithTimeout(300*time.Millisecond),
    // Idempotent read → bounded retries with jittered backoff, capped. A 429
    // asks for ≥ 1 s, which never fits the 300 ms budget, and MaxDelay would
    // cut its Retry-After short: do not re-send it early, fail to the empty rail.
    r8e.WithRetry(3, r8e.ExponentialJitterBackoff(20*time.Millisecond),
        r8e.MaxDelay(150*time.Millisecond),
        r8e.RetryIf(func(err error) bool {
            var throttled r8e.RetryAfterProvider
            return !errors.As(err, &throttled)
        })),
    // Gate retries against a token bucket so a partial outage can't amplify load.
    r8e.WithRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1)),
    // Trip on failures AND on a brownout (slow-but-200), recover gently.
    r8e.WithCircuitBreaker(
        r8e.FailureThreshold(5),
        r8e.RecoveryTimeout(10*time.Second),
        r8e.SlowCallRate(150*time.Millisecond, 0.5),
        // Count only what says the recommender is unwell: a permanent 4xx is
        // its normal answer, our own bulkhead's rejection never reached it
        // (IsLocalRejection matches it as the bulkhead returns it; the client
        // never returns an r8e sentinel), and a caller that gives up says
        // nothing about it. A 429 still counts, so sustained throttling opens
        // the breaker and backs off. The 300 ms WithTimeout ends strictly
        // before the request deadline (900 ms or more left), so a hang still
        // counts as a failure.
        r8e.CircuitBreakerClassifier(func(ctx context.Context, err error) r8e.Outcome {
            if r8e.IsPermanent(err) || r8e.IsLocalRejection(err) {
                return r8e.OutcomeNeutral
            }
            return r8e.CallerCancellationIsNeutral(ctx, err)
        }),
    ),
    // Sized to the downstream connection pool, not guessed.
    r8e.WithBulkhead(32),
    // Empty recs is a documented, safe degraded value — never cached, only on failure.
    r8e.WithFallback[[]Item](nil),
    // Failure modes are observable. Hooks run concurrently and may run after
    // Do returned; these only call the metrics and logging libraries, both
    // safe for concurrent use, and keep no state of their own.
    r8e.WithHooks(&r8e.Hooks{
        OnCircuitOpen: func() { metrics.Inc("recommend.circuit_open") },
        // What the classifier sets aside: callers abandoning a slow
        // recommender only show here.
        OnCircuitNeutral: func() { metrics.Inc("recommend.circuit_neutral") },
        OnRetry:          func(attempt int, err error) { log.Warn("recommend retry", attempt, err) },
        OnFallbackUsed:   func(err error) { metrics.Inc("recommend.fallback") },
    }),
    // Readiness deliberately NOT gated: a shared dependency must not flip the
    // whole fleet's /readyz at once.
)

// In the request handler, with the request's ctx:
res, err := policy.Do(ctx, func(ctx context.Context) ([]Item, error) {
    // Honors ctx (cancellation/deadline propagate to the HTTP call); idempotent GET.
    return recommender.Get(ctx, userID)
})
```
