*[Lire en Français](README.fr.md)*

# Example 29 — Request Sheddability

Demonstrates per-call load-shedding priority with `WithSheddability`: when a
backend is overloaded, background work yields first while user-facing traffic
keeps getting through.

## What it demonstrates

A throttler that sheds blindly drops critical and deferrable requests with equal
probability — the wrong outcome, since the cheap, background work is what should
yield first. `WithSheddability` stamps each call's priority on its context, and
the adaptive throttler honours it:

1. **`SheddabilityAlways`** (background) — shed first as soon as any load
   shedding is active.
2. **`SheddabilityDefault`** (the zero value, no stamp) — shed at the normal SRE
   probability.
3. **`SheddabilityNever`** (critical, user-facing) — always admitted, even at
   maximum load.

The example bursts all three classes round-robin against a backend that is
**healthy**, then **overloaded**, then **recovered**. The pass rates make the
priority ordering visible: under load, background drops to near zero, default
drops partially, and critical stays at 100%.

## How it works

The adaptive throttler watches the success/failure ratio over a rolling window
and starts rejecting calls **locally** — before they ever leave the process —
once accepts outrun backend capacity (the Google SRE client-side throttling
model). The sheddability stamp decides *which* calls it rejects first. A
locally-shed call never runs `fn` and comes back as `ErrThrottled`, so the
absence of that error means the call reached the backend.

## Key concepts

| Concept | Detail |
|---|---|
| `WithSheddability(ctx, level)` | Stamps a call's shedding priority on its context |
| `SheddabilityNever` | Bypass — critical traffic, always admitted |
| `SheddabilityDefault` | Zero value — shed at normal SRE probability |
| `SheddabilityAlways` | Shed first — background or speculative work |
| `WithAdaptiveThrottle(...)` | Client-side throttler that reads the stamp; `OverloadRatio`, `MinRequests`, `ThrottleWindow`, `MaxRejectionRate` tune its sensitivity |
| `ErrThrottled` | Returned by a locally-shed call; `fn` was never invoked |

## When to use

- Mixed workloads where user-facing requests share a policy with background jobs
  (reindexing, prefetch, analytics) and you want the background work to absorb
  the overload.
- Speculative or best-effort calls that are safe to drop under pressure — mark
  them `SheddabilityAlways`.
- Critical paths (checkout, auth) that must reach the backend even during a
  brownout — mark them `SheddabilityNever`.

## Run

```bash
go run ./examples/29-sheddability/
```

## Expected output

Three labelled bursts. In the **healthy** burst all three classes reach the
backend. In the **overloaded** burst, background drops sharply (often to 0),
default drops partially, and critical stays at its full share; the local-shed
count is high. In the **recovered** burst shedding clears and all classes pass
again. Exact pass counts vary with timing because the throttler is
probabilistic.
