# Example 01 — Quickstart

Minimal end-to-end example of creating a resilience policy and executing a
function through it.

## What it demonstrates

- Creating a `Policy[string]` with `NewPolicy`, composing three patterns in a
  single call: **timeout**, **retry** (exponential backoff), and **circuit
  breaker**.
- Calling `policy.Do` to execute a function through the composed middleware
  chain.
- r8e automatically sorts patterns into the correct execution order regardless
  of the order you specify options.

## Key concepts

| Concept | Detail |
|---|---|
| `NewPolicy[T]` | Generic policy constructor — `T` is the return type of the wrapped function |
| `WithTimeout` | Cancels the call if it exceeds the given duration |
| `WithRetry` | Retries transient failures with the specified backoff strategy |
| `WithCircuitBreaker` | Fast-fails when the downstream is unhealthy |
| `policy.Do` | Executes `func(context.Context) (T, error)` through the middleware chain |

## Run

```bash
go run ./examples/01-quickstart/
```

## Expected output

```
result: Hello from r8e!
```
