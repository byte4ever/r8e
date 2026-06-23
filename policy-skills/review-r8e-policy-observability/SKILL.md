---
name: review-r8e-policy-observability
description: >-
  Adversarial review of an r8e policy's observability and operability — whether
  the policy is NAMED (registered for health), whether WithHooks is wired to the
  failure modes it actually has, the READINESS IMPACT (and the fleet-wide flip
  risk of gating a shared dependency), config-vs-code & hot-reload reach, OTel
  wiring, and the chaos kill-switch. Use to review whether a policy can be SEEN
  and OPERATED in production. REJECT by default.
---

# review-r8e-policy-observability

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context beyond the artifact. Treat the artifact as anonymous. The
artifact is the **policy code AND the service context together** — you cannot
judge observability or readiness impact without knowing what the policy protects
and whether the dependency is shared. Any service trait marked UNKNOWN (is the
dependency shared or dedicated? do operators retune at runtime? is this a
production policy?) is unresolved risk, not a benign default: a `WithReadinessImpact()`
on a dependency of unknown sharing is a finding, not an assumption.

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

Trigger on review of an r8e policy whenever its operability is in question:
naming/registration, hooks, readiness impact, config-vs-code reach, OTel, or
chaos kill-switch. Also on explicit request to judge whether a policy is
observable and operable in production.

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY whether the policy is **observable and operable**:
is the policy NAMED (so `NewPolicy(name, …)` auto-registers it for health — an
anonymous `r8e.Do(…)` has NO health visibility)?; are `WithHooks` wired to the
failure modes the policy actually has (a breaker/retry/fallback with no
`OnCircuitOpen`/`OnRetry`/`OnFallbackUsed` is blind to its own events — note that
metrics counters are collected AUTOMATICALLY even with no hooks, so "no metrics
at all" is the WRONG framing; the real gap is hooks→logging/alerting)?; the
**readiness impact** — `WithReadinessImpact()` gates `/readyz`; gating a SHARED
dependency causes a FLEET-WIDE readiness flip (every replica trips together → the
whole service goes NotReady at once, a self-inflicted availability incident),
whereas a DEDICATED dependency whose loss should pull the pod SHOULD gate;
config-vs-code reach — which chosen patterns are code-only (`WithCoalesce`,
`WithCache`, `WithChaos`, classifiers, `WithFallbackFunc`, budget links) and thus
NOT hot-reloadable / silently absent from emitted `r8econf` JSON, and whether
operators expect to retune them at runtime (`Reconfigure` cannot add/remove a
pattern); OTel wiring (`r8eotel.Register` / `r8eotel.Trace`); whether `WithChaos`
shipped WITHOUT a `ChaosEnabled` kill-switch.

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Retry count / backoff / budget / classifier *tuning* → review-r8e-policy-retry
- Timeout / budget *sizing* → review-r8e-policy-timeouts
- Circuit breaker / bulkhead / rate-limit *sizing* → review-r8e-policy-overload
- Fallback *value* safety, cache *key* correctness → review-r8e-policy-fallback
- Idempotency / retry-hedge / ctx / cleanup safety of the wrapped call →
  review-r8e-policy-call

You judge whether the policy is OBSERVABLE and OPERABLE, not whether its
parameters are right. The *presence* of a breaker without a hook is your concern;
its `FailureThreshold` is the overload axis's. Staying in lane is mandatory.

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

- Is the policy NAMED (`NewPolicy(name, …)`) so it auto-registers for health, or
  anonymous?
- If the name is empty `""` or a placeholder → effectively anonymous, no
  meaningful health identity in the registry.
- Anonymous `r8e.Do(…)` path: no registration at all → no health visibility.
- Which failure modes does the policy actually HAVE (breaker / retry / hedge /
  fallback / bulkhead / rate-limit / SLO / throttle / cache)?
- For each present failure mode, is the matching hook wired
  (`OnCircuitOpen`/`OnRetry`/`OnHedge`/`OnFallbackUsed`/`OnBulkheadRejected`/… )?
- Is `WithHooks` present at all? (Absent → no event surfacing to logs/alerting.)
- Metrics counters are collected AUTOMATICALLY even without hooks — record this
  (so the gap is hooks→alerting, NOT "no metrics").
- Is `WithReadinessImpact()` present?
- If present, is the gated dependency SHARED (third-party / fleet-common) or
  DEDICATED (owned, loss should pull this pod)?
- Fleet-wide flip risk: a shared dep gated → every replica flips `/readyz`
  together on a single downstream blip.
- Conversely: a dedicated dep whose loss should pull the pod is NOT gated →
  the pod stays Ready while broken (a different, opposite miss).
- Are the health conditions the policy can surface (`ErrCircuitOpen`,
  `ErrSLOShed`, …) actually reachable by an operator (named registry)?
- OTel: is `r8eotel.Register(meter, reg)` / `r8eotel.Trace(policy, tp)` present
  where the context says an OTel/metrics stack exists?
- Config-expressible params: are the retunable knobs reachable via `r8econf`
  JSON / `Reconfigure` for runtime tuning without redeploy?
- Code-only patterns present (`WithCoalesce`/`WithCache`/`WithChaos`/classifiers/
  `WithFallbackFunc`/budget links) → silently ABSENT from emitted JSON; say so.
- `Reconfigure` cannot ADD or REMOVE a pattern — only retune existing ones
  (configuring an absent pattern → `ErrPatternAbsent`).
- `WithChaos` present without a `ChaosEnabled` kill-switch in a production policy.
- Of the failure modes that MATTER operationally (the ones that page someone),
  which are alertable today and which are silent?

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which the policy is operationally
blind or mis-operated. Each: Trigger / Propagation / Symptom / Detectability. If
fewer than 5 are findable, justify in writing that the policy's operability gaps
are structurally bounded. Prioritise: shared-dep readiness gating (fleet-wide
`/readyz` flip → mass outage), a present failure mode with no hook (event
invisible to alerting), an empty/anonymous name (no health identity), a code-only
pattern an operator tries to retune via JSON (silently absent), chaos shipped with
no kill-switch (cannot disable an injected fault in prod).

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated operational failure mode, not aesthetic preference or a
"best practice" invoked without a concrete case. `WithReadinessImpact()` on a
shared/third-party dependency (fleet-wide flip), or shipping `WithChaos` with no
`ChaosEnabled` kill-switch in production, is BLOCKING on its own.

### Phase 4 — Verdict

- ≥1 BLOCKING → **REJECT** with the blocking list.
- Only IMPORTANT/NOTED → **CONDITIONAL APPROVE** with explicit accepted risks.
- **APPROVE** only if phases 1–2 produced no significant observation AND every
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
- Claiming "no metrics" when a present pattern lacks a hook — metrics are
  automatic; the gap is hooks→alerting, name it precisely

## References

- references/checklist-observability.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (health & readiness, code-only vs
  config-expressible, Reconfigure limits), ../r8e-policy/references/decision-matrix.md
  (operations → observability/config row), ../r8e/SKILL.md (Hooks, readiness,
  r8eotel, chaos kill-switch semantics)
