# Checklist — observability / ops axis

Per-category checks for the policy's observability and operability. Each check is
phrased so a "yes" to the *hazard* is an observation to score. Enumerate; do not
pre-dismiss.

## Naming & health registration
- [ ] Is the policy built with `NewPolicy(name, …)` and a NON-empty, meaningful
      name? A name auto-registers it for health reporting.
- [ ] Is the name empty `""` or a placeholder? Then it is effectively anonymous —
      no meaningful identity in the health registry.
- [ ] Is the call an anonymous `r8e.Do(…)` (no policy object)? Then NOTHING is
      registered → no health visibility at all.
- [ ] Can an operator find this policy's health by name when triaging an incident?

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

## Config-vs-code & hot reload
- [ ] Which chosen patterns are CODE-ONLY (carry a func/closure): `WithCoalesce`,
      `WithCache`, `WithChaos`, all classifiers (`RetryIf`, `ThrottleClassifier`,
      `AIMDClassifier`, `SLOClassifier`), `WithFallbackFunc`, `Parent`/shared-budget
      links, `ChaosEnabled`?
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
