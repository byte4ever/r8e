*[Lire en Français](README.fr.md)*

# Example 19 — Retry Budget

Demonstrates the adaptive retry budget that throttles retries during a
downstream outage, so a struggling dependency is never buried under a *retry
storm* of its callers' own making.

## What it demonstrates

A policy is configured with `WithRetry(5, …)` capped by
`WithRetryBudget(MaxTokens(4), TokenRatio(0.1))`. The budget is a token bucket:
it starts full, every retryable failure removes one token, and every success
returns `0.1` tokens. While the bucket sits at or below half capacity, retries
are suppressed — the first attempt of each call still runs, but it is no longer
amplified into more load.

The example runs in three acts:

1. **Outage begins.** The bucket is full, so the first call spends its budget
   on real retries — it makes several attempts before failing. Those failed
   retries drain the bucket below half.
2. **Budget exhausted.** Calls 2 and 3 now report a single attempt each: the
   first try runs, but the budget refuses to retry. The `OnRetryBudgetExceeded`
   hook fires, and `Metrics()` plus `HealthStatus()` surface the throttling — a
   degraded health state that deliberately leaves readiness intact.
3. **Recovery.** A run of 30 successful calls slowly refills the bucket (0.1
   tokens at a time), climbs back above the half-mark, and clears the exhausted
   condition — retries would resume from here.

## Key concepts

| Concept | Detail |
|---|---|
| `WithRetryBudget(MaxTokens, TokenRatio)` | Token bucket governing the retry *rate*: failures drain it, successes refill it by `TokenRatio` each |
| First attempt always runs | The budget only gates retries (2nd attempt onward); requests keep flowing even when the bucket is empty |
| `r8e.Transient(err)` | Marks an error as retryable — only retryable failures drain the budget |
| `OnRetryBudgetExceeded` | Hook fired each time a retry is suppressed by the budget |
| `Metrics().RetryBudgetExceeded` / `RetryBudgetTokens` | Counter of shed retries and the live token level for dashboards |
| `retry_budget_exhausted` health | A *degraded* condition that never gates readiness — the service is degraded, not down |

## When to use

- Any retrying client of a dependency that can fail in bulk, where naive
  retries would multiply load on an already-struggling service.
- Use alongside (not instead of) per-call retry caps and backoff — the budget
  bounds the aggregate *rate*, while backoff spaces individual attempts.
- Share one budget across several policies (`WithSharedRetryBudget`) for a
  process-wide retry ceiling; aggregate its token gauge with `max`/`avg`, not
  `sum`, since every sharing policy reports the same level.

## Run

```bash
go run ./examples/19-retry-budget/
```

## Expected output

Three sections. Call 1 makes several attempts; calls 2 and 3 make a single
attempt each, and `OnRetryBudgetExceeded` fires. The observability block shows
retries suppressed, a low token count, and a degraded-but-healthy state. After
recovery the token count is back near capacity and the exhausted condition is
gone. The exact attempt counts are deterministic here, since the downstream
always fails immediately.
