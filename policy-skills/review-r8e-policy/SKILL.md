---
name: review-r8e-policy
description: >-
  Orchestrated multi-axis adversarial review of an r8e resilience policy. Selects
  the applicable per-axis reviewers (call/Do, timeouts, retry, overload,
  fallback/cache, observability), fans them out CONCURRENTLY as isolated
  sub-agents, collects each verdict, and arbitrates one global verdict
  (worst-verdict-wins). Use to review/audit an r8e NewPolicy / Do() call /
  r8econf PolicyConfig against the service it protects. Default global verdict:
  REJECT.
---

# review-r8e-policy

Orchestrator for the r8e-policy reviewer fleet. This skill is NOT a per-axis
reviewer: it does not itself enumerate 15 observations, run 5 adversarial
scenarios, or judge the policy. It SELECTS the applicable axes, FANS them out
concurrently as isolated sub-agents, and ARBITRATES one global verdict.

## Disposition (load-bearing — do not soften)

Default GLOBAL verdict: REJECT. The aggregate APPROVE must be EARNED by EVERY
applicable reviewer independently approving — never assumed, never averaged into
existence.

You orchestrate; you do not re-judge the policy yourself, and you do not
override, downgrade, or drop any reviewer's finding. A reviewer owns its axis;
its verdict and its BLOCKING/IMPORTANT/NOTED items pass through to the aggregate
verbatim and attributed.

The artifact is anonymous. Strip author, commit history, and any
investment/deadline markers BEFORE fan-out. You do not receive (and do not pass
on) the author's identity, the discussion history, or the production context.
You are not evaluating effort, intent, or investment.

**The artifact is the policy code AND the service context together.** A policy
cannot be judged without the call it wraps. If the service context (idempotency,
latency, error taxonomy, criticality, sharing) is missing, that is itself the
first finding: dispatch with the context marked UNKNOWN and require each reviewer
to treat unknowns as unresolved risk, not as benign.

## Activation

Trigger when asked to review/audit an r8e policy comprehensively — a `NewPolicy`
composition, a `Do()` call site, or an `r8econf` `PolicyConfig` — i.e. not a
single named axis.

If the user asks for ONE axis, do NOT orchestrate — invoke that reviewer directly:

| User asks about… | Invoke directly |
|---|---|
| the wrapped call / Do body / idempotency / retry-safety | review-r8e-policy-call |
| timeouts / deadlines / latency / budget | review-r8e-policy-timeouts |
| retries / backoff / budgets / classification | review-r8e-policy-retry |
| circuit breaker / bulkhead / rate limit / shedding / overload | review-r8e-policy-overload |
| fallback / cache / coalesce / stale data | review-r8e-policy-fallback |
| hooks / metrics / health / readiness / config | review-r8e-policy-observability |

## Procedure (mandatory, in order)

1. **Identify & anonymise.** Resolve the artifact: the policy + the service
   context. Strip author, history, investment/deadline markers. Mark any missing
   service trait as UNKNOWN (do not infer it away).

2. **Select reviewers** per the Selection rule below. For EACH axis, record WHY
   it is in scope (which signal fired or why it is baseline) and the patterns it
   will find present/absent. Coverage is never silently dropped — every axis is
   either run or explicitly skipped with a written structural reason.

3. **Fan out CONCURRENTLY.** Dispatch the selected reviewers as PARALLEL isolated
   sub-agents — ALL in ONE batch (multiple Agent/Task calls in a single message),
   one sub-agent per axis. Each sub-agent's prompt contains ONLY:
   - the anonymised artifact (policy code + service context, unknowns marked);
   - the instruction to act as `review-r8e-policy-<axis>` by reading
     `../review-r8e-policy-<axis>/SKILL.md` (its Disposition, Procedure phases
     1–5, Anti-patterns) and that reviewer's `references/`;
   - the demand to execute phases 1–5 in order and to END with a single line:
     `FINAL VERDICT: <REJECT|CONDITIONAL APPROVE|APPROVE>`.

   Pass NO author, NO history, NO deadline/investment context. Do not review in
   this orchestrating context — judgment happens inside the sub-agents.

4. **Collect & validate.** Each returned review MUST contain Phase 5
   (meta-critique) AND a `FINAL VERDICT:` line. A review missing either is
   INVALID — re-dispatch that single reviewer. Do not proceed to arbitration with
   an invalid or missing verdict, and do not substitute your own judgment.

5. **Arbitrate** per the Arbitration rules (worst-verdict-wins).

6. **Produce the consolidated report** per the Report format.

## Selection rule

Because the **absence** of a pattern is a valid finding (no retry budget on a
shared dependency; no timeout bounding a retry; no fallback where one is needed),
the six axes are **baseline-included** by default. Signals do not gate inclusion;
they mark which axis is the *primary focus*. Skip an axis ONLY for a written
structural reason (e.g. the artifact is a standalone primitive with no `Do`, not
a policy).

| Signal in the artifact | Axis raised to primary focus |
|---|---|
| a `Do(…)` body / described downstream call (always present) | review-r8e-policy-call (always) |
| any `WithTimeout` / `WithTimeBudget` / latency numbers / a deadline | review-r8e-policy-timeouts (always baseline) |
| `WithRetry` / `WithHedge` / a shared dependency / error taxonomy | review-r8e-policy-retry (always baseline) |
| `WithCircuitBreaker` / `WithBulkhead` / `WithRateLimit` / `WithSLO` / capacity numbers | review-r8e-policy-overload (always baseline) |
| `WithFallback` / `WithCache` / `WithCoalesce` / degraded-value question | review-r8e-policy-fallback (always baseline) |
| `WithHooks` / naming / `WithReadinessImpact` / `r8econf` / OTel | review-r8e-policy-observability (always baseline) |

### Inclusion rule under uncertainty

When unsure whether an axis applies, INCLUDE it. A reviewer with nothing in its
lane returns a "nothing in scope (trivial)" verdict — one sub-agent, no downstream
effect. The false-negative cost (a whole dimension of the policy unreviewed, a
double-charge ships) is unbounded. Bias toward inclusion.

## Arbitration (worst-verdict-wins — load-bearing)

- **GLOBAL REJECT** if ANY applicable reviewer returns REJECT (≥1 BLOCKING in any
  axis). List every blocking item, attributed to its axis.
- **GLOBAL CONDITIONAL APPROVE** if no reviewer REJECTs but ≥1 returns CONDITIONAL
  APPROVE. The accepted-risks list is the UNION of every reviewer's IMPORTANT
  findings / accepted risks, attributed.
- **GLOBAL APPROVE** only if EVERY applicable reviewer returns APPROVE.
- Never let a NOTED or "out of scope" downgrade a BLOCKING. Never average — the
  worst single verdict governs. A split (one REJECT, five APPROVE) is a GLOBAL
  REJECT; the orchestrator does not reconcile disagreement into something milder.

## Report format

```
# Multi-axis r8e policy review — <policy name / target service>

## Axes run
<axis> — in scope because <signal/baseline>; finds <patterns present/absent>   (one line each)
Skipped: <axis> — <structural reason> (rare)

## Per-axis verdicts
| Axis | Verdict | BLOCKING | IMPORTANT |
| ... |

## Consolidated findings (by severity, then axis)
### BLOCKING
- [<axis>] <finding> — remediation: <...>
### IMPORTANT
- [<axis>] <finding>
### NOTED (incl. out-of-scope cross-references)
- [<axis>] <finding>

## GLOBAL VERDICT: <REJECT | CONDITIONAL APPROVE | APPROVE>
<REJECT: the consolidated blocking list. CONDITIONAL: the union of accepted risks.
APPROVE: confirmation that every applicable axis approved.>

## Aggregate meta-check (mandatory)
1. Did I drop, merge-away, or downgrade any reviewer's BLOCKING/IMPORTANT? (must be "no")
2. Did every applicable axis actually run and return a Phase-5 verdict?
3. Is a global APPROVE/CONDITIONAL truly earned, or am I being lenient to reconcile disagreement?
4. Was every UNKNOWN service trait carried as unresolved risk, not assumed benign?
```

## Anti-patterns (forbidden — do not soften the aggregate)

- Overriding or downgrading a reviewer's REJECT/BLOCKING to reconcile a split.
- Averaging verdicts, or declaring APPROVE while any CONDITIONAL/REJECT stands.
- Dropping an axis that "probably doesn't apply" without a written structural reason.
- Re-judging the policy yourself instead of relaying the reviewers' findings.
- Reviewing the policy without its service context (judging the wrapper blind).
- Praise of the policy or the author (you do not evaluate effort).
- Passing author / history / deadline context into a sub-agent prompt.
- Proceeding to arbitration with a review missing Phase 5 or its FINAL VERDICT line.

## References

- The 6 per-axis reviewers:
  [call](../review-r8e-policy-call/SKILL.md),
  [timeouts](../review-r8e-policy-timeouts/SKILL.md),
  [retry](../review-r8e-policy-retry/SKILL.md),
  [overload](../review-r8e-policy-overload/SKILL.md),
  [fallback](../review-r8e-policy-fallback/SKILL.md),
  [observability](../review-r8e-policy-observability/SKILL.md)
- The authoring skill (canonical rules the reviewers cite):
  [../r8e-policy/SKILL.md](../r8e-policy/SKILL.md) and its
  `references/api-constraints.md` + `references/decision-matrix.md`.
- Graded calibration artifacts: `../fixtures/{good,mediocre,poor}.md`.
