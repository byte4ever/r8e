*[Lire en Français](README.fr.md)*

# Example 35 — Adaptive Timeout

Demonstrates a percentile-driven adaptive timeout that sizes each call's
deadline from the backend's own recent latency instead of a guessed constant.

## What it demonstrates

A policy is configured with `WithTimeout(1s, AdaptiveTimeout(...))`. Once enough
successful calls have been seen:

1. The deadline is computed from a sliding window of recent **successful**
   latencies as `clamp(p99 × multiplier, floor, ceiling)`.
2. The `1s` passed to `WithTimeout` becomes the hard **ceiling** and the warmup
   fallback — the adaptive value can only tighten below it, never exceed it.
3. With a steady ~10ms backend, the adaptive timeout settles around 20ms
   (`p99 × 2`), far below the 1s ceiling, so a genuine straggler is cut quickly.

Only **successful** calls feed the window, so a timeout firing can never inflate
the very percentile that set it. As the backend's latency drifts, the deadline
follows it — no redeploy to re-tune a constant.

## Key concepts

| Concept | Detail |
|---|---|
| `WithTimeout(ceiling, AdaptiveTimeout(...))` | The duration is the hard ceiling and warmup fallback, not the operating value |
| `AdaptiveTimeoutPercentile(0.99)` | Sizes the deadline off the p99 — the slowest ~1% of healthy calls |
| `AdaptiveTimeoutMultiplier(2.0)` | Headroom above the percentile so normal jitter never trips the timeout |
| `AdaptiveTimeoutFloor(20ms)` | Lower bound so an ultra-fast window can't collapse the timeout to near-zero |
| `Metrics().AdaptiveTimeout` | The deadline the policy would currently apply |

## When to use

- Backends whose latency varies or drifts over time, where any single fixed
  timeout is either too tight (cutting healthy calls) or too loose (letting
  stragglers tie up slots).
- Services where you already track a stable p99 and want the timeout to track
  it automatically rather than by periodic hand-tuning.
- Pair with a circuit breaker or retry budget so the tightened deadline turns
  stragglers into fast, contained failures.

## Run

```bash
go run ./examples/35-adaptive-timeout/
```

## Expected output

After the warmup, the observed p99 (~10ms), the adaptive timeout (~20ms, noted
as "was a 1s ceiling"), and the count of timeouts (typically 0). Exact
millisecond values vary slightly between runs.
