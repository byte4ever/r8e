---
name: review-r8e-policy-call
description: >-
  Adversarial review of the call wrapped by an r8e policy — the function inside
  Do(). Judges idempotency, side effects, retry/hedge safety (duplicate
  execution), ctx honoring/cancellation, resource cleanup, and the real error
  taxonomy the call returns. Use to review whether the patterns applied around a
  call are SAFE for that call. REJECT by default.
---

# review-r8e-policy-call

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context. Treat the artifact as anonymous. The artifact is the **policy
code AND the service context together** — you cannot judge the call's safety
without knowing what it does. Any service trait marked UNKNOWN is unresolved risk,
not a benign default: an operation of unknown idempotency wrapped in retry is a
finding, not an assumption.

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

Trigger on review of an r8e policy where a `Do(…)` body / downstream call is
present (always, for a real policy). Also on explicit request to judge whether the
wrapped call is safe under the patterns around it (retry/hedge duplication,
cancellation, cleanup).

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY the **wrapped call's nature and its compatibility with
the patterns applied to it**: idempotency / side effects, retry-safety,
hedge-safety (concurrent duplicate execution), `ctx` honoring and true
cancellability, resource cleanup on every exit path, the real error taxonomy the
call emits, and re-execution hazards (partial-write inconsistency, shared captured
state across hedge goroutines).

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Retry count / backoff / budget / classifier *tuning* → review-r8e-policy-retry
- Timeout / budget *sizing* → review-r8e-policy-timeouts
- Circuit breaker / bulkhead / rate-limit *sizing* → review-r8e-policy-overload
- Fallback *value* safety, cache *key* correctness → review-r8e-policy-fallback
- Hooks / metrics / health wiring → review-r8e-policy-observability

You judge whether retry/hedge are SAFE for this call (a call axis concern); the
retry/overload axes judge whether they are TUNED well. Staying in lane is
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

- Operation class: read / idempotent vs write / side-effecting (and whether this
  is stated or UNKNOWN).
- Retry-safety: `WithRetry` present while the op mutates state and sends no
  idempotency key the server honors.
- Hedge-safety: `WithHedge` present while the op has side effects (two concurrent
  executions of the same write).
- `ctx` honoring: does the wrapped fn accept the policy `ctx` and pass it to its
  I/O? A call ignoring `ctx` makes `WithTimeout`/`WithTimeBudget`/cancellation
  unable to stop in-flight work (only stop *waiting* on it).
- Orphaned side effect: a timeout/cancel that returns to the caller while the
  downstream write keeps running to completion.
- Resource cleanup on EVERY exit path (response body, connection, file, lock),
  including the fallback / timeout / cancel / panic paths.
- Partial-failure consistency: a write retried after a partial application leaves
  inconsistent state.
- Shared captured state: variables closed over by the fn that two hedge
  goroutines (or sequential retries) mutate without synchronisation.
- Re-execution determinism: read-after-write / token-refresh / RNG inside the fn
  that misbehaves when run more than once.
- Real error taxonomy: which errors the call actually returns, and whether
  permanent ones are distinguishable (a precondition the retry/fallback axes
  depend on — enumerate the taxonomy here even though *acting* on it is theirs).
- Panic surface: can the fn panic, and is `WithRecover` present if so.
- Blocking / unbounded work not bounded by `ctx` (infinite read, no deadline).
- Nested guards: the call already has its own internal timeout/retry that fights
  the policy's.

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which the wrapped call fails
under its policy. Each: Trigger / Propagation / Symptom / Detectability. If fewer
than 5 are findable, justify in writing that the call's failure modes are
structurally bounded. Prioritise: retry of a write (double effect), hedge of a
write (concurrent duplicate), ctx ignored (orphaned work after timeout),
partial-write retried (inconsistency), resource leaked on a non-happy path.

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated failure mode, not aesthetic preference or a "best
practice" invoked without a concrete case. A retried/hedged non-idempotent write,
or a timeout that cannot cancel a committing write, is BLOCKING.

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
- Treating an UNKNOWN idempotency/side-effect as safe by default

## References

- references/checklist-call.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (classification default, code-only
  patterns), ../r8e-policy/references/decision-matrix.md (idempotency gate),
  ../r8e/SKILL.md (Recover, Hedge, error classification semantics)
