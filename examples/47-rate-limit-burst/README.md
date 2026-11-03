*[Lire en Français](README.fr.md)*

# Example 47 — Rate Limit Burst

Demonstrates `Burst`: it sets the size of the rate limiter's token bucket
apart from the rate, so you choose how many calls can leave at once.

## What it demonstrates

By default the bucket holds one second of tokens, and at least one, and it
starts full. At 10 req/s, 10 calls can leave at the same instant: on start-up,
or after a quiet second. `Burst(n)` sets the bucket to `n` tokens. Up to `n`
calls can leave at once, then the calls follow the rate.

The example drives its policies with a manual clock (`WithClock`), so the
output does not depend on the speed of the machine. It runs three phases:

1. **Default bucket**: with `WithRateLimit(10)`, 10 of 12 calls made at the
   same instant are admitted.
2. **`Burst(3)`**: with `WithRateLimit(10, r8e.Burst(3))`, 3 of the 12 calls
   are admitted. A token then comes back every 100ms, so each 100ms step
   admits one call of two.
3. **Reload**: a reload of the rate (`rate_limit` set to 20) keeps the burst.
   After a pause of one second, only 3 calls leave at once. A reload of
   `rate_limit_burst` set to 6 retunes it, and after the next pause 6 calls
   leave. The pauses let the bucket refill: a reload adds no tokens. A reload
   cannot add a burst to a policy built without one, so `Reconfigure` on the
   phase 1 policy returns `ErrPatternAbsent`.

## Key concepts

| Concept | Detail |
|---|---|
| `Burst(n)` | Bucket capacity of `n` tokens, apart from the rate. The bucket starts full, so `n` calls can leave at once on start-up |
| Default capacity | One second of tokens, and at least one, so a rate below 1 req/s still admits calls |
| Range | `n` between 1 and `r8e.MaxBurst`; any other value makes `NewPolicy` panic with `ErrRateLimitBurstOutOfRange` |
| `Reconfigure` and AIMD | They change the rate, never a capacity set by `Burst` |
| Rate of zero or less | Empties the bucket, `Burst` or not. A later positive rate brings the `Burst` capacity back, with an empty bucket |
| `rate_limit_burst` | JSON field, requires `rate_limit`. A reload can change the burst of a policy built with one, not add one (`ErrPatternAbsent`). A reload without the field keeps the current burst |
| Quota over a window | A bucket holds a quota only over its refill window: `WithRateLimit(0.125, r8e.Burst(450))` can let up to 900 calls through in an hour, while `Burst(1)` never goes past 451 |

## When to use

- A downstream that accepts only a few calls at once, even when its rate is
  higher.
- A quota counted over a sliding window: keep the burst small, as the table
  shows.
- A smooth start-up: `Burst(1)` spaces the calls out from the first one.

## Run

```bash
go run ./examples/47-rate-limit-burst/
```

## Expected output

`+` marks an admitted call, `x` a call rejected with `ErrRateLimited`.

```text
=== Phase 1: default bucket, one second of tokens ===
  12 calls at t=0: ++++++++++xx (10 admitted)

=== Phase 2: Burst(3), then the steady rate ===
  12 calls at t=0: +++xxxxxxxxx (3 admitted)
  +100ms, 2 calls: +x (1 admitted)
  +100ms, 2 calls: +x (1 admitted)
  +100ms, 2 calls: +x (1 admitted)

=== Phase 3: reload ===
  reload rate_limit=20: ok
  after 1s, 12 calls: +++xxxxxxxxx (3 admitted)
  reload rate_limit_burst=6: ok
  after 1s, 12 calls: ++++++xxxxxx (6 admitted)
  reload rate_limit_burst=6 on rl-default: r8e: cannot reconfigure a pattern the policy does not have: "rate_limit_burst"
```
