# Example 05 — Rate Limiter

Demonstrates the token-bucket rate limiter in both **reject** and **blocking**
modes.

## What it demonstrates

### Reject mode (default)

A rate limiter configured at 5 tokens/second receives 8 rapid-fire requests.
The first ~5 succeed (consuming the available burst), and the remaining
requests are immediately rejected with `ErrRateLimited`. No waiting occurs —
excess traffic is shed instantly.

### Blocking mode

The same rate (5 tokens/second) is configured with `RateLimitBlocking()`.
Instead of rejecting excess requests, the limiter blocks until a token becomes
available. The first ~5 requests complete instantly; the remaining requests are
delayed until new tokens are replenished (one every 200ms at 5/sec).

## Key concepts

| Concept | Detail |
|---|---|
| `WithRateLimit(rate)` | Token-bucket limiter allowing `rate` requests per second |
| `RateLimitBlocking()` | Option to block (wait for a token) instead of rejecting |
| `ErrRateLimited` | Sentinel error returned in reject mode when no tokens are available |
| Token bucket | Tokens accumulate at `rate/sec`; burst capacity equals the rate |

## When to use each mode

- **Reject mode** — API gateways, load shedding, or when callers can retry
  later. Gives immediate feedback.
- **Blocking mode** — Background workers, batch processors, or internal
  pipelines where you want to smooth throughput rather than drop requests.

## Run

```bash
go run ./examples/05-rate-limiter/
```

## Expected output

Reject mode shows some requests succeeding and others getting `RATE LIMITED`.
Blocking mode shows all requests eventually succeeding, with later ones
delayed by the token replenishment interval.
