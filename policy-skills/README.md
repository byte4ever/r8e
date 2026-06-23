# r8e policy skill fleet

A Claude Code **skill fleet** for authoring and auditing [r8e](https://github.com/byte4ever/r8e)
resilience policies. It mirrors the `go` / `review-go` fleet shape: one authoring
skill, one review orchestrator, and six isolated per-axis reviewers.

The premise: **a policy can only be judged against the call it wraps**. The same
`WithRetry(3, …)` is correct for an idempotent read and a financial incident for a
card charge. So this fleet's reviewers each judge one axis of the *policy + its
target service together*, and the authoring skill **interrogates the service**
(in expert mode, when there is no code to read) before it writes a single option.

## Fleet map

| Skill | Role | Analogue |
|---|---|---|
| `r8e-policy` | **Authoring** — expert questionnaire + trait→pattern decision matrix; modes *author* / *audit*. In author mode it MUST submit the draft to the review fleet as a gate. | `go` |
| `review-r8e-policy` | **Orchestrator** — selects applicable axes, fans them out as concurrent isolated sub-agents, arbitrates *worst-verdict-wins*. Default global verdict: **REJECT**. | `review-go` |
| `review-r8e-policy-call` | Axis: the protected function inside `Do` — idempotency, side effects, retry/hedge safety, ctx honoring, resource cleanup, real error taxonomy. | `review-go-concurrency` |
| `review-r8e-policy-timeouts` | Axis: deadlines & latency — timeout vs p99, time budget, deadline propagation, adaptive timeout, hedge delay, unbounded-latency hazards. | per-aspect |
| `review-r8e-policy-retry` | Axis: retry & amplification — strategy/backoff, retry & concurrency budgets, retry-after, error classification, retry of non-idempotent ops. | per-aspect |
| `review-r8e-policy-overload` | Axis: overload protection & shedding — circuit breaker, bulkhead/adaptive concurrency, rate limit, throttle, SLO governor, metastable failure. | per-aspect |
| `review-r8e-policy-fallback` | Axis: degradation & data correctness — fallback safety, cache/coalesce key correctness, ttl, stale-if-error, negative cache, refresh-ahead. | per-aspect |
| `review-r8e-policy-observability` | Axis: ops — hooks/metrics tied to failure modes, health/readiness impact, naming/registry, config-vs-code & hot reload, chaos kill-switch, OTel. | per-aspect |

`fixtures/` holds three graded policy artifacts (`good.md`, `mediocre.md`,
`poor.md`) used for the calibration run and cited by the per-axis worked reviews.
`CALIBRATION.md` records the calibration + attribution-inversion results.
`VERSIONS.json` (the version pin), `install.sh` / `install.ps1` (cross-platform
installers), and `MAINTENANCE.md` (re-pin/release runbook) drive the independent
release — see **Versioning** and **Install** below.

## Design invariants (load-bearing — do not soften)

Every reviewer in this fleet obeys the anti-sycophancy contract:

- **Default verdict REJECT.** APPROVE is *earned* by exhausting the procedure.
- **Information asymmetry.** Reviews run in **isolated sub-agents**; the artifact
  is anonymous (no author, no history, no deadline/investment markers).
- **Forced numerical production.** ≥ 15 observations in enumeration, ≥ 5
  adversarial scenarios — per axis.
- **Strict phase order.** enumeration → scoring → verdict, never mixed.
- **Mandatory meta-critique** (Phase 5) — a verdict without it is invalid.
- **Named anti-patterns**, forbidden word-for-word.

These come from the reviewer-agent specification and are non-negotiable across
the fleet.

## Versioning

The fleet is released **independently** of r8e core, with its own SemVer, but it
is **pinned** to the exact r8e (and side-package) version it was calibrated
against. The single source of truth is [`VERSIONS.json`](VERSIONS.json):

| skill_version | r8e | otter | ristretto | r8eotel |
|---|---|---|---|---|
| `1.0.0` | `v0.10.0` | `v0.10.0` | `v0.10.0` | `v0.10.0` |

Releases are tagged `policy-skills/v<skill_version>` with a self-contained tarball
asset. The authoring skill always proposes r8e **at the pinned version** so your
project and the skill agree. Re-pinning and re-calibration on each r8e / side-
package release are governed by [`MAINTENANCE.md`](MAINTENANCE.md).

## Install

Each directory is a skill; they install as siblings under `.claude/skills/` so the
relative `../<skill>/…` cross-references resolve. Two cross-platform install
scripts ship with every release and install from their own location (clone or
extracted tarball).

**From a clone of r8e** (Linux/macOS):

```bash
./policy-skills/install.sh            # symlink into ./.claude/skills (default)
./policy-skills/install.sh --copy --dir /path/to/proj/.claude/skills
```

Windows (PowerShell):

```powershell
.\policy-skills\install.ps1           # copy into .\.claude\skills (default)
.\policy-skills\install.ps1 -Symlink  # link instead (needs Developer Mode)
```

**From a release** (no clone): download and extract the tarball, then run the
bundled script:

```bash
ver=1.0.0
curl -fsSL -o ps.tgz \
  "https://github.com/byte4ever/r8e/releases/download/policy-skills/v${ver}/policy-skills-v${ver}.tar.gz"
tar -xzf ps.tgz && ./policy-skills/install.sh
```

**Manual** (if you prefer not to run a script): symlink the eight directories
listed in the Fleet map above into `.claude/skills/`.

The fleet relies on the existing `r8e` API-reference skill as its canonical source
of truth for r8e signatures. Install it too — either via the main README "Claude
Code Skill" section, or by passing `--with-r8e-ref <path-to>/claude-skill` to
`install.sh` (`-WithR8eRef` on PowerShell).

## Usage

- **Write a policy** (with or without the code): `Use the r8e-policy skill to
  write a policy for <service>.` In expert mode it asks the questions whose
  answers drive every parameter, then drafts, then runs the review gate.
- **Audit a policy**: `Use review-r8e-policy on this policy.` The orchestrator
  fans out the applicable axes and returns one consolidated, attributed verdict.
- **One axis only**: invoke the axis reviewer directly, e.g.
  `review-r8e-policy-retry`.
