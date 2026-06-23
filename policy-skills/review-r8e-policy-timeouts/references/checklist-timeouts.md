# Checklist — timeouts / deadlines / latency axis

Per-category checks for the call's deadline and latency budget. Each check is
phrased so a "yes" to the *hazard* is an observation to score. Enumerate; do not
pre-dismiss.

## Hard-deadline presence (the first gate)
- [ ] Is there a `WithTimeout` bounding the call at all?
- [ ] Are `WithRetry` and/or `WithHedge` present? If so and there is NO hard
      deadline, the call's latency is unbounded — re-execution and stragglers
      stack with no ceiling.
- [ ] Is the path latency-critical (checkout / synchronous request / SLA-bound)?
      Unbounded latency there is a user-facing incident.

## WithTimeout sizing vs latency profile
- [ ] Is `WithTimeout` ≈ p99 × 2–4 headroom? (decision-matrix range)
- [ ] Is it far TIGHTER than p99 (e.g. < p99)? → false-timeout storm, a healthy
      slow-tail call is killed.
- [ ] Is it far LOOSER than p99 (e.g. ≫ p99 × 4)? → exists on paper but a single
      slow call runs almost unbounded; no real protection.
- [ ] Is the p99 STATED? If UNKNOWN, the sizing cannot be judged — observation.

## WithTimeout vs the stated SLA / budget
- [ ] Does the per-call ceiling fit INSIDE the upstream SLA / request budget?
      (A 2s timeout inside a 500ms SLA is nonsense — one slow call blows the SLA.)
- [ ] If `WithTimeout` ≈ the budget, does it cap the WHOLE call (retries + hedge),
      or only one attempt?

## Worst-case retry chain vs the budget
- [ ] Compute `WithRetry(n)` × per-attempt deadline (+ backoff waits). Does the
      worst-case chain exceed the budget? (3 × 2s ≈ 6s vs a 500ms SLA blows it.)
- [ ] Does `MaxDelay`/backoff add latency the chain math must include?
- [ ] Is the per-attempt window the FULL `WithTimeout`, so each retry can take the
      whole ceiling (total = n × ceiling)?

## Sum-cap (WithTimeBudget) vs per-attempt
- [ ] On an SLA-bound retry/hedge path, is there a `WithTimeBudget(d)` capping the
      SUM of attempts? Without it, the total can run past the budget even if each
      attempt is within `WithTimeout`.
- [ ] Is the deadline a per-attempt `PerAttemptTimeout` (each attempt gets the
      full window) when a TOTAL cap is what the budget requires — or vice versa?
- [ ] `WithTimeBudget` present but NO `WithRetry`/`WithHedge` consumer? →
      `NewPolicy` panics `ErrTimeBudgetWithoutConsumer`.

## Adaptive deadlines (variable latency)
- [ ] Is the latency distribution wide / variable / unknown? A static ceiling is
      then either always too tight or always too loose → consider `AdaptiveTimeout`.
- [ ] `AdaptiveTimeout` present without `WithTimeout`? → `ErrAdaptiveTimeoutWithoutTimeout`.

## Hedge delay sizing
- [ ] Is `WithHedge(delay)` near p95 on a heavy right tail (race only genuine
      stragglers)?
- [ ] Is the delay ≪ p50? Then the hedge fires on the MAJORITY of calls → load
      doubling, not tail-trimming.
- [ ] Is the delay ≫ p99? Then it never helps.
- [ ] Latency profile drifts over time → consider `AdaptiveHedge`. Present without
      `WithHedge`? → `ErrAdaptiveHedgeWithoutHedge`.

## Deadline propagation (fan-out & ingress)
- [ ] Does this call fan out to further callees? Then `WithTimeBudget(d,
      PropagateDeadline())` (+ `httpx.InjectDeadline` for HTTP) so the downstream
      stops when our budget is spent.
- [ ] Is this an ingress handler with an inbound ctx deadline? Then
      `WithTimeBudget(d, RespectInboundDeadline())` so it never runs past the
      caller's deadline (smallest deadline wins).
- [ ] `PropagateDeadline` or `RespectInboundDeadline` present without
      `WithTimeBudget`? → `ErrDeadlinePropagationWithoutBudget` /
      `ErrInboundDeadlineWithoutBudget`.
- [ ] For HTTP, is `InjectDeadline`/`ExtractDeadline` wired so the relative-ms
      header (`X-R8e-Timeout-Ms`) actually crosses the wire?

## Required-companion timeouts (omitting the companion is a build error)
- [ ] `WithCoalesce` present WITHOUT `WithTimeout`? → panic
      `ErrCoalesceWithoutTimeout` (the detached coalesced call would be unbounded).
- [ ] `WithCache` + `RefreshAhead(d<ttl)` WITHOUT `WithTimeout`? → panic
      `ErrRefreshAheadWithoutTimeout` (the detached reload would be unbounded).

## Nested guards
- [ ] Does the call have its OWN timeout (e.g. `http.Client.Timeout`) shorter than
      `WithTimeout`, so the policy deadline never bites — or longer, so the client
      fires first and the policy knob is inert?

## Red lines (any one is BLOCKING on its own)
- Retry and/or hedge present with NO hard deadline bounding the call (unbounded
  latency), especially on a critical path.
- A required-companion timeout missing so `NewPolicy` panics (build failure) —
  `WithCoalesce`/`RefreshAhead` without `WithTimeout`.
- A per-call timeout that cannot bound the worst-case retry chain on a
  latency-critical path.
