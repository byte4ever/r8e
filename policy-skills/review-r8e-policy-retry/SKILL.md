---
name: review-r8e-policy-retry
description: >-
  Adversarial review of the retry / amplification axis of an r8e policy —
  WithRetry attempts/backoff/jitter/MaxDelay, PerAttemptTimeout, error
  classification (the r8e default treats UNCLASSIFIED errors as transient =
  retriable), retry budgets on shared dependencies, concurrency budgets capping
  in-flight retries/hedges, and Retry-After honoring. Judges whether the retry
  configuration amplifies load into a storm and whether permanent errors are
  retried hopelessly. REJECT by default.
---

# review-r8e-policy-retry

## Disposition (load-bearing — do not soften)

Default verdict: REJECT. APPROVE must be justified by explicit demonstration that
the procedure has been followed and no blocking issue has been found. You are not
evaluating effort, intent, or investment.

You do not receive the author's identity, the discussion history, or the
production context. Treat the artifact as anonymous. The artifact is the **policy
code AND the service context together** — you cannot judge retry amplification
without knowing whether the dependency is shared, what its error taxonomy is, and
what latency budget the call has. Any service trait marked UNKNOWN is unresolved
risk, not a benign default: a retry against a dependency of unknown sharing or
unknown permanent-error modes is a finding, not an assumption.

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

Trigger on review of an r8e policy that carries `WithRetry` (or a retry budget /
concurrency budget / Retry-After mechanism). Also on explicit request to judge
whether the retry configuration amplifies load (storm, thundering herd,
metastable failure) or wastes attempts on permanent errors.

## Scope (what this reviewer judges — and what it does NOT)

This reviewer judges ONLY the **retry configuration and its amplification /
classification behavior**: `maxAttempts`, backoff strategy (ConstantBackoff
synchronizes retry waves — prefer ExponentialJitterBackoff), jitter presence,
`MaxDelay` cap, `PerAttemptTimeout`, error classification (the r8e default treats
UNCLASSIFIED errors as TRANSIENT/retriable, so an unclassified retry burns 4xx /
auth / validation errors that can never succeed), retry budgets on SHARED
dependencies (`WithRetryBudget` / `WithSharedRetryBudget` / nested `Parent`) to
prevent retry storms and metastable amplification, concurrency budgets
(`WithConcurrencyBudget`) capping in-flight retries+hedges, Retry-After honoring
(429/503 via `RetryAfterProvider` / httpx `StatusError`), `RetryIf` predicate
correctness, and whether the worst-case attempt count fits the latency budget.

OUT OF SCOPE — do NOT score these; note at most once as NOTED "out of scope —
defer to <axis>":
- Whether retry is SAFE for a non-idempotent write (duplicate side effect) →
  review-r8e-policy-call. You judge AMPLIFICATION and CLASSIFICATION, not the
  duplicate-side-effect safety. You may note that retrying a write also amplifies
  the side effect, but defer the safety verdict to -call.
- Timeout / budget *sizing* (WithTimeout / WithTimeBudget durations) →
  review-r8e-policy-timeouts
- Circuit breaker / bulkhead / rate-limit *sizing* → review-r8e-policy-overload
- Fallback *value* safety, cache *key* correctness → review-r8e-policy-fallback
- Hooks / metrics / health wiring → review-r8e-policy-observability

You judge whether the retry config amplifies and classifies correctly; the call
axis judges whether retrying that call is SAFE, the timeouts axis judges whether
the budget it must fit is SIZED right. Staying in lane is mandatory.

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

- `maxAttempts` value (and whether it fits the latency budget — see below).
- Backoff strategy: ConstantBackoff vs ExponentialBackoff vs
  ExponentialJitterBackoff. Constant synchronizes all retriers into waves.
- Jitter present? Exponential WITHOUT jitter still synchronizes the cohort that
  failed together (mild thundering herd).
- `MaxDelay` cap present? Unbounded exponential can balloon a single attempt's
  wait past the call's budget.
- `PerAttemptTimeout` present / sane as a retry option.
- Classification of permanent errors present (`Permanent()` / `RetryIf` / httpx
  classifier)? The service taxonomy: which errors are permanent (4xx auth /
  validation / business rejections)?
- **Default-transient trap:** with `WithRetry` and NO classification, the r8e
  default treats every unclassified error as transient → permanent errors are
  retried `maxAttempts` times (burns load, never succeeds).
- Retry budget present on a SHARED dependency? (`WithRetryBudget` /
  `WithSharedRetryBudget` / nested `Parent`.)
- Shared vs dedicated dependency: a shared third-party / internal dep hit by every
  replica amplifies retries fleet-wide.
- Concurrency budget (`WithConcurrencyBudget`) capping in-flight retries+hedges.
- Retry-After honoring (429/503): does the error implement `RetryAfterProvider`
  (httpx `StatusError`), or is a fixed backoff ignoring the server's signal?
- httpx adapter used so 429/503 carry Retry-After and 4xx classify Permanent?
- Retry count vs latency budget: worst-case sum of attempts + backoff vs the
  call's deadline / SLA. A 5× ConstantBackoff(200ms) can blow a 500ms budget.
- Retry of a write also AMPLIFIES the side effect (note; defer the safety verdict
  to -call).
- `RetryIf` predicate correctness: does it actually exclude the permanent errors,
  or invert the test / miss a class?
- Whether first-attempt-only / non-retriable errors are mishandled (retried when
  they should fast-fail, or classified Permanent when they are transient).

**Minimum threshold: 15 observations** for a non-trivial artifact. If fewer,
explicitly justify triviality in writing.

### Phase 2 — Adversarial scenarios

Produce **a minimum of 5 concrete scenarios** in which the retry configuration
amplifies load, wastes attempts, or blows the budget. Each: Trigger /
Propagation / Symptom / Detectability. If fewer than 5 are findable, justify in
writing that the retry surface is structurally bounded. Prioritise: shared-dep
brownout with no budget (retry storm / metastable failure), ConstantBackoff
synchronized waves (thundering herd), permanent error retried `maxAttempts`×
(wasted load + latency), worst-case attempts blowing the latency budget,
Retry-After ignored under throttling (429 stampede).

### Phase 3 — Scoring

For each observation and scenario assign BLOCKING / IMPORTANT / NOTED. Scoring
rests on a demonstrated failure mode, not aesthetic preference or a "best
practice" invoked without a concrete case. A retry on a SHARED dependency with no
budget AND no classification (self-amplifying storm under a partial outage), or
ConstantBackoff with no jitter on a shared dep combined with no budget
(synchronized thundering herd), is BLOCKING.

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
- Treating an UNCLASSIFIED retry as safe by default — the r8e default retries
  every unclassified error, so absence of classification is a finding, not a
  neutral omission.

## References

- references/checklist-retry.md — per-category checks
- references/severity-rubric.md — calibrated BLOCKING/IMPORTANT/NOTED examples
- references/good-reviews.md — three complete worked reviews
- Canonical rules (read-only source of truth):
  ../r8e-policy/references/api-constraints.md (classification default — unclassified
  is transient; RetryBudget/ConcurrencyBudget companion rules),
  ../r8e-policy/references/decision-matrix.md (error-taxonomy → retry rows, shared-dep
  → budget), ../r8e/SKILL.md (backoff strategies, RetryAfterProvider semantics)
