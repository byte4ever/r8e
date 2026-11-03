# r8e

Vocabulary of the r8e resilience library: the words its API, docs and
issues use for what a policy decides about a call.

## Language

**Cooldown**:
A period during which a policy refuses calls locally because a server
asked the caller to wait (a Retry-After hint).
_Avoid_: Retry-After barrier, quota gate, backoff window

**Local rejection**:
A call refused before reaching the downstream by a guard that protects the
client's own capacity or a server's stated limit: the rate limiter, the
bulkhead and its queue, the adaptive concurrency limiter, the cooldown. A
refusal by the breaker, the throttler or the SLO governor is not one: it
reports on the downstream's health.
_Avoid_: Shed call, client-side failure

**Neutral outcome**:
A breaker outcome that counts neither as a success nor as a failure.
_Avoid_: Ignored call, skipped outcome
