# r8e policy — trait → pattern decision matrix

Each row maps a **service/call trait** to the options it implies, a parameter
range, and the one-line rationale to carry into the output. Read top-down: the
call's nature (idempotency) gates everything below it. Validate the assembled set
against `api-constraints.md` before presenting.

## 1. Idempotency & side effects (the gate)

| Trait | Implies | Rationale |
|---|---|---|
| Idempotent read (GET) | `WithRetry` OK, `WithHedge` OK | re-execution and duplication are harmless |
| Non-idempotent write, **no** idempotency key | NO `WithRetry`, NO `WithHedge` (or make the op idempotent first) | each re-execution moves state twice |
| Non-idempotent write **with** an idempotency key the call sends | `WithRetry` OK (dedup downstream), `WithHedge` only if the key dedups concurrent dupes | the key collapses duplicates server-side |
| Call ignores `ctx` | fix the call first; `WithTimeout` cannot cancel it (only stops *waiting*); under a breaker, add `HalfOpenProbeLease` as a safety net (§4) | a "timeout" that can't cancel leaks work, and a hung probe keeps its half-open slot |
| Call goes through another r8e policy (nested) | the function wraps or replaces the nested policy's errors before returning them | a bare nested rejection reads as this policy's own (`IsLocalRejection`): the breaker would never count the downstream's saturation |

## 2. Latency profile → deadlines

| Trait | Implies | Range |
|---|---|---|
| Known p99 | `WithTimeout` ≈ p99 × 2–4 headroom (hard ceiling) | tight enough to protect, loose enough not to false-trip |
| Callers set a deadline (request ctx) | keep `WithTimeout`, `PerAttemptTimeout` and a budget **strictly shorter** than the time it leaves when the call starts | an r8e deadline equal or later arms no timer: the caller's context error ends the call, no `OnTimeout`, and the hang is the caller's |
| Variable / unknown p99 | `WithTimeout(ceiling)` + `AdaptiveTimeout(…)` | adaptive tightens below the ceiling from live successes |
| Per-request budget / SLA across retries+hedge | `WithTimeBudget(d)` (needs a retry/hedge consumer) | caps the *sum*, tighter than per-attempt |
| Cross-service deadline already on inbound ctx | `WithTimeBudget(d, RespectInboundDeadline())` | never run past the caller's deadline; a budget tightened to it is the caller's (its error, no `OnTimeBudgetExceeded`) |
| This call fans out to further callees | `WithTimeBudget(d, PropagateDeadline())` (+ `httpx.InjectDeadline` for HTTP) | downstream stops when our budget is spent |
| Heavy right tail, idempotent | `WithHedge(delay)` near p95, ideally `AdaptiveHedge(…)` + a concurrency budget | race only genuine stragglers, cap extra load |

## 3. Error taxonomy & sharing → retry

| Trait | Implies | Range / note |
|---|---|---|
| Has permanent error modes (4xx auth/validation) | classify them `Permanent` (or `RetryIf`) | else retry burns attempts on hopeless errors |
| Transient faults (5xx/timeouts) | `WithRetry(2–4, ExponentialJitterBackoff(base), MaxDelay(cap))` | jitter avoids synchronized retry waves |
| Shared dependency hit by the whole fleet | add `WithRetryBudget` (per-policy) or `WithSharedRetryBudget` | caps aggregate retry pressure → no retry storm |
| Multiple policies under one gateway | nest budgets via `r8e.Parent(parent)` | a storm in one leaf throttles its siblings |
| Emits `Retry-After` (429/503) | honored automatically if the error implements `RetryAfterProvider` (httpx `StatusError` does); if the hints exceed `MaxDelay` or the budget, do not retry them (`RetryIf`) | `MaxDelay` caps the hint: a capped one re-sends early |
| Backpressure you can react to (429/Retry-After) | consider `WithRateLimit(rate, AIMD(…))` | client rate adapts to server overload |

## 4. Capacity & overload → shedding

| Trait | Implies | Range |
|---|---|---|
| Known static downstream capacity (pool size) | `WithBulkhead(poolSize)` | cap in-flight to what downstream can serve |
| Bursty arrivals, want to wait not reject | `WithBulkhead(n, BulkheadMaxWait(d))` or `BulkheadCoDel(target, interval)` | FIFO wait / controlled-delay shedding |
| Unknown / variable capacity | `WithAdaptiveConcurrency(…)` (Gradient2) — NOT with Bulkhead | self-tunes from RTT |
| Repeatedly-failing dependency | `WithCircuitBreaker(FailureThreshold, RecoveryTimeout)` | fast-fail while it's down |
| Errors that say nothing about the dep's health reach the breaker (business/`Permanent` 4xx, caller cancellations, rejections by a bulkhead / rate limiter / adaptive concurrency inside the breaker) | add `CircuitBreakerClassifier(fn)`: `Permanent`/business and `IsLocalRejection(err)` → `OutcomeNeutral`, the rest → `CallerCancellationIsNeutral(ctx, err)` | a run of them would open the breaker on a healthy dep; `IsLocalRejection` matches by identity, so a nested policy's wrapped rejection still counts (`errors.Is` would hide it) |
| Caller cancellations are neutral | keep an r8e deadline (`WithTimeout`, `PerAttemptTimeout`, `PropagateDeadline` budget) **strictly** shorter than the callers' | else a hanging dep ends as neutral cancellations and never trips |
| Under a breaker, a call can hang without returning (ignores `ctx`, or its `ctx` handling is unknown) | `HalfOpenProbeLease(d)`, `d` above the worst admission-to-report span: all attempts + backoff + bulkhead / rate-limiter wait | else a hung probe keeps the breaker half-open until restart (`WithTimeout` does not free its slot); a shorter `d` fails probes that would succeed |
| Brownout (slow-but-200) is the real failure | add `SlowCallRate(d, rate)` | failure trip alone misses brownouts |
| Healing downstream re-floods on close | add `RampRecovery(window)` | slow-start admission |
| Hard external quota / RPS limit | `WithRateLimit(rate)` (+ `RateLimitBlocking()` to wait) | stay under quota |
| Client-side load shed by backend health | `WithAdaptiveThrottle(…)` | proportional shed before the binary trip |
| Shed by a stated SLO's burn rate | `WithSLO(target, …)` | shed when the error budget burns too fast |
| Has criticality tiers | stamp `WithSheddability(ctx, …)`; throttle/SLO honor it | never shed `Never` traffic; shed `Always` first |

## 5. Degradation → fallback / cache

| Trait | Implies | Note |
|---|---|---|
| A **safe** degraded value exists (documented) | `WithFallback[T](v)` / `WithFallbackFunc` | NEVER fabricate success for a write |
| Degraded value depends on the error | `WithFallbackFunc(func(error)(T,error))` | discriminate shed sentinels vs real errors |
| Reads dominate, repeated keys | `WithCache[T](cache, keyFn, ttl)` | fresh hit short-circuits the chain |
| Stale data tolerable on failure | `+ StaleIfError(d)` | RFC 5861 stale-on-error |
| Hot key, avoid synchronous miss at expiry | `+ RefreshAhead(d<ttl)` (needs `WithTimeout`) | Caffeine refresh-after-write |
| Failures are cacheable (avoid hammering) | `+ NegativeCache(d)` | repeats fast-fail |
| Miss stampede on a hot key | `+ WithCoalesce(keyFn)` (needs `WithTimeout`) | collapse concurrent misses |
| Data changes frequently | keep `ttl` short, well under the change interval | stale authoritative data is a defect |
| keyFn must distinguish requests | derive the key from request identity in ctx; empty key opts out | a colliding key returns wrong results |

## 6. Operations → observability / config

| Trait | Implies | Note |
|---|---|---|
| Any non-trivial policy | name it (`NewPolicy(name, …)`) and build it once | anonymous `Do` and an empty name have no health visibility |
| Policies built per job / tenant / request | `defer policy.Registry().Unregister(policy)`, an empty name, or a `WithRegistry` registry dropped with them | a named policy stays registered: the registry and its health endpoints grow without bound |
| Failure modes must be visible | `WithHooks(&Hooks{…})` on the patterns present; metrics are automatic | wire hooks to the modes you protect against; hooks run concurrently and may run after `Do` returned, so they must be safe for concurrent use |
| Breaker with a classifier that makes outcomes neutral | `OnCircuitNeutral` hook, or an alert on `CircuitNeutralOutcomes` (`r8e.policy.circuit_neutral_outcomes`) | a neutral outcome reaches neither the failure counter nor the slow-call window: callers abandoning a slow dep only show there |
| OTel shop | `r8eotel.Register(meter, reg)` + `r8eotel.Trace(policy, tp)` | metrics + tracing bridge |
| Dedicated/owned dependency whose loss should pull the pod | `WithReadinessImpact()` | only when NOT fleet-shared |
| Shared dependency | do NOT gate readiness | avoids fleet-wide readiness flip |
| Needs runtime tuning without redeploy | express config-expressible params in `r8econf` JSON | code-only patterns can't hot-reload |
| Want to prove the policy actually catches faults | `WithChaos(…)` + a `ChaosEnabled` kill-switch | never ship chaos without an off switch |

## Assembly checklist (run before the review gate)

1. Idempotency decided first; retry/hedge consistent with it.
2. Every required-companion present (`api-constraints.md`).
3. No bulkhead + adaptive-concurrency together.
4. Permanent errors classified if retrying.
5. Every parameter traces to a service trait (rationale table).
6. Readiness impact only on non-shared deps.
7. Fallback never fabricates a write success.
8. With a breaker: errors that say nothing about the dep's health are neutral
   (`CircuitBreakerClassifier`, local rejections through `IsLocalRejection`), and
   an r8e deadline strictly shorter than the callers' ends a hanging call.
9. With a breaker over a call that may hang without returning:
   `HalfOpenProbeLease` above the worst probe span.
10. Hooks safe for concurrent use; a policy built per job / tenant / request is
    unregistered when done (or unnamed).
