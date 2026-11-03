# The cooldown checks at the chain entry and in the retry loop, not inside the breaker

A cooldown refuses calls until a server's Retry-After deadline. We check it in
two places: at the entry of the chain, after coalescing and before the timeout
and the circuit breaker, and in the retry loop, where a retry due while the
cooldown is armed is not made and the last error comes back as is. The breaker
therefore never sees a cooldown refusal. Its default classifier counts local
rejections as failures, so an hour of refusals reaching it would open it, and it
would stay open after the server's deadline had passed.

## Considered options

- **Inside the breaker, next to the rate limiter and the bulkhead.** Rejected:
  the breaker would count every refusal, unless every user wrote a classifier
  that makes `ErrCoolingDown` neutral.
- **As a breaker state ("open until").** Rejected: the breaker belongs to one
  policy and has a fixed recovery timeout, while a quota spans several routes
  (hence `WithSharedCooldown`) and the server gives the duration.
- **Entry check only.** Rejected: inside one call, the retry loop would still
  send its next attempt into the quota the first attempt just hit.

## Consequences

- The armed cooldown makes a policy degraded, never critical: a 429 on a shared
  dependency must not take every instance out of rotation at once.
- With a retry, the retry loop arms the cooldown from each attempt; without
  one, the entry middleware arms it from the call. One function decides which,
  so each hint arms the cooldown once.
- The retry loop checks the cooldown after each failed attempt, before the
  last-attempt exit, and again before a retry leaves, so a cooldown armed by
  another call during the backoff also stops it. The last error comes back as
  is, never wrapped with `ErrRetriesExhausted`.
- A shared cooldown excludes the policy's own, as a shared bulkhead does, and a
  policy reload does not change its cap: one route's reload must not retune the
  quota of the others. The shared retry and concurrency budgets keep their
  rule (last option wins, any policy's reload retunes them). `r8econf.GetPolicy`
  reports the conflict as an error.
