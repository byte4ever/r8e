# r8e

Vocabulary of the r8e resilience library: the words its API, docs and
issues use for what a policy decides about a call.

## Language

**Cooldown**:
A period during which a policy refuses calls locally because a server
asked the caller to wait (a Retry-After hint).
_Avoid_: Retry-After barrier, quota gate, backoff window

**Local rejection**:
A call refused by one of r8e's own guards (breaker, rate limiter,
bulkhead, cooldown) before anything reaches the downstream.
_Avoid_: Shed call, client-side failure

**Neutral outcome**:
A breaker outcome that counts neither as a success nor as a failure.
_Avoid_: Ignored call, skipped outcome
