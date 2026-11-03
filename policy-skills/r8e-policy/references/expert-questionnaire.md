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
     Under a breaker, a hung call also keeps its half-open probe slot until
     restart → `HalfOpenProbeLease` (axis 4).
4. **Does the call own resources it must release (body, conn, file) on every path?**
   → informs whether a fallback/timeout can strand a resource.
5. **Does the call go through another r8e policy (a client library built on
   r8e)?**
   - Yes → the function wraps or replaces that policy's errors before returning
     them, so its rejections are not taken for this policy's own
     (`IsLocalRejection`) and the breaker still counts them.
   - No → nothing to do.
6. **Is it an HTTP call through httpx, and do the callers read the response
   body after `Do`?**
   - Yes → bound the call with a deadline on the ctx passed to `Do`: `WithTimeout`,
     `WithHedge`, `PerAttemptTimeout` and `PropagateDeadline` cut the body once
     `Do` returns. No `WithCoalesce`, `WithCache` or static `WithFallback` on
     that client.
   - Does the status alone tell a retriable case from a final one (same 403 /
     404 for both)? No → `httpx.NewResponseClient` with a stateless classifier.

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
4. **What deadline do the callers set, and how much of it is left when this
   call starts?**
   → `WithTimeout`, `PerAttemptTimeout` and the budget stay strictly shorter
   than what is left. An r8e deadline equal or later arms no timer: the call
   ends with the caller's context error, `OnTimeout` never fires, and a hang is
   the caller's, not r8e's.
5. **With a budget: must it cut the call on time, and does anything read the
   error it ends with?**
   - Cut on time → `PropagateDeadline()` and a `WithTimeout` longer than the
     budget. `ErrTimeBudgetExceeded` then wraps the cut attempt's error only
     when it was the last attempt allowed.
   - A cooperative budget is fine → the last attempt may run past it, up to
     `WithTimeout`.

## Axis 3 — retry / amplification

1. **Which errors are *permanent* (retrying never helps) vs *transient*?**
   - Typical: 4xx auth/validation = permanent; 5xx/timeouts = transient.
   - Answer drives the classifier — without it, retry hits permanent errors.
2. **Is this dependency shared by many callers / the whole fleet?**
   - Yes → add a retry budget (storm protection).
   - No / dedicated → a plain retry may be fine.
3. **Does the service emit `Retry-After` / 429 throttling, and how long are the
   hints?**
   - Yes → honored automatically (httpx) — confirm the adapter is used. If the
     hints exceed `MaxDelay` or the budget, `HonorRetryAfter()` stops on those
     hints only, `RetryIf` stops on every hinted error: `MaxDelay` caps a hint,
     so a capped one re-sends early. Long hints on a quota → cooldown (axis 4).
4. **How many retries can the upstream latency budget actually afford?**
   → caps `maxAttempts` and `MaxDelay`.
5. **Do several routes share one parent retry budget, and must the others keep
   retrying when one route dies?**
   - Yes → `ShieldParent()` on each child budget, and a parent large enough to
     stay above half its capacity after a dead child's drain.
   - No, a storm in one route should throttle the others → plain `Parent`.

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
4. **Is there a hard external rate quota, and over which window?**
   → `WithRateLimit`. A sliding window (N per hour) → `Burst(1)`: the default
   bucket holds one second of tokens.
5. **Do you have an explicit SLO for this path, and traffic tiers that can be
   shed?** → `WithSLO` / `WithAdaptiveThrottle` + `Sheddability`.
6. **Which errors are the service's normal answer rather than a sign it is
   unwell (not-found, validation, business refusals)? Can the callers cancel
   the call, and what deadline do they set?**
   - Such errors exist, or callers cancel → `CircuitBreakerClassifier`: those
     errors, caller cancellations (`CallerCancellationIsNeutral`) and the
     policy's own rejections (`IsLocalRejection`) are neutral for the breaker,
     and `WithTimeout` stays strictly shorter than the callers' deadline so a
     hang still trips it.
   - Neither → the default breaker counting is right (the policy's own
     rejections still need `IsLocalRejection` when it has a bulkhead, a rate
     limiter or adaptive concurrency).
7. **If the dependency hangs while the breaker probes it, can a call stay stuck
   (it ignores `ctx`, or nobody knows)?**
   - Yes / unsure → `HalfOpenProbeLease(d)`, `d` above the worst time a probe
     can take (all attempts, backoff, bulkhead or rate-limiter wait).
   - No (the call honors `ctx` under `WithTimeout`) → no lease needed.
8. **Does the quota answer `429` with a `Retry-After`? How long are the hints,
   are refused calls counted against the quota, and do several routes share
   it? Does anything else (a `503` from one instance) send a hint?**
   - Long hints, or refused calls counted → `WithCooldown(MaxCooldown(cap))`.
   - Several routes on that quota → `WithSharedCooldown(NewCooldown(…))`.
   - Other responses carry hints too → `CooldownIf` narrowed to the quota's
     `429`.
   - Short hints, nothing counted → a breaker that counts the `429` may be
     enough.
9. **Do several routes or policies call this API through one capacity limit
   (pool, concurrency quota)? Does one route's call go through another?**
   - Shared limit → `WithSharedBulkhead(NewBulkhead(n))`, sized to the limit.
   - One route calls another on the same bulkhead → do not share it: a nested
     call wedges it under load.
10. **Can the call outlive its timeout (slow to stop on `ctx`), and must the
    bulkhead cap those calls?** → no `WithHedge` in that policy: the hedge frees
    the slot on the cut.

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
5. **With coalescing or refresh-ahead: is the policy owned by a client that
   shuts down, and do all its holders stop with that client?**
   - Yes → `WithBaseContext(clientCtx)` stops the detached calls on shutdown.
   - Holders outlive the client → no base context: once it ends, every
     coalesced miss fails with `context.Canceled`.

## Axis 6 — observability / ops

1. **Which failure modes must be observable** (alerting, dashboards)?
   → wire the corresponding `Hooks`, safe for concurrent use (they may run
   after `Do` returned). With a classifier, include `OnCircuitNeutral`.
2. **If this dependency is fully down, should this pod stop serving traffic?**
   - Yes AND it's dedicated → `WithReadinessImpact()`.
   - It's shared → do NOT gate readiness (fleet-wide flip).
3. **Do operators need to retune this at runtime without a redeploy?**
   - Yes → express config-expressible params via `r8econf` JSON.
4. **Is this a metrics/OTel shop?** → `r8eotel.Register` + `Trace`.
5. **Do logs, dashboards or alerts match error text or `r8e.rejection_reason`
   values?** → `r8e.ErrorCode(err)` for labels. Since v0.13.0 there is no
   `permanent:` / `transient:` prefix, and a retried panic is `panic_recovered`,
   no longer `retries_exhausted`.
6. **Is the policy built once at start-up, or per job / tenant / request?**
   - Once → nothing to do.
   - Repeatedly → `defer policy.Registry().Unregister(policy)`, an empty name, or
     a `WithRegistry` registry dropped with the policies; else the registry and
     its health endpoints grow without bound.

## Multi-service policies

If the policy fronts **several** services (e.g. a gateway), ask which call the
`Do` actually wraps — there is one policy per call. If the user wants one policy
"for the gateway", clarify whether they mean shared guards across per-call
policies (a budget tree with `Parent`/`WithSharedRetryBudget`, a
`WithSharedBulkhead`, a `WithSharedCooldown`), or a single fan-out call. Do not
collapse distinct downstreams into one policy with one timeout.
