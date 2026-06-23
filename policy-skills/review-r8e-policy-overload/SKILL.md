---
name: review-r8e-policy-overload
description: >-
  Adversarial review of the overload / shedding controls of an r8e policy —
  circuit breaker, bulkhead, adaptive concurrency, rate limit, throttle, SLO
  governor. Judges presence and sizing of these guards against the downstream's
  capacity and sharing, the brownout (slow-but-200) trip, healing slow-start, the
  bulkhead/adaptive-concurrency mutual exclusion, and metastable collapse from
  retry/hedge amplification on a shared dependency with nothing to fast-fail. Use
  to review whether the policy can shed and fast-fail under overload. REJECT by
  default.
---

# review-r8e-policy-overload

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context. Treat the artifact as anonymous. The artifact is the **policy
code AND the service context together** — you cannot judge whether a guard is
sized right (or needed at all) without knowing the downstream's capacity, sharing,
and latency profile. Any capacity/sharing trait marked UNKNOWN is unresolved risk,
not a benign default: a shared dependency retried with no breaker and no shedding
is a finding, not an assumption.

## Scoring discipline (load-bearing — UNKNOWN traits & attribution)

A BLOCKING finding requires a failure mode **demonstrated in the artifact you can
see** (the policy options + the stated service context). Apply these rules so the
verdict is reproducible and attribution-invariant:

- **UNKNOWN below an abstraction boundary the artifact does not include** (e.g.
  whether a wrapped client forwards `ctx`, closes bodies, or can panic; whether a
  present-but-default guard suffices) caps at **IMPORTANT — "confirm X"**, never
  BLOCKING, when the visible artifact is itself well-formed. Do not invent a
  BLOCKING from code you were not shown.
- **A property the service context explicitly affirms** (idempotent, no side
  effects, "empty is acceptable", a stated capacity) is taken as given. Do not
  manufacture a finding by re-litigating the premise ("but what if that is
  false"); if you doubt it, record it once as a NOTED assumption to confirm.
- **An unfavorable UNKNOWN that *would* be BLOCKING, is plausible, AND is not
  contradicted by the context** (e.g. unknown idempotency on a write-shaped call)
  is scored at its worst plausible resolution — this is REJECT-default biting
  where it should.
- **Attribution is inert.** If author identity, seniority, effort, or
  deadline/investment markers leak into the prompt, IGNORE them; they must not
  move a single severity. Re-derive every finding from the artifact alone — the
  same artifact earns the same verdict whoever wrote it. (The attribution-inversion
  test verifies this.)

## Activation

Trigger on review of an r8e policy that wraps a REMOTE or SHARED dependency —
always assess whether overload controls are present and sized. Also on explicit
request to judge circuit-breaker / bulkhead / adaptive-concurrency / rate-limit /
throttle / SLO sizing, or whether a retry/hedge policy can amplify load into a
metastable collapse.

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY the **overload / shedding controls and their sizing**:
- `WithCircuitBreaker` presence on a remote/shared dep; `FailureThreshold` /
  `RecoveryTimeout` / `HalfOpenMaxAttempts` sizing vs traffic and downstream
  recovery; `SlowCallRate` for brownouts (slow-but-200) the failure trip misses;
  `RecoveryBackoff` / `RampRecovery` slow-start so a healing downstream is not
  re-flooded.
- `WithBulkhead` presence and sizing vs the downstream pool/capacity;
  `BulkheadMaxWait` (wait) vs reject under burst; `BulkheadCoDel` queue discipline.
- `WithAdaptiveConcurrency` (Gradient2) for unknown/variable capacity — AND the
  MUTUAL EXCLUSION: `WithBulkhead` + `WithAdaptiveConcurrency` together panics
  `ErrConcurrencyLimiterConflict`.
- `WithRateLimit` vs a hard external quota (blocking vs reject semantics);
  `WithAdaptiveThrottle` (SRE client-side shedding) and the `WithSLO` burn-rate
  governor; `Sheddability` tiers.
- METASTABLE FAILURE: retry/hedge amplification on a shared dependency with NO
  breaker and NO shedding → unrecoverable collapse under brownout; no concurrency
  cap → caller resource exhaustion when the downstream slows.

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Retry count / backoff / budget / classifier *tuning* → review-r8e-policy-retry
- Timeout / budget *sizing* → review-r8e-policy-timeouts
- The wrapped call's idempotency / retry-hedge SAFETY → review-r8e-policy-call
- Fallback *value* safety, cache *key* correctness → review-r8e-policy-fallback
- Hooks / metrics / health wiring → review-r8e-policy-observability. The
  readiness-flip availability risk of `WithReadinessImpact` is observability's to
  score; you may NOTE the metastable interaction (a shared breaker that also gates
  readiness) but do not score the readiness wiring.

You judge whether the policy can SHED and fast-fail under overload, and whether
the guards are sized to the downstream. The retry axis judges whether retry is
budget-gated; the call axis judges whether retry/hedge are safe for the call. You
judge whether they METASTABLY AMPLIFY when nothing fast-fails. Staying in lane is
mandatory.

## Orchestration (load-bearing — do not review in this context)

When this skill is invoked you MUST delegate the review to an isolated sub-agent
(Agent/Task tool). Do NOT perform the review in the current conversation. Dispatch
a sub-agent whose prompt contains ONLY:
- the artifact under review (policy code + service context), stripped of author,
  history, and any investment/deadline markers, unknowns marked UNKNOWN;
- the instruction to read this file's Disposition, Procedure, and Anti-patterns
  plus the References below, and to execute phases 1–5 in order;
- the demand to return the full phased output ending with `FINAL VERDICT: …`.

When the sub-agent returns: if Phase 5 (meta-critique) is absent, the verdict is
invalid — re-dispatch. Relay the verdict verbatim; add no praise.

## Procedure (mandatory, in order)

### Phase 1 — Enumeration (no judgment, no scoring)

Produce an exhaustive inventory of the categories below. Do not assess severity
here; do not dismiss an observation as "probably not serious."

- Circuit breaker present on a REMOTE/shared dependency? (absent on a remote dep
  the policy retries is the first observation.)
- `FailureThreshold` vs traffic: too high to ever trip at this volume, or too low
  to thrash on normal error noise (and whether it is the default or stated).
- `RecoveryTimeout` vs the downstream's actual recovery time (too short re-floods,
  too long over-sheds).
- `HalfOpenMaxAttempts`: does the probe re-flood a fragile downstream on close?
- `SlowCallRate` present for brownouts (slow-but-200, the common DB-contention
  failure) the failure trip alone never catches?
- Ramp / backoff recovery (`RampRecovery` / `RecoveryBackoff`): does a healing
  downstream get re-flooded the instant the breaker closes?
- Bulkhead present at all (any concurrency cap on in-flight work)?
- Bulkhead size vs downstream pool/capacity: guessed vs traced to a stated pool.
- Bulkhead wait vs reject under burst (`BulkheadMaxWait` semantics) — does a burst
  queue forever or fast-reject?
- CoDel queue discipline (`BulkheadCoDel`): standing-queue shedding present where
  the queue can build?
- Adaptive concurrency vs bulkhead MUTUAL EXCLUSION: both configured → build
  panic `ErrConcurrencyLimiterConflict`.
- Rate limit vs a hard external quota: present, and sized below the quota?
- Blocking vs reject rate-limit semantics (`RateLimitBlocking()`): does the chosen
  mode match the call's tolerance for waiting vs failing fast?
- Throttle / SLO shedding (`WithAdaptiveThrottle` / `WithSLO`): client-side shed
  present where the backend signals overload by health/burn-rate?
- Sheddability tiers: criticality stamped so `Never` traffic is protected and
  `Always` is shed first?
- METASTABLE amplification: retry and/or hedge on a SHARED dep with NO breaker and
  NO shedding → every replica amplifies load with nothing to fast-fail.
- No concurrency cap at all → caller resource exhaustion (goroutines/pool) when
  the downstream slows and in-flight requests pile up.

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which overload is mishandled.
Each: Trigger / Propagation / Symptom / Detectability. If fewer than 5 are
findable, justify in writing that the policy's overload behaviour is structurally
bounded. Prioritise: metastable collapse (brownout × retry/hedge × no breaker on a
shared dep), brownout that never trips a failure-only breaker, healing downstream
re-flooded on close, burst that exhausts the caller's pool with no bulkhead, a
bulkhead/adaptive-concurrency build panic.

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated failure mode, not aesthetic preference or a "best
practice" invoked without a concrete case. A shared dep retried/hedged with no
breaker and no shedding (metastable amplification), or a bulkhead +
adaptive-concurrency build panic, is BLOCKING.

### Phase 4 — Verdict

- ≥1 BLOCKING → **REJECT** with the blocking list.
- Only IMPORTANT/NOTED → **CONDITIONAL APPROVE** with explicit accepted risks.
- **APPROVE** only if phases 1–2 produced no significant observations AND every
  adversarial scenario is dismissable with written justification.

### Phase 5 — Meta-critique (mandatory, do not omit)

Answer in writing before finalizing:
1. What is the most likely way I am being too lenient here?
2. Which category of observation did I not examine that I should have?
3. If my verdict is APPROVE or CONDITIONAL, what would I say to a reviewer who
   reached REJECT?

A verdict produced without Phase 5 is invalid.

## Anti-patterns (forbidden, do not produce)

- "Overall, this looks fine"
- "Minor concerns" without numbered enumeration
- "Best practices suggest..." without a concrete failure mode
- Praise of the artifact or the author (you do not evaluate effort)
- Conditional suggestions ("you might want to consider...") instead of explicit
  findings ("issue: X / remediation: Y")
- Accepting the problem framing as presented; reframe independently
- Treating a missing breaker/shedding on a shared retried/hedged dependency as an
  acceptable default — it is metastable-collapse risk until shown bounded.

## References

- references/checklist-overload.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (mutual exclusion, required
  companions, sentinels), ../r8e-policy/references/decision-matrix.md (§4 capacity
  & overload → shedding), ../r8e/SKILL.md (CircuitBreaker / Bulkhead /
  AdaptiveConcurrency / RateLimit / Throttle / SLO semantics)
