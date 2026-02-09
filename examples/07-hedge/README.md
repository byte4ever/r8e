# Example 07 — Hedged Requests

Demonstrates hedged (speculative) requests that reduce tail latency by
racing a second concurrent call against the primary.

## What it demonstrates

A policy is configured with `WithHedge(100ms)`. For each call:

1. The **primary** call starts immediately.
2. If the primary hasn't responded within **100ms**, a **hedge** (second
   concurrent call) is launched with the same function.
3. The **first response to arrive wins** — the other call's context is
   cancelled.

The example simulates a service with random latency between 50ms and 300ms.
Over 5 calls, you can observe:

- **Fast primary** (< 100ms) — the hedge never fires; the primary result is
  returned immediately.
- **Slow primary** (> 100ms) — the hedge fires (logged by `OnHedgeTriggered`).
  If the hedge completes first, `OnHedgeWon` fires. Either way, the fastest
  response is returned.

## Key concepts

| Concept | Detail |
|---|---|
| `WithHedge(delay)` | Launches a second call if the primary hasn't responded within `delay` |
| `OnHedgeTriggered` | Hook fired when the hedge delay elapses and a second call starts |
| `OnHedgeWon` | Hook fired when the hedge call completes before the primary |
| Context cancellation | The losing call's context is cancelled to free resources |

## When to use

- Services with high tail latency (p99 >> p50) where occasional slow calls
  dominate user experience.
- Read-only or idempotent operations — hedging non-idempotent writes can cause
  side effects.
- DNS lookups, cache reads, or stateless API calls where the cost of a
  redundant request is low.

## Run

```bash
go run ./examples/07-hedge/
```

## Expected output

Five calls with varying latency. Some trigger the hedge; some don't. Output
varies due to randomized latency.
