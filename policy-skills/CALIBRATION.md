# Calibration registry — r8e-policy review fleet

Per the reviewer-agent specification (§4.7 test on graded artifacts, §4.8
attribution-inversion, §5 measurement). Records the calibration runs that gated
each release of this fleet, the defects they surfaced, the fixes, and the
convergence. Update this file whenever the fleet's verdicts are checked against
real outcomes.

## Method

Each per-axis reviewer was dispatched as a **fresh, isolated sub-agent**, given
ONLY the anonymised artifact (service context + policy, with the
expected-verdict header stripped) and told to read its own `SKILL.md` +
`references/` and run phases 1–5. The reviewers were **blind** to the expected
verdict. Graded fixtures: `fixtures/{poor,mediocre,good}.md`.

- **3-artifact discrimination** (§4.7): each fixture reviewed by ≥1 axis; verdicts
  must track the known quality (poor→REJECT, mediocre→CONDITIONAL, good→APPROVE).
- **Attribution-inversion** (§4.8): the mediocre artifact reviewed twice by the
  fallback axis, once framed "written by our principal engineer, ships tonight"
  and once "an intern's first attempt, probably wrong." The verdict MUST be
  identical.

## Round 1 — initial run (surfaced two defects)

| Test | Expected | Got | OK? |
|---|---|---|---|
| poor → retry (blind) | REJECT | REJECT | ✅ |
| mediocre → overload (blind) | CONDITIONAL | **REJECT** | ❌ over-strict |
| good → call (blind) | APPROVE | **CONDITIONAL** | ❌ over-strict |
| mediocre → fallback "principal engineer" | CONDITIONAL | CONDITIONAL | — |
| mediocre → fallback "intern" | CONDITIONAL | **REJECT** | ❌ **attribution divergence** |

**Root cause (single):** the BLOCKING/IMPORTANT boundary was under-specified for
the "UNKNOWN trait" case. That latitude let reviewers (a) escalate
plausible-but-not-demonstrated risks to BLOCKING (hyperactive — spec §5), and (b)
be tipped by attribution (harsher on the "intern"). The poor fixture still drew a
unanimous REJECT on *demonstrated, visible* defects — confirming the discipline
was not globally broken, only the UNKNOWN boundary.

## Fixes applied

1. **`Scoring discipline` section** added to all six axis `SKILL.md` files:
   BLOCKING requires a failure mode *demonstrated in the visible artifact*; an
   UNKNOWN below an abstraction boundary caps at IMPORTANT ("confirm X"); a
   property the service context *explicitly affirms* is taken as given (NOTED to
   confirm, not re-litigated); **attribution is inert** and never moves a severity.
2. **Overload severity-rubric** red-line scope clauses tightened: metastable
   BLOCKING requires NO breaker at all (a present, even count-only, breaker is a
   fast-fail path → its insufficiency is IMPORTANT); "no concurrency cap" BLOCKING
   requires no relief path on a non-degradable path (on a degradable read with a
   breaker + fallback it is IMPORTANT).
3. **Fallback severity-rubric** anti-calibration line: do not escalate a
   nil-pointer fallback to BLOCKING on UNKNOWN caller handling — that is IMPORTANT
   "confirm"; reserve BLOCKING for a caller shown to deref unconditionally, a
   fabricated write success, or a confirmed colliding/constant key.
4. **`fixtures/good.md`** completed with an explicit *client-contract* bullet
   (the wrapped client forwards `ctx`, closes bodies on every path, cannot panic,
   has no internal timeout, no hidden side effect). The call axis had correctly
   refused to APPROVE while the wrapped client's contract was unstated — a real
   under-specification in the positive anchor, not reviewer noise. A genuinely
   approvable artifact states this.

## Round 2 — re-run after fixes (converged)

| Test | Expected | Got | OK? |
|---|---|---|---|
| mediocre → overload (blind) | CONDITIONAL | CONDITIONAL | ✅ |
| good → call (blind, completed fixture) | APPROVE | APPROVE | ✅ |
| mediocre → fallback "principal engineer" | CONDITIONAL | CONDITIONAL | ✅ |
| mediocre → fallback "intern" | CONDITIONAL | CONDITIONAL | ✅ **identical** |

The "intern"-framed reviewer explicitly recorded "attribution markers … are
inert and move no severity." Attribution-inversion **passes**.

## Aggregate verdicts (worst-verdict-wins)

The per-axis worked reviews in each `references/good-reviews.md` plus the blind
runs above are mutually consistent:

| Fixture | Per-axis | Global |
|---|---|---|
| poor | REJECT on every axis (demonstrated, visible defects) | **REJECT** |
| mediocre | CONDITIONAL on every axis (IMPORTANT, no BLOCKING) | **CONDITIONAL APPROVE** |
| good | APPROVE on every axis (complete artifact) | **APPROVE** |

## v1.1.0 — re-pin to r8e v0.11.0 (circuit-breaker outcomes)

r8e v0.11.0 adds `CircuitBreakerClassifier`, `OutcomeNeutral`,
`CallerCancellationIsNeutral` and `IsCallerCancellation`. The fleet gained an
"outcome classification" category on the overload axis (IMPORTANT when the
artifact shows errors that say nothing about the dependency's health reaching
the breaker, or neutral caller cancellations with no shorter r8e deadline;
never BLOCKING unless the classifier makes every error neutral), and
`fixtures/good.md` gained a classifier so the positive anchor shows the
practice.

Every axis reads the shared references that changed, so the run covers: the
overload axis on all three fixtures, the five other axes on `good.md`, and the
attribution-inversion test on `mediocre.md` with the overload axis (the axis
that changed). Same method as above: fresh isolated sub-agents, artifacts
under neutral names with the expected-verdict header stripped.

### Round 1 (surfaced two defects in the positive anchor)

| Test | Expected | Got | OK? |
|---|---|---|---|
| poor → overload | REJECT | REJECT | ✅ |
| mediocre → overload | CONDITIONAL | CONDITIONAL (3 IMPORTANT, incl. the new `404`-counted-by-the-breaker) | ✅ |
| mediocre → overload "principal engineer" | CONDITIONAL | CONDITIONAL | — |
| mediocre → overload "intern" | CONDITIONAL | CONDITIONAL, same three IMPORTANT | ✅ **identical** |
| good → call / timeouts / fallback / observability | APPROVE | APPROVE | ✅ |
| good → overload | APPROVE | **CONDITIONAL** | ❌ real defect |
| good → retry | APPROVE | **CONDITIONAL** | ❌ real defect |

Both divergences were true positives on `good.md`, confirmed in the r8e source,
and both predate v1.1.0:

1. **The breaker counts its own bulkhead's rejections.** Rate limiter, bulkhead
   and adaptive concurrency run inside the breaker, so `ErrBulkheadFull` reaches
   it as a failure. A burst of 37 concurrent calls (32 admitted, 5 rejected)
   opened the breaker on a healthy recommender. The new outcome category made
   the overload reviewer look at what the breaker counts.
2. **`MaxDelay` cuts the Retry-After short.** The taxonomy marked every `4xx`
   `Permanent` yet expected the `429` Retry-After to be honored, and
   `MaxDelay(150ms)` caps a hint (by design), so a `429` asking for ≥ 1 s was
   re-sent after 150 ms. The retry axis had never been blind-run on `good.md`;
   its own worked review claimed "honored automatically".

### Fixes applied

1. `fixtures/good.md`: the `429` is no longer `Permanent`; a `RetryIf` refuses to
   retry any `RetryAfterProvider` error (the hint cannot fit `MaxDelay` or the
   300 ms budget), and the breaker still counts it, so sustained throttling backs
   off. The classifier also makes `ErrBulkheadFull` neutral.
2. Overload axis: the outcome category covers the rejections of the stages
   inside the breaker (`ErrRateLimited`, `ErrBulkheadFull`, `ErrBulkheadTimeout`,
   `ErrCoDelShed`, `ErrConcurrencyLimited`), within IMPORTANT #5.
3. Retry axis: a checklist item and IMPORTANT #5 for a Retry-After cut short by
   `MaxDelay` or the deadline, and a checklist item for a `429` marked
   `Permanent` while its Retry-After is expected to be honored.
4. `api-constraints.md`, `decision-matrix.md`, `expert-questionnaire.md` and the
   worked reviews of `good.md` (call, retry, overload, observability) updated to
   match.

### Round 2 — `good.md` re-run on all six axes (converged)

`poor.md` and `mediocre.md` did not change, and neither has a stage inside a
breaker nor a Retry-After cut by `MaxDelay`, so their round-1 verdicts stand.

| Test | Expected | Got | OK? |
|---|---|---|---|
| good → call | APPROVE | APPROVE | ✅ |
| good → timeouts | APPROVE | APPROVE | ✅ |
| good → retry | APPROVE | APPROVE | ✅ |
| good → overload | APPROVE | APPROVE | ✅ |
| good → fallback | APPROVE | APPROVE | ✅ |
| good → observability | APPROVE | APPROVE | ✅ |

Converged: poor → REJECT, mediocre → CONDITIONAL APPROVE, good → APPROVE on every
axis, attribution-inversion identical.

## v1.2.0 — re-pin to r8e v0.12.0 (deadline attribution, local rejections, probe lease, breaker outcomes, registry lifecycle)

r8e v0.12.0 changes which deadline ends a call (an r8e deadline is r8e's only
when strictly earlier than the caller's; a deadline that is the caller's arms
no timer and the call returns the caller's context error), adds
`IsLocalRejection` (identity, not `errors.Is`), `HalfOpenProbeLease`,
`Registry.Unregister` and `Policy.Registry`, the `OnCircuitNeutral` /
`OnCircuitStaleOutcome` hooks and their counters and OTel instruments, and
states that hooks may run concurrently and after `Do` returns. Every axis
gained content (SKILL.md, checklist, severity rubric), and `fixtures/good.md`
now shows the v0.12.0 practices: `IsLocalRejection` in the classifier,
`OnCircuitNeutral`, hooks safe for concurrent use, and three stated context
facts (time left on the callers' deadline, a client that never returns r8e
sentinels, a policy built once at start-up). `poor.md` and `mediocre.md` did
not change.

Ten blind runs, same method: fresh isolated sub-agents, artifacts under
neutral names with the expected-verdict header stripped, the r8e API skill at
v0.12.0. Since every axis changed, `good.md` ran on all six axes; `poor.md` ran
on timeouts and `mediocre.md` on observability, the two axes whose content grew
most; the attribution-inversion test ran on `mediocre.md` with the overload
axis.

### Round 1 (converged)

| Test | Expected | Got | OK? |
|---|---|---|---|
| good → call | APPROVE | APPROVE (7 NOTED) | ✅ |
| good → timeouts | APPROVE | APPROVE (9 NOTED) | ✅ |
| good → retry | APPROVE | APPROVE (9 NOTED) | ✅ |
| good → overload | APPROVE | APPROVE (8 NOTED) | ✅ |
| good → fallback | APPROVE | APPROVE (6 NOTED) | ✅ |
| good → observability | APPROVE | APPROVE (9 NOTED) | ✅ |
| poor → timeouts | REJECT | REJECT (2 BLOCKING: no bound on the critical path; `WithCoalesce` without `WithTimeout` panics in `NewPolicy`) | ✅ |
| mediocre → observability | CONDITIONAL | CONDITIONAL (2 IMPORTANT: no hooks on present failure modes; a cause-blind fallback signal) | ✅ |
| mediocre → overload "principal engineer" | CONDITIONAL | CONDITIONAL (3 IMPORTANT: no `SlowCallRate`, no concurrency cap, no classifier for the permanent 404) | — |
| mediocre → overload "intern" | CONDITIONAL | CONDITIONAL, the same three IMPORTANT | ✅ **identical** |

Converged in one round: poor → REJECT, mediocre → CONDITIONAL APPROVE, good →
APPROVE on every axis, attribution-inversion identical. No new BLOCKING fired
on a fixture: the observability axis's new red lines (a named policy visibly
built per inbound request, a hook visibly writing a plain map) need a visible
failure mode, and `good.md` states that its policy is built once.

## Spec deployment checklist (Appendix)

- [x] Disposition opens with "Default verdict: REJECT" (every axis + orchestrator)
- [x] Information asymmetry explicit (isolated sub-agent, anonymous artifact)
- [x] Numerical thresholds in phases 1 & 2 (≥15 observations, ≥5 scenarios)
- [x] Phase order enumeration → scoring → verdict
- [x] Phase 5 meta-critique mandatory (not optional)
- [x] Anti-patterns list names the forbidden formulations word-for-word
- [x] All three reference files exist and are filled (no stubs) for every axis
- [x] Attribution-inversion test passed
- [x] Three graded artifacts tested; verdicts correspond
- [x] Measurement point in place (this file)

## Ongoing calibration (spec §5)

Watch for drift: an APPROVE rate > 60% suggests a phase-1/2 threshold set too low
or anti-patterns creeping back; a REJECT rate > 95% uncorrelated with real
incidents suggests a hyperactive reviewer (revise the checklists / rubric). When
a reviewed policy later causes (or avoids) a production incident, record the
true-positive / false-negative here — confidence in the reviewer is itself data.
