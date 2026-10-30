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
3. **Named policy built per inbound request, never removed.** The artifact shows
   `NewPolicy("catalog-read", …)` inside a request handler, with no
   `Unregister`, no empty name and no registry dropped with it. Propagation:
   every request adds a policy to `DefaultRegistry`, which keeps it for the life
   of the process; memory grows with traffic, and `Health` / `CheckReadiness` /
   `Snapshot` iterate an ever longer list of dead policies. Symptom: slow health
   probes, then the process runs out of memory. Remediation: build the policy
   once at startup; or `defer policy.Registry().Unregister(policy)`; or an empty
   name. Scope: requires the per-request construction to be visible; per job or
   per tenant is IMPORTANT #5, and an unstated lifecycle is NOTED #4.
4. **A hook writes a plain Go map.** `OnRetry: func(_ int, err error) {
   counts[err.Error()]++ }` on a policy called concurrently. Propagation: two
   calls retry at once, both hooks write the map, and the runtime aborts with
   `concurrent map writes`, a fatal error that no `recover` catches. Symptom: the
   whole process crashes under load. Remediation: a metrics library, an atomic
   counter, or a mutex.

## IMPORTANT — justified concern, may be accepted with explicit documented risk

1. **No hooks on present failure modes.** The policy has a breaker / retry /
   fallback but no `WithHooks` — `OnCircuitOpen`/`OnRetry`/`OnFallbackUsed` never
   fire. Propagation: the breaker opens, retries fire, fallback activates, and
   none of it reaches logs or alerting. (Metrics counters still increment
   automatically — NOTED — but nothing is wired to alert on them.) Acceptable only
   if the team confirms it alerts on the automatic metric counters out of band;
   otherwise wire the hooks.
2. **Empty / anonymous name.** `NewPolicy("", …)` or an anonymous `r8e.Do(…)`.
   Propagation: the policy joins no registry → no health identity → an operator
   triaging cannot find its state by name. Remediation: give it a stable,
   meaningful name. Scope: an empty name chosen on purpose for a policy built
   per job or request keeps the registry bounded: NOTED #7, not this.
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
5. **Named policies built per job / tenant, never removed.** The artifact builds
   a named policy per job or per tenant, with no `Unregister`, no empty name and
   no dropped registry. Propagation: the registry keeps every one for the life of
   the process; health endpoints list jobs that ended long ago, with their last
   state: a breaker that was open when its job ended stays open, so the
   aggregate health reads "unhealthy" (and `/readyz` stays NotReady if the
   policy gates readiness). Remediation: `defer
   policy.Registry().Unregister(policy)`, an empty name, or a registry dropped
   with them.
6. **Hook not safe for concurrent use.** A hook increments a plain `int`,
   appends to a slice, sets a struct field, or writes a request-scoped logger or
   span. Propagation: hooks run concurrently and may run after `Do` returned; the
   counter loses updates, the slice races, the request-scoped object is used
   after its request ended. Symptom: wrong metrics, data races under `-race`.
   Remediation: a metrics library, atomics or a mutex; nothing request-scoped in
   a hook. (A plain map is BLOCKING #4.)
7. **Neutral outcomes unseen.** A `CircuitBreakerClassifier` makes caller
   cancellations neutral, and neither `OnCircuitNeutral` nor an alert on
   `CircuitNeutralOutcomes` / `r8e.policy.circuit_neutral_outcomes` is in view.
   Propagation: a neutral outcome feeds neither the failure counter nor the
   slow-call window; when callers start abandoning a slow dependency, the breaker
   sees nothing and no counter that anyone watches moves. Acceptable if the team
   confirms it alerts on the automatic counter out of band; otherwise wire the
   hook.

## NOTED — observation for awareness, no action required

1. No `r8eotel.Register` / `r8eotel.Trace`, and the context does NOT say the shop
   runs OTel — defer; add a tracing bridge if/when an OTel stack lands.
2. The policy could surface an extra hook (e.g. `OnBulkheadRejected`) for a mode
   it has, but that mode is low-signal here — flag, no action.
3. Metrics are collected automatically; the team alerts on those counters out of
   band and confirmed it — the missing event hooks are then cosmetic.
4. The artifact does not say where the policy is built — flag "confirm it is
   built once, or removed from its registry when done".
5. No alert on `CircuitStaleOutcomes` is correct: stale outcomes rise with every
   trip (about one per call in flight). An alert that treats them as a fault on
   their own pages on every trip; flag it as noise.
6. With `RampRecovery` and `r8eotel.Trace`, dashboards keyed on
   `r8e.rejection_reason` should include `circuit_ramping`.
7. An empty name chosen on purpose for a policy built per job or request: it
   keeps the registry bounded, at the cost of health visibility — flag the
   trade-off.

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
- Reading per-request construction into a snippet that shows `NewPolicy` next to
  `Do`: without a visible handler, loop or per-tenant map, the lifecycle is
  UNKNOWN (NOTED #4), not BLOCKING #3.
- Flagging a hook that only calls a metrics or logging library built for
  concurrent use — that is the safe form.
- Treating the rise of `CircuitStaleOutcomes` after a trip as a fault — it is
  the expected trace of the calls in flight.
