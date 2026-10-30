# Checklist — observability / ops axis

Per-category checks for the policy's observability and operability. Each check is
phrased so a "yes" to the *hazard* is an observation to score. Enumerate; do not
pre-dismiss.

## Naming & health registration
- [ ] Is the policy built with `NewPolicy(name, …)` and a NON-empty, meaningful
      name? A name auto-registers it for health reporting.
- [ ] Is the name empty `""`? Then the policy joins no registry
      (`policy.Registry()` is nil): no health visibility. A placeholder name
      registers but carries no meaningful identity.
- [ ] Is the call an anonymous `r8e.Do(…)` (no policy object)? Then NOTHING is
      registered → no health visibility at all.
- [ ] Can an operator find this policy's health by name when triaging an incident?

## Registry lifecycle (named policies built repeatedly)
- [ ] Where is the policy built: once at startup and shared, or per job /
      tenant / request / test? If the artifact does not show it, it is UNKNOWN; a
      snippet with `NewPolicy` next to `Do` is no evidence of per-request
      construction.
- [ ] Built repeatedly with a non-empty name: is each one removed when done
      (`defer policy.Registry().Unregister(policy)`, which works whatever
      registry it joined), built with an empty name, or registered in a
      `WithRegistry` registry dropped with them? Otherwise the registry grows
      without bound: memory, and `Health` / `CheckReadiness` / `Snapshot`
      iterate and list policies that no longer run.
- [ ] A dead policy keeps reporting its last state: a breaker that was open when
      its job ended stays open, since no call arrives to probe it. With
      `WithReadinessImpact()`, `/readyz` stays NotReady; without it, the
      aggregate health stays "unhealthy".
- [ ] `Unregister` matches by identity: another policy with the same name stays.
      Is removal attempted by name, or through a new policy with the same name?

## Hooks wired to the failure modes the policy has
- [ ] Enumerate the failure modes the policy ACTUALLY has: breaker / retry /
      hedge / fallback / bulkhead / rate-limit / SLO / throttle / cache.
- [ ] Is `WithHooks(&Hooks{…})` present at all?
- [ ] For each present mode, is the matching hook wired? Breaker →
      `OnCircuitOpen`; retry → `OnRetry`; hedge → `OnHedge`; fallback →
      `OnFallbackUsed`; bulkhead → `OnBulkheadRejected`; etc.
- [ ] A present failure mode with NO hook is blind to its own events — the event
      reaches no log/alert. (NOT "no metrics": metrics counters increment
      automatically. The gap is hooks→logging/alerting.)
- [ ] Do the wired hooks reach an alertable sink (metrics counter, structured
      log), or are they no-ops / `fmt.Println`?

## Hook safety (concurrency, after Do returned)
- [ ] Hooks run on the goroutine of their event: they may be called
      concurrently, and may run after `Do` returned (`WithTimeout`,
      `WithCoalesce`, `RefreshAhead`, the losing attempt of `WithHedge`). Does a
      hook mutate state of its own without synchronisation: a plain counter
      (`n++`), a map, a slice, a struct field?
- [ ] Does a hook touch request-scoped state (a request logger's fields, a span,
      a response writer) that may be gone when it runs?
- [ ] A hook that only calls a metrics or logging library built for concurrent
      use is fine.

## Breaker outcomes the classifier sets aside
- [ ] With a `CircuitBreakerClassifier`: is the neutral count surfaced
      (`OnCircuitNeutral`, or an alert on `CircuitNeutralOutcomes` /
      `r8e.policy.circuit_neutral_outcomes`)? A neutral outcome feeds neither the
      failure counter nor the slow-call window, so a downstream the callers keep
      abandoning, or a steady stream of local rejections, never shows in the
      breaker: the neutral count is the only trace.
- [ ] `CircuitStaleOutcomes` / `OnCircuitStaleOutcome`: each trip adds about as
      many stale outcomes as calls were in flight, whatever the recovery
      timeout. Does an alert treat them as a fault on their own (it would page
      on every trip)? A stale outcome counts once, as stale, even when it is
      neutral: the two counters do not overlap.

## Metrics are automatic (record, don't mis-frame)
- [ ] Note that r8e collects per-pattern metric counters AUTOMATICALLY, even with
      no `WithHooks`. So "the policy emits no metrics" is the WRONG finding.
- [ ] The real question: are those counters wired to alerting, and are the
      event-level hooks (open/retry/fallback) surfaced for paging?

## Readiness impact (the red-line category)
- [ ] Is `WithReadinessImpact()` present?
- [ ] If present: is the gated dependency SHARED (third-party / fleet-common /
      hit by every replica) or DEDICATED (owned, its loss should pull THIS pod)?
- [ ] SHARED + readiness-gated = fleet-wide flip: one downstream blip trips every
      replica's `/readyz` together → the whole service goes NotReady at once
      (self-inflicted mass outage). BLOCKING.
- [ ] DEDICATED dep whose loss should pull the pod but readiness is NOT gated →
      the pod reports Ready while broken (the opposite miss; IMPORTANT).
- [ ] Default reminder: an open breaker is REPORTED but does NOT pull the pod
      unless `WithReadinessImpact()` is set. Informational ≠ readiness-gating.

## OTel wiring
- [ ] Does the service context mention an OTel / OpenTelemetry / metrics stack?
- [ ] If so, is `r8eotel.Register(meter, reg)` present (metrics bridge)?
- [ ] Is `r8eotel.Trace(policy, tp)` present (tracing bridge) where spans matter?
- [ ] Absence in a non-OTel shop is at most NOTED; absence where the context says
      "we run OTel" is a real gap.
- [ ] `r8eotel.Register` exports `r8e.policy.circuit_neutral_outcomes` and
      `r8e.policy.circuit_stale_outcomes` with the other counters. With
      `RampRecovery`, do dashboards or alerts keyed on `r8e.rejection_reason`
      include `circuit_ramping` (a call shed during ramp recovery)?

## Config-vs-code & hot reload
- [ ] Which chosen patterns are CODE-ONLY (carry a func/closure): `WithCoalesce`,
      `WithCache`, `WithChaos`, all classifiers (`RetryIf`, `ThrottleClassifier`,
      `AIMDClassifier`, `SLOClassifier`, `CircuitBreakerClassifier`),
      `WithFallbackFunc`, `Parent`/shared-budget links, `ChaosEnabled`?
- [ ] When a `PolicyConfig` JSON is emitted, those patterns are SILENTLY ABSENT —
      is that acknowledged, or will an operator think the JSON is the whole policy?
- [ ] Do operators expect to RETUNE knobs at runtime without redeploy? If so, are
      the retunable params config-expressible (`r8econf`)?
- [ ] `Reconfigure` can only retune patterns the policy ALREADY has — it CANNOT
      add or remove a pattern (configuring an absent one → `ErrPatternAbsent`). Is
      anyone relying on `Reconfigure` to introduce a pattern?

## Chaos kill-switch (the second red line)
- [ ] Is `WithChaos(…)` present?
- [ ] If so, is there a `ChaosEnabled` kill-switch so the injected fault can be
      turned OFF in production WITHOUT a redeploy?
- [ ] `WithChaos` is code-only and `ChaosEnabled` is the off switch — shipping
      chaos with no kill-switch in a production policy is BLOCKING.

## Alertability of the modes that matter
- [ ] Of the failure modes that would page someone (breaker open, SLO shed,
      fallback storm), which are alertable today via wired hooks/metrics?
- [ ] Which are silent — the event happens but nothing surfaces it?

## Red lines (any one is BLOCKING on its own)
- `WithReadinessImpact()` on a SHARED / third-party dependency → a single
  downstream blip flips the whole fleet's `/readyz` at once (mass outage).
- `WithChaos(…)` shipped with NO `ChaosEnabled` kill-switch in a production
  policy → an injected fault cannot be disabled without a redeploy.
- A named policy visibly built per inbound request and never removed from its
  registry → the registry grows with traffic for the life of the process.
- A hook visibly writing a plain Go map → concurrent hook calls crash the
  process (`concurrent map writes` is fatal, not recoverable).
