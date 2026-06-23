# r8e policy — expert questionnaire (the no-code path)

When there is no code to read, the policy must be *derived from the service*. This
is the elicitation bank: the questions whose answers change a parameter, grouped
by axis, each with the concrete answer options and **what the answer changes**.

## How to run it

- **Ask only what you cannot infer and what actually moves a parameter.** If the
  user already said "read-only GET", do not ask whether it is idempotent.
- **Prefer `AskUserQuestion`** with the concrete options below; batch related
  questions (the tool takes up to 4 at once). Lead with the highest-leverage
  question of each axis.
- **Idempotency is the gate — ask it first.** It decides whether retry/hedge are
  even on the table; do not ask retry/hedge tuning before it is answered.
- **Never default a safety-critical unknown.** "I don't know if it's idempotent"
  is resolved by asking, not by assuming retriable. If the user truly cannot
  answer, treat the call as a non-idempotent write (the safe assumption) and say
  so.
- Record every answer as the *rationale source* for the parameter it drives, and
  every assumption you had to make as a flagged residual unknown.

## Axis 1 — call / Do (ask first)

1. **Is the operation a read or a write / does it have side effects?**
   - Read-only / idempotent → retry & hedge are safe.
   - Write / mutates state → retry & hedge are off by default.
   - Read with expensive side effects (e.g. logs a charge, triggers a job) → treat as write.
2. **If a write: does the call send an idempotency key the server honors?**
   - Yes → retry becomes safe (server dedups); hedge still risky for concurrent dupes.
   - No → no retry, no hedge until one is added.
3. **Does the downstream call honor `ctx` (cancellation/deadline)?**
   - Yes → timeout/budget can actually cancel in-flight work.
   - No / unsure → a timeout only stops *waiting*; flag that work leaks past it.
4. **Does the call own resources it must release (body, conn, file) on every path?**
   → informs whether a fallback/timeout can strand a resource.

## Axis 2 — timeouts / deadlines

1. **What are the downstream p50 / p99 latencies?**
   - Known numbers → size `WithTimeout` ≈ p99 × 2–4.
   - Unknown / highly variable → `WithTimeout(ceiling)` + `AdaptiveTimeout`.
2. **Is there a per-request budget or SLA for this call?**
   - Yes (e.g. "300 ms end to end") → `WithTimeBudget`.
   - No → a single `WithTimeout` may suffice.
3. **Does this call run inside a request that already has a deadline, or fan out
   to further callees?**
   - Inbound deadline exists → `RespectInboundDeadline()`.
   - Fans out → `PropagateDeadline()` (+ httpx header for HTTP).

## Axis 3 — retry / amplification

1. **Which errors are *permanent* (retrying never helps) vs *transient*?**
   - Typical: 4xx auth/validation = permanent; 5xx/timeouts = transient.
   - Answer drives the classifier — without it, retry hits permanent errors.
2. **Is this dependency shared by many callers / the whole fleet?**
   - Yes → add a retry budget (storm protection).
   - No / dedicated → a plain retry may be fine.
3. **Does the service emit `Retry-After` / 429 throttling?**
   - Yes → honored automatically (httpx) — confirm the adapter is used.
4. **How many retries can the upstream latency budget actually afford?**
   → caps `maxAttempts` and `MaxDelay`.

## Axis 4 — overload / capacity

1. **What is the downstream capacity** (connection-pool size, RPS quota,
   concurrency limit)?
   - Known static number → `WithBulkhead(n)`.
   - Unknown / variable → `WithAdaptiveConcurrency`.
2. **Under a burst, should excess callers wait or be rejected immediately?**
   - Wait → `BulkheadMaxWait` / `BulkheadCoDel`.
   - Reject → default bulkhead.
3. **Is the failure mode a hard down (errors) or a brownout (slow but 200)?**
   - Brownout → add `SlowCallRate`.
4. **Is there a hard external rate quota?** → `WithRateLimit`.
5. **Do you have an explicit SLO for this path, and traffic tiers that can be
   shed?** → `WithSLO` / `WithAdaptiveThrottle` + `Sheddability`.

## Axis 5 — fallback / cache

1. **Is there a value you can safely return when the call fails?**
   - Yes, and it's documented safe → `WithFallback`.
   - No / a wrong value is harmful → no fallback (surface the error).
   - Never for a write that fabricates success.
2. **Is stale data acceptable, and for how long?**
   - Yes → cache `+ StaleIfError(d)`.
   - No → no stale serving.
3. **Are reads repeated on the same keys, and how often does the data change?**
   - Repeated + slow-changing → `WithCache(ttl ≪ change interval)`.
   - Hot key, avoid expiry stampede → `+ RefreshAhead` / `+ WithCoalesce`.
4. **What uniquely identifies a request for caching/coalescing?**
   → the `keyFn`; a colliding key returns wrong results.

## Axis 6 — observability / ops

1. **Which failure modes must be observable** (alerting, dashboards)?
   → wire the corresponding `Hooks`.
2. **If this dependency is fully down, should this pod stop serving traffic?**
   - Yes AND it's dedicated → `WithReadinessImpact()`.
   - It's shared → do NOT gate readiness (fleet-wide flip).
3. **Do operators need to retune this at runtime without a redeploy?**
   - Yes → express config-expressible params via `r8econf` JSON.
4. **Is this a metrics/OTel shop?** → `r8eotel.Register` + `Trace`.

## Multi-service policies

If the policy fronts **several** services (e.g. a gateway), ask which call the
`Do` actually wraps — there is one policy per call. If the user wants one policy
"for the gateway", clarify whether they mean a shared budget tree
(`Parent`/`WithSharedRetryBudget`) across per-call policies, or a single
fan-out call. Do not collapse distinct downstreams into one policy with one
timeout.
