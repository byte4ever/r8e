# Example 12 — Hooks & Observability

Demonstrates all 12 lifecycle hooks available on `Hooks`, showing how they
fire during policy execution.

## What it demonstrates

A complete `Hooks` struct is configured with all available callbacks, then
three policies exercise different hook types:

### Retry hooks

A policy with retry + fallback runs a function that fails twice before
succeeding. The `OnRetry` hook fires on each retry attempt with the attempt
number and the error that triggered it.

### Bulkhead hooks

A policy with `WithBulkhead(1)` executes a single call. The `OnBulkheadAcquired`
hook fires when the slot is acquired, and `OnBulkheadReleased` fires when
the slot is released after completion.

### Fallback hooks

A policy with retry + fallback runs a function that always fails. After
retries are exhausted, the `OnFallbackUsed` hook fires with the final error
before the fallback value is returned.

## All available hooks

| Hook | When it fires |
|---|---|
| `OnRetry` | Before each retry attempt (with attempt number and error) |
| `OnCircuitOpen` | When the circuit breaker transitions to open state |
| `OnCircuitClose` | When the circuit breaker transitions back to closed state |
| `OnCircuitHalfOpen` | When the circuit breaker enters half-open (probe) state |
| `OnRateLimited` | When a request is rejected by the rate limiter |
| `OnBulkheadFull` | When a request is rejected because the bulkhead is at capacity |
| `OnBulkheadAcquired` | When a bulkhead slot is acquired |
| `OnBulkheadReleased` | When a bulkhead slot is released |
| `OnTimeout` | When a call exceeds its timeout deadline |
| `OnHedgeTriggered` | When a hedge delay elapses and a second call is fired |
| `OnHedgeWon` | When the hedge call completes before the primary |
| `OnFallbackUsed` | When the fallback is invoked (with the triggering error) |

## When to use

- Feed retry and circuit breaker events into metrics (Prometheus, StatsD).
- Log state transitions for debugging.
- Trigger alerts on circuit breaker opens or repeated fallback usage.

## Run

```bash
go run ./examples/12-hooks/
```
