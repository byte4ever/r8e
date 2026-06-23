---
name: review-r8e-policy-fallback
description: >-
  Adversarial review of the fallback and cache layer of an r8e policy — the
  degraded VALUE a policy substitutes and the cached/coalesced result it serves.
  Judges fallback value safety (fabricated write success, nil-deref values,
  sentinel discrimination), cache/coalesce key correctness (collisions, empty
  keys), ttl vs change interval, stale-if-error, negative cache, and refresh-ahead.
  Use to review whether the degraded values and cached results a policy returns are
  SAFE and CORRECT. REJECT by default.
---

# review-r8e-policy-fallback

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context. Treat the artifact as anonymous. The artifact is the **policy
code AND the service context together** — you cannot judge a fallback value or a
cache key without knowing what the call does, what it returns, and what the caller
derefs. Any service trait marked UNKNOWN is unresolved risk, not a benign default:
a fallback whose semantic correctness is unstated, or a keyFn whose request
identity is unstated, is a finding, not an assumption.

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

Trigger on review of an r8e policy where `WithFallback` / `WithFallbackFunc`,
`WithCache`, or `WithCoalesce` is present (or conspicuously absent on a hot read
key). Also on explicit request to judge whether the degraded value a policy
substitutes is safe to consume and whether its cache/coalesce keys identify
requests correctly.

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY the **degraded-value and cached-result layer**:
- `WithFallback` / `WithFallbackFunc` VALUE safety — does the fallback fabricate a
  "success" for a failed WRITE (masking a real failure the caller must see)? is
  the static value safe to consume (a nil pointer the caller derefs vs a safe
  empty slice)? does a `FallbackFunc` discriminate the shed sentinels
  (`ErrCircuitOpen`/`ErrRateLimited`/…) from genuine downstream errors when its
  degraded value is only appropriate for one of them? is the value semantically
  correct as an *authoritative* result?
- `WithCache` correctness — keyFn collisions (distinct requests sharing a key →
  WRONG results returned to the wrong caller), empty/constant keys (opts out /
  pointless), ttl vs the data's change interval (a long ttl on changing data
  serves STALE data as authoritative), `StaleIfError(d)` appropriateness,
  `NegativeCache(d)`, `RefreshAhead(d)` (and that it REQUIRES `WithTimeout` as a
  correctness companion).
- The invariant that r8e NEVER caches a fallback value (only a genuine success) —
  confirm any reliance on it is correct.
- `WithCoalesce` key correctness (collapsing DISTINCT requests is a correctness
  bug) and its `WithTimeout` requirement (a correctness companion). Miss stampede
  on a hot key with no coalescing.

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Whether retry/hedge is SAFE for the call (idempotency, ctx, cleanup) →
  review-r8e-policy-call
- Timeout / budget *sizing* (you only flag the `WithTimeout` REQUIREMENT for
  coalesce / refresh-ahead as a correctness companion) → review-r8e-policy-timeouts
- Retry count / backoff / budget / classifier *tuning* → review-r8e-policy-retry
- Circuit breaker / bulkhead / rate-limit *sizing* → review-r8e-policy-overload
- Hooks / metrics / health wiring → review-r8e-policy-observability

You judge whether the degraded VALUE and the cache/coalesce KEY are safe and
correct; the other axes judge the protective machinery around them. Staying in
lane is mandatory.

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

- Fallback present? `WithFallback` / `WithFallbackFunc` configured or absent.
- Fallback fabricates write success: does the fallback value present a
  *successful* result for a non-idempotent WRITE that actually FAILED (the caller
  believes the side effect happened)?
- Static fallback value safe to consume: is the value a nil pointer / nil map the
  caller will deref or index (nil-panic), or a safe empty (empty slice safe to
  range, zero struct the caller tolerates)?
- `FallbackFunc` discriminates shed sentinels vs real errors: when the degraded
  value is only appropriate for shedding (or only for genuine failure), does the
  func test `errors.Is(err, ErrCircuitOpen/ErrRateLimited/…)` rather than firing
  blindly on every error?
- Fallback semantically correct as an authoritative result: would a caller
  treating the value as truth be misled (a fabricated "ok", a stale balance, a
  default that hides a real distinction)?
- Cache present? `WithCache` configured or absent on a read-dominated repeated-key
  call.
- keyFn collision risk: can two DISTINCT requests (different id / tenant / user /
  query) map to the SAME key → one caller's result served to another?
- Empty / constant key: does keyFn return `""` or a fixed string (opts every call
  out of caching, or — worse if non-empty-and-constant — collapses everyone onto
  one entry)?
- ttl vs change interval: is the ttl long relative to how often the data changes,
  so the cache serves STALE data as authoritative past the point it changed?
- `StaleIfError(d)` window: present? appropriate to the data's tolerance for
  staleness on failure (RFC 5861)?
- `NegativeCache(d)`: are failures cached, and is caching them correct (a cached
  404 vs a cached transient that should not stick)?
- `RefreshAhead(d)` present + requires `WithTimeout`: is `RefreshAhead(d<ttl)`
  used, and is `WithTimeout` present (its required companion — absence is a
  build-time panic `ErrRefreshAheadWithoutTimeout`)?
- Coalesce key correctness: does the `WithCoalesce` keyFn collapse only
  *equivalent* requests, or could it merge DISTINCT requests (returning one
  caller's result to another)?
- Coalesce requires `WithTimeout`: is `WithCoalesce` present without `WithTimeout`
  (build-time panic `ErrCoalesceWithoutTimeout`)?
- Never-cache-a-fallback reliance: does the design rely on r8e NOT caching the
  fallback value (only genuine successes)? Confirm the reliance is correct.
- Miss stampede: is there a hot key with no `WithCoalesce`, so a single miss lets
  N concurrent callers all hit the origin at once?

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which the fallback or cache layer
produces a wrong or unsafe result. Each: Trigger / Propagation / Symptom /
Detectability. If fewer than 5 are findable, justify in writing that the value /
key surface is structurally bounded. Prioritise: fallback fabricates a write
success (caller believes the effect happened), nil fallback value the caller
derefs (nil-panic), keyFn collision serving one caller's data to another, ttl too
long serving stale data as authoritative, `FallbackFunc` firing its degraded value
on a genuine downstream error it should surface.

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated failure mode, not aesthetic preference or a "best
practice" invoked without a concrete case. A fallback that fabricates a successful
result for a failed non-idempotent WRITE, or a cache/coalesce keyFn that COLLIDES
distinct requests and returns one caller's data to another, is BLOCKING.

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
- Treating a fallback value's safety, or a keyFn's request-identity, as fine by
  default when the service context leaves it UNKNOWN.

## References

- references/checklist-fallback.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (never-cache-a-fallback, the
  coalesce/refresh-ahead WithTimeout companions, cache nil/ttl panics, shed
  sentinels), ../r8e-policy/references/decision-matrix.md (section 5 degradation →
  fallback/cache), ../r8e/SKILL.md (Fallback, FallbackFunc, Cache, Coalesce
  semantics)
