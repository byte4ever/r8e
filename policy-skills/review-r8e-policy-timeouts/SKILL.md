---
name: review-r8e-policy-timeouts
description: >-
  Adversarial review of the timeouts / deadlines / latency budget of an r8e
  policy. Judges whether a hard deadline bounds the call, whether WithTimeout is
  sized against downstream p50/p99 and the stated SLA, whether the retry+hedge
  SUM is capped (WithTimeBudget vs per-attempt), deadline propagation
  (PropagateDeadline / RespectInboundDeadline / httpx header) for fan-out and
  ingress, AdaptiveTimeout / AdaptiveHedge for variable latency, and the
  required-companion timeout rules whose absence panics NewPolicy. Use to review
  whether the call's latency is actually bounded. REJECT by default.
---

# review-r8e-policy-timeouts

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context. Treat the artifact as anonymous. The artifact is the **policy
code AND the service context together** — you cannot judge whether a deadline is
sized correctly without the downstream latency profile, the stated SLA/budget,
and the call's criticality. Any latency trait marked UNKNOWN is unresolved risk,
not a benign default: a retry+hedge policy with no stated p99 and no hard
`WithTimeout` is a finding, not an assumption that "it is probably fast enough."

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

Trigger on review of an r8e policy whenever latency must be bounded — i.e. any
policy with `WithRetry`, `WithHedge`, `WithCoalesce`, `WithCache`+`RefreshAhead`,
or a call on a latency-critical / SLA-bound path. Also on explicit request to
judge whether the call's deadline, time budget, hedge delay, or deadline
propagation are correctly sized.

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY **whether the call's latency is bounded and the
deadline/budget knobs are sized for the service**: presence/absence of a hard
deadline (`WithTimeout`) bounding the call; `WithTimeout` sized vs downstream
p50/p99 (too tight → false timeouts; too loose → no real protection, blows the
upstream SLA); the retry×attempt-timeout worst-case latency vs the budget;
`WithTimeBudget` capping the retry+hedge SUM vs a per-attempt `PerAttemptTimeout`;
deadline propagation (`PropagateDeadline` + `httpx.InjectDeadline`) for fan-out;
`RespectInboundDeadline` for ingress; `AdaptiveTimeout` for variable latency;
the hedge DELAY sized vs p50/p95 (`AdaptiveHedge`); and the required-companion
timeout rules whose absence panics `NewPolicy` (`WithCoalesce` and
`RefreshAhead` REQUIRE `WithTimeout`; `WithTimeBudget` requires a retry/hedge
consumer; `PropagateDeadline`/`RespectInboundDeadline` require `WithTimeBudget`).

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Retry COUNT / backoff / budget / classifier *tuning* → review-r8e-policy-retry
- Circuit breaker / bulkhead / rate-limit *sizing* → review-r8e-policy-overload
- The CALL ignoring `ctx` (the ctx-honoring defect itself) → review-r8e-policy-call
  (you MAY note that an ignored ctx makes your timeout unable to cancel in-flight
  work — only stop *waiting* — but the defect is the call axis's to score)
- Fallback *value* safety, cache *key* correctness → review-r8e-policy-fallback
- Hooks / metrics / health wiring → review-r8e-policy-observability

You judge whether the call's latency is BOUNDED and the deadlines are SIZED for
the service; the call axis judges whether the call can actually be cancelled, the
retry/overload axes judge their own knobs. Staying in lane is mandatory.

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

- Hard-deadline presence: is there a `WithTimeout` bounding the call at all? With
  retry and/or hedge present and NO hard deadline, latency is unbounded.
- `WithTimeout` vs p99: is the ceiling ≈ p99 × 2–4 headroom, or far tighter
  (false-timeout storm) or far looser (no real protection)?
- `WithTimeout` vs the stated SLA/budget: does the per-call ceiling fit inside the
  upstream SLA (a 2s timeout inside a 500ms SLA is nonsense)?
- Retry × attempt-timeout worst case: `WithRetry(n)` × the per-attempt deadline =
  worst-case chain latency. Does it exceed the budget? (3 × 2s = ~6s vs a 500ms
  SLA blows it.)
- `WithTimeBudget` present where a sum-cap is needed: a retry/hedge policy on an
  SLA-bound path with no `WithTimeBudget` lets the SUM run past the budget even if
  each attempt is fine.
- Per-attempt vs total: is the deadline a per-attempt `PerAttemptTimeout` (each
  attempt gets the full window) or a total cap? They bound different things.
- `AdaptiveTimeout` where latency is variable: a static ceiling on a service with
  a wide/unknown latency distribution leaves the timeout either always too tight
  or always too loose.
- Hedge delay vs p50/p95: `WithHedge(delay)` should fire near p95 on a heavy right
  tail; a delay ≪ p50 fires the hedge on the *majority* of calls (load doubling),
  a delay ≫ p99 never helps.
- `AdaptiveHedge` where the latency profile drifts: a static hedge delay degrades
  as the distribution shifts.
- `PropagateDeadline` for fan-out: does this call fan out to further callees that
  should stop when our budget is spent? Is `httpx.InjectDeadline` wired for HTTP?
- `RespectInboundDeadline` for ingress: is this an ingress handler that must never
  run past the caller's already-set deadline?
- httpx deadline header for HTTP fan-out: is `InjectDeadline`/`ExtractDeadline`
  present so the relative-ms header crosses the wire?
- Required-companion timeout missing → build panic: `WithCoalesce` or
  `RefreshAhead(d<ttl)` present without `WithTimeout` → `NewPolicy` panics
  (`ErrCoalesceWithoutTimeout` / `ErrRefreshAheadWithoutTimeout`); `WithTimeBudget`
  with no retry/hedge consumer → `ErrTimeBudgetWithoutConsumer`;
  `PropagateDeadline`/`RespectInboundDeadline` without `WithTimeBudget`;
  `AdaptiveTimeout` without `WithTimeout`; `AdaptiveHedge` without `WithHedge`.
- Nested client timeout fighting the policy: an `http.Client.Timeout` shorter or
  longer than `WithTimeout` so the policy's deadline never bites or fires first.
- A timeout so loose it never fires: a ceiling far above p99 that exists on paper
  but provides no real bound under the worst tail.
- No deadline at all on a path that needs one.

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which the call's latency
escapes its budget or the policy fails to build. Each: Trigger / Propagation /
Symptom / Detectability. If fewer than 5 are findable, justify in writing that
the latency surface is structurally bounded. Prioritise: retry/hedge with no hard
deadline (unbounded chain on a critical path), worst-case retry chain exceeding
the SLA, a `WithTimeout` so loose a single slow call blows the SLA, a hedge delay
below p50 doubling load, a required-companion timeout missing so `NewPolicy`
panics at boot.

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated failure mode, not aesthetic preference or a "best
practice" invoked without a concrete case. Retry and/or hedge with NO hard
deadline on a critical path, a worst-case retry chain that cannot fit the budget
on a latency-critical path, or a missing required-companion timeout that panics
`NewPolicy`, is BLOCKING.

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
- Treating an absent or UNKNOWN p99/SLA as "probably fast enough" and waving
  through a missing or unsized deadline

## References

- references/checklist-timeouts.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (required-companion timeout panics,
  ordering), ../r8e-policy/references/decision-matrix.md (latency profile →
  deadlines), ../r8e/SKILL.md (WithTimeout / WithTimeBudget / AdaptiveTimeout /
  AdaptiveHedge / PropagateDeadline / RespectInboundDeadline semantics)
