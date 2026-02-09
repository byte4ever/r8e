# Example 10 — Full Policy

Demonstrates composing all resilience patterns into a single policy. r8e
automatically sorts patterns into the correct execution order regardless of
the order options are specified.

## What it demonstrates

A single policy is created with every available pattern:

- **Fallback** — static value as a last resort
- **Timeout** — 2-second global deadline
- **Circuit breaker** — opens after 3 failures, 10-second recovery
- **Rate limiter** — 100 requests/second
- **Bulkhead** — 10 concurrent calls
- **Retry** — 3 attempts with exponential backoff
- **Hedge** — fire a second call after 50ms
- **Hooks** — observability callbacks for retry, timeout, and fallback

Three scenarios run through the composed policy:

1. **Successful call** — all patterns pass through transparently. The
   function's result is returned directly.

2. **Failing call** — the function always fails. Retries are exhausted (hooks
   log each attempt), then the fallback provides the final value.

3. **Retry + fallback on a fresh policy** — demonstrates that fallback
   catches the error after retries are exhausted.

## Execution order

Patterns are auto-sorted by priority. The outermost middleware executes first:

```
Fallback → Timeout → Circuit Breaker → Rate Limiter → Bulkhead → Retry → Hedge → fn()
```

This ordering ensures:
- Fallback catches everything
- Timeout bounds the total execution time
- Circuit breaker prevents calls to an unhealthy downstream
- Rate limiter and bulkhead protect shared resources
- Retry and hedge are innermost — they retry/race the actual function

## Run

```bash
go run ./examples/10-full-policy/
```

## Expected output

A successful call returns the result directly. A failing call shows retry
hooks firing, then the fallback value being returned.
