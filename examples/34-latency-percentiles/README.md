*[Lire en Français](README.fr.md)*

# Example 34 — Latency Percentiles

Demonstrates the always-on latency percentiles every policy records, exposing a
slow tail that a plain average would quietly average away.

## What it demonstrates

A bare policy is created with no resilience options at all — `WithTimeout`,
`WithHedge`, and the rest are absent. Yet the policy still measures itself:

1. Each `Do()` call's end-to-end duration is fed into a sliding-window
   DDSketch as a **side effect of the call** — there is no option to enable.
2. A backend is driven 200 times. It answers in ~10ms nine times out of ten,
   but the tenth call takes ~150ms — a deliberately skewed distribution.
3. `Metrics()` exposes the recent **p50/p95/p99**. The p50 reflects the typical
   fast call; the p99 keeps the rare slow call visible.

The lesson is the gap between p50 and p99. An average would blend the rare slow
call into the fast majority and report a deceptively healthy number, even though
real users land on the slow path one time in ten.

## Key concepts

| Concept | Detail |
|---|---|
| `Metrics().LatencyP50` | The typical call — the bulk of the distribution |
| `Metrics().LatencyP95` / `LatencyP99` | The tail — the slow calls an average hides |
| `Metrics().LatencySamples` | How many calls are in the current sliding window |
| Always-on instrumentation | No option to enable; the recording is free, mirroring resilience4j's per-call timers |

## When to use

- Any time you want a true picture of service time — alerting and SLOs should
  be driven by p99, not by a mean that masks the tail.
- Feeding dashboards or an adaptive timeout/hedge that tunes itself from the
  observed p99 (see examples 35 and 36).
- Low-traffic or low-ceremony policies where you want observability without
  wiring up a separate metrics pipeline.

## Run

```bash
go run ./examples/34-latency-percentiles/
```

## Expected output

The count of samples in the window, followed by p50/p95/p99. The p50 sits near
10ms while the p99 sits far above it (near the 150ms slow path). Exact values
vary because the slow calls land randomly.
