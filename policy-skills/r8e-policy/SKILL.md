---
name: r8e-policy
description: >-
  Author and audit r8e resilience policies. Use when asked to write, design,
  choose patterns for, or review an r8e Policy[T] / Do() call / r8econf
  PolicyConfig for a specific service — especially in EXPERT mode, where there is
  no code to read and the policy must be derived by interrogating the target
  service. Drives a six-axis decision matrix (call/Do, timeouts, retry, overload,
  fallback/cache, observability) and validates every drafted policy through the
  review-r8e-policy fleet before delivery. Use whenever someone says "write/design
  an r8e policy", "which patterns do I need for X", or "review my policy".
---

# r8e-policy

Authoring router for r8e resilience policies. A policy is a composition of r8e
patterns wrapping one downstream call. The hard part is **not the API** — it is
matching the patterns and their parameters to the *nature of the call and the
service it hits*. This skill makes that match explicit, then proves it with the
review fleet.

> Canonical r8e API truth lives in the `r8e` skill (`../r8e/SKILL.md`). This skill
> does not restate signatures; it decides *which* patterns, with *what*
> parameters, and *why*. The hard ordering / requirement / exclusion constraints
> are in `references/api-constraints.md`.

## Pinned r8e version

This skill release is calibrated for an **exact** r8e version. The canonical pin
is [`../VERSIONS.json`](../VERSIONS.json); this section is kept in agreement with
it (both are bumped together on each release — see `../MAINTENANCE.md`).

- **r8e core**: `v0.10.0`
- side packages: `otter v0.10.0`, `ristretto v0.10.0`, `r8eotel v0.10.0`

Always propose r8e **at this pinned version** so the user's project and this skill
agree. When you deliver a policy, include the matching install snippet:

```sh
go get github.com/byte4ever/r8e@v0.10.0
```

```go
// go.mod
require github.com/byte4ever/r8e v0.10.0
// + only if used: github.com/byte4ever/r8e/otter|ristretto|r8eotel v0.10.0
```

If the user is already on a different r8e version, say so explicitly: features
newer than the pin may not be reviewed by this skill release, and features
removed/renamed since the pin may not exist — recommend aligning to the pin or
upgrading the skill (`../MAINTENANCE.md`).

## First principle — the call governs the policy

The same option is correct or catastrophic depending on the call:

- `WithRetry(5, …)` — sane for an idempotent GET, a **double-charge** for a
  payment.
- `WithHedge(…)` — free latency win for a read, **duplicate side effects** for a
  write.
- `WithFallback(value)` — graceful degradation for recommendations, a **silent
  financial incident** when it fabricates a "success" for a failed charge.

So you never write a policy you cannot justify against the call. When the code is
available, **read the `Do` body and the target's client**. When it is not (the
common expert-mode case), **interrogate the user** before drafting — see Expert
mode below.

## Three modes

- **Author** (`write a policy for …`) — elicit the service traits, map them to
  patterns via the decision matrix, draft the policy, then run the **review gate**
  (mandatory) and iterate until it no longer REJECTs.
- **Audit** (`review my policy`) — do NOT review here; hand off to the
  `review-r8e-policy` orchestrator (isolated sub-agents) and relay its verdict.
- **Refactor** (`tune / fix this policy`) — smallest change that removes a
  finding; preserve the call's contract; re-run the gate on the result.

## Procedure — Author mode (mandatory, in order)

### 1. Acquire the call's nature

If the code is available: read the `Do` body, the client it calls, and the error
types it returns. Record, for each of the six axes, what you learned and what is
still unknown.

If the code is NOT available (expert mode): run the **expert questionnaire**
(`references/expert-questionnaire.md`). Ask only the questions whose answers
actually change a parameter, and only those you cannot infer. Prefer the
`AskUserQuestion` tool with concrete options drawn from the questionnaire. Never
invent a service fact — an unknown trait is a question, not a default.

The six axes you must be able to answer for:

| Axis | The question the policy cannot be written without |
|---|---|
| **call / Do** | Is the operation idempotent / a write? Safe to retry? Safe to duplicate (hedge)? Does the call honor `ctx`? |
| **timeouts** | What is the downstream p50/p99? Is there a per-call budget or SLA? |
| **retry** | Which errors are permanent vs transient? Is the dependency shared across the fleet (amplification risk)? Does it emit `Retry-After`? |
| **overload** | What is the downstream capacity (pool size, quota, RPS limit)? Is metastable collapse a risk? |
| **fallback / cache** | Is there a *safe* degraded value? Is stale data acceptable, and for how long? |
| **observability** | Which failure modes must be visible? Should this policy gate readiness? Does config need hot reload? |

### 2. Map traits → patterns

Apply `references/decision-matrix.md`: each trait implies specific options and
parameter ranges, each with a one-line rationale. Carry the rationale into the
output — a parameter without a reason is a finding waiting to happen.

Check the draft against `references/api-constraints.md` BEFORE presenting:
required-companion options (e.g. `WithCoalesce`/refresh-ahead need `WithTimeout`;
`WithTimeBudget`/budgets need a consumer), mutual exclusions
(`WithBulkhead` vs `WithAdaptiveConcurrency`), and the fact that ordering is
automatic (never hand-order options).

### 3. Draft the output

Produce, by default:

- the `r8e.NewPolicy[T](name, …)` Go snippet (named, so it registers for health),
  with a short inline rationale per option;
- a **rationale table** (option → parameter → why → which service trait drove it);
- the `r8econf` JSON `PolicyConfig` block **for the config-expressible subset
  only**, and an explicit note of which chosen patterns are code-only (Coalesce,
  Cache, Chaos, classifiers, fallback funcs) and thus absent from JSON;
- the residual unknowns you assumed, each flagged so the user can correct them;
- the **pinned install snippet** (`go get …@<pinned>` + the `require` line) at the
  version from "Pinned r8e version" above, plus the side-package requires only for
  the adapters the policy actually uses (`otter`/`ristretto` for `WithCache`,
  `r8eotel` for OTel).

### 4. Review gate (mandatory — do not skip)

A drafted policy is NOT delivered until it has passed the review fleet. Invoke
the `review-r8e-policy` orchestrator on `{service context + drafted policy}`
(it fans the applicable axes out as isolated sub-agents). Then:

- **GLOBAL REJECT** → fix every BLOCKING and re-run the gate. Do not deliver a
  rejected policy; do not argue the reviewer down.
- **CONDITIONAL APPROVE** → deliver, but surface the union of accepted risks to
  the user explicitly and let them accept each.
- **APPROVE** → deliver.

Pass the orchestrator ONLY the anonymised artifact (service context + policy) —
no "I just wrote this", no deadline, no investment. The gate's value comes from
its independence; feeding it your authorship destroys it.

### 5. Deliver

Present the policy, the rationale table, the JSON subset, the verdict, and any
accepted risks. If the user changes a service trait, re-map and re-run the gate —
do not patch the parameter by hand without re-justifying it.

## Anti-patterns (forbidden when authoring)

- Writing options before you know whether the call is idempotent / a write.
- Defaulting an unknown service trait instead of asking.
- Delivering a policy that has not passed the review gate.
- Hand-ordering options (ordering is automatic; order conveys nothing).
- A parameter with no rationale ("retry 3 because 3 is normal").
- A fallback that fabricates success for a failed write.
- Gating readiness (`WithReadinessImpact`) on a shared dependency without calling
  out the fleet-wide-flip risk.

## References

- `references/expert-questionnaire.md` — the elicitation question bank (axis →
  questions → what each answer changes), for the no-code path.
- `references/decision-matrix.md` — service-trait → pattern + parameter range +
  rationale.
- `references/api-constraints.md` — hard ordering, required-companion, and
  mutual-exclusion rules (the panics and `Err*WithoutX` sentinels).
- The review fleet (the gate): [../review-r8e-policy/SKILL.md](../review-r8e-policy/SKILL.md).
- Canonical r8e API: [../r8e/SKILL.md](../r8e/SKILL.md).
