# Severity rubric — observability / ops axis

Calibrated examples. Severity rests on a **demonstrated operational failure mode**,
never on aesthetic preference or a best-practice cited without a concrete case.
When in doubt between two levels, the one with a concrete propagation path wins.

## BLOCKING — demonstrated failure mode, remediation required before approval

1. **Readiness gated on a shared dependency.** `WithReadinessImpact()` on a policy
   protecting a third-party / fleet-common dependency. Propagation: the dependency
   blips → every replica's breaker (or readiness condition) trips at the same time
   → every pod's `/readyz` flips NotReady together → the load balancer pulls the
   whole service at once. Symptom: a brief downstream wobble becomes a full
   self-inflicted outage. Remediation: do NOT gate readiness on a shared dep;
   leave it informational (open breaker is reported but does not pull the pod).
2. **Chaos with no kill-switch.** `WithChaos(…)` in a production policy with no
   `ChaosEnabled` off switch. Propagation: a problem appears, the operator wants
   to stop injecting faults, but chaos is code-only and there is no runtime
   toggle → the only way to stop it is a redeploy. Symptom: cannot disable an
   active fault injector under pressure. Remediation: add `ChaosEnabled` gated on
   config/env so chaos can be turned off without shipping code.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **No hooks on present failure modes.** The policy has a breaker / retry /
   fallback but no `WithHooks` — `OnCircuitOpen`/`OnRetry`/`OnFallbackUsed` never
   fire. Propagation: the breaker opens, retries fire, fallback activates, and
   none of it reaches logs or alerting. (Metrics counters still increment
   automatically — NOTED — but nothing is wired to alert on them.) Acceptable only
   if the team confirms it alerts on the automatic metric counters out of band;
   otherwise wire the hooks.
2. **Empty / anonymous name.** `NewPolicy("", …)` or an anonymous `r8e.Do(…)`.
   Propagation: the policy is not (meaningfully) registered → no health identity →
   an operator triaging cannot find its state by name. Remediation: give it a
   stable, meaningful name.
3. **Dedicated dep not gated.** A policy guards a dependency this pod OWNS and
   whose loss should pull the pod, but `WithReadinessImpact()` is absent.
   Propagation: the dep dies, the pod keeps reporting Ready, traffic keeps
   arriving at a broken instance. Remediation: gate readiness (the opposite of the
   shared-dep red line).
4. **Code-only pattern presumed retunable.** A `WithCache`/`WithCoalesce`/
   classifier is in the policy and the context says operators retune via JSON.
   Propagation: the emitted `PolicyConfig` silently omits that pattern; an
   operator edits the JSON and the pattern is unaffected (or `Reconfigure` returns
   `ErrPatternAbsent`). Remediation: document that these are code-only; do not
   promise runtime tuning of them.

## NOTED — observation for awareness, no action required

1. No `r8eotel.Register` / `r8eotel.Trace`, and the context does NOT say the shop
   runs OTel — defer; add a tracing bridge if/when an OTel stack lands.
2. The policy could surface an extra hook (e.g. `OnBulkheadRejected`) for a mode
   it has, but that mode is low-signal here — flag, no action.
3. Metrics are collected automatically; the team alerts on those counters out of
   band and confirmed it — the missing event hooks are then cosmetic.

## Anti-calibration (do NOT do this)

- Scoring "the breaker's threshold is wrong" here — that is the overload axis.
- Calling "no metrics" a finding — metrics are AUTOMATIC; the only metrics-shaped
  finding is hooks→alerting, named precisely.
- Marking a SHARED-dep readiness gate merely IMPORTANT — it is BLOCKING (fleet
  flip).
- Marking the absence of OTel BLOCKING when the context never mentions OTel —
  that is at most NOTED.
- Treating `Reconfigure` as able to add a missing pattern — it cannot; only
  retunes existing ones.
