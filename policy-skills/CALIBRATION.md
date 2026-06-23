# Calibration registry — r8e-policy review fleet

Per the reviewer-agent specification (§4.7 test on graded artifacts, §4.8
attribution-inversion, §5 measurement). Records the calibration run that gated
this fleet's first release, the defects it surfaced, the fixes, and the
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
