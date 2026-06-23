# Maintaining & releasing the policy-skills fleet

The fleet is versioned and released **independently** of r8e core, but it is
**pinned** to an exact r8e version (and side-package versions) it was calibrated
against. This file is the runbook for keeping it in sync and cutting a release.

Single source of truth for versions: [`VERSIONS.json`](VERSIONS.json). It carries
the fleet's own `skill_version` and the exact `targets` (r8e, otter, ristretto,
r8eotel) it was built and calibrated for. The install scripts and
`r8e-policy/SKILL.md` ("Pinned r8e version") both read from it.

## Trigger: a new release of r8e or a side package

**Every time r8e core or a side package (otter / ristretto / r8eotel) is
released, evaluate whether the fleet needs an update.** (A PreToolUse hook on
`git tag` / `gh release create` reminds you — see `.claude/settings.json`.)

### 1. Diff the public surface against the last pinned version

Compare the new r8e release to `VERSIONS.json:targets.r8e`:

- New `WithX` pattern, option, or sub-option → may need a new decision-matrix row,
  a questionnaire question, a checklist category, and possibly a new axis or axis
  scope note.
- New / changed **sentinel error** or **required-companion / mutual-exclusion**
  rule → update `r8e-policy/references/api-constraints.md`.
- New **OTel instrument**, hook, or metric → update the observability axis
  (checklist + rubric) and the api-constraints / decision-matrix.
- Changed **auto-ordering** (priority list) → update `api-constraints.md` and any
  axis that reasons about ordering.
- Side-package change (otter/ristretto adapter API, r8eotel `Register`/`Trace`) →
  update the cache/observability references and the `targets` entry.

Sources to diff: the new tag's release notes, `claude-skill/SKILL.md` (the r8e API
reference), `errors.go` (sentinels), `config.go` (config-expressible surface),
`metrics.go` / `hooks.go`, and the `r8eotel` instrument list.

### 2. Update the affected skill content

Edit only what the diff implicates: `api-constraints.md`, `decision-matrix.md`,
`expert-questionnaire.md`, and the relevant axis `SKILL.md` +
`references/{checklist,severity-rubric,good-reviews}.md`. Keep the six axes
mirrored on the `review-r8e-policy-call/` gold template.

### 3. Re-pin the versions

Bump `VERSIONS.json`: set `targets.*` to the new versions, and bump
`skill_version` per the decision rubric below. Update the **"## Pinned r8e
version"** block in `r8e-policy/SKILL.md` to match (it is bumped together with
`VERSIONS.json` — they must agree).

### 4. Re-run the calibration (mandatory before tagging)

Re-run the calibration from `CALIBRATION.md` § Method: blind isolated per-axis
reviewers on `fixtures/{poor,mediocre,good}.md` + the attribution-inversion test.
Verdicts must still correspond (poor→REJECT, mediocre→CONDITIONAL, good→APPROVE)
and attribution-inversion must pass. Record the run in `CALIBRATION.md`. If a new
pattern is now common enough to belong in a fixture, add it and recalibrate.

### 5. Tag and release (see the release section below).

## skill_version decision rubric (independent SemVer)

The fleet's `skill_version` tracks **the skill**, not r8e:

- **PATCH** (`1.0.0 → 1.0.1`): docs/wording fixes, calibration tweak, or a pure
  re-pin to a new r8e PATCH that changes no reviewed surface.
- **MINOR** (`1.0.0 → 1.1.0`): new pattern coverage, a new checklist category, a
  new axis, or re-pin to an r8e MINOR that adds patterns.
- **MAJOR** (`1.0.0 → 2.0.0`): a breaking change to the review *contract* (verdict
  semantics, axis decomposition) or a re-pin to an r8e MAJOR.

A re-pin with no content change is still at least a PATCH (the `targets` changed).

## Release procedure

The fleet is not a Go module; it is released as a **git tag + GitHub release with
a tarball asset**, mirroring the side-package tag convention (`otter/vX.Y.Z`).

1. Ensure `VERSIONS.json`, the SKILL "Pinned r8e version" block, `CALIBRATION.md`,
   and the READMEs are updated and the calibration passed.
2. Commit (`feat(policy-skill): …` / `chore(policy-skill): re-pin r8e vX.Y.Z`).
3. Tag: `git tag -a policy-skills/v<skill_version> -m "policy-skills v<skill_version> — pinned to r8e <r8e_version>"`.
4. Build the release asset (a self-contained tarball that bundles the install
   scripts next to the skill dirs, so `install.sh`/`install.ps1` install from
   their own location):

   ```sh
   # The path already includes the policy-skills/ prefix, so the tarball extracts
   # to policy-skills/… (scripts next to the skill dirs) — no --prefix needed.
   git archive --format=tar.gz \
     -o policy-skills-v<skill_version>.tar.gz \
     policy-skills/v<skill_version> -- policy-skills
   ```

5. Publish: `gh release create policy-skills/v<skill_version> \
     policy-skills-v<skill_version>.tar.gz \
     --title "policy-skills v<skill_version>" \
     --notes "Pinned to r8e <r8e_version>. Install: see policy-skills/README.md."`
6. Push the tag: `git push origin policy-skills/v<skill_version>`.

## Compatibility note

A skill release is **calibrated for one r8e version**. Using it against a much
newer r8e is not unsafe (the reviewers are conservative), but new patterns will be
unreviewed until the next skill release. The authoring skill always proposes the
**pinned** r8e version so a user's project and the skill agree by default.
