*[Lire en Francais](README.fr.md)*

# Example 15 — Presets

Demonstrates ready-made option bundles for common use cases, so you don't
have to configure every pattern from scratch.

## What it demonstrates

### StandardHTTPClient

`r8e.StandardHTTPClient()` returns a `[]any` option slice with:

- **Timeout:** 5 seconds
- **Retry:** 3 attempts, 100ms exponential backoff
- **Circuit breaker:** 5 failures threshold, 30-second recovery

Suitable for general-purpose HTTP clients where moderate retries and a
conservative circuit breaker are appropriate.

### AggressiveHTTPClient

`r8e.AggressiveHTTPClient()` returns options with:

- **Timeout:** 2 seconds
- **Retry:** 5 attempts, 50ms exponential backoff, 5-second max delay cap
- **Circuit breaker:** 3 failures threshold, 15-second recovery
- **Bulkhead:** 20 concurrent calls

Suitable for latency-sensitive services that need faster failure detection,
more retry attempts, and concurrency protection.

### Usage

Presets return `[]any`, which is spread into `NewPolicy` with `...`:

```go
policy := r8e.NewPolicy[string]("my-api", r8e.StandardHTTPClient()...)
```

You can append additional options after the preset to customize further:

```go
opts := append(r8e.StandardHTTPClient(), r8e.WithFallback("default"))
policy := r8e.NewPolicy[string]("my-api", opts...)
```

## Key concepts

| Concept | Detail |
|---|---|
| `StandardHTTPClient()` | Conservative preset: 5s timeout, 3 retries, CB(5, 30s) |
| `AggressiveHTTPClient()` | Aggressive preset: 2s timeout, 5 retries, CB(3, 15s), bulkhead(20) |
| Composable | Presets return `[]any` — spread and extend with additional options |

## Run

```bash
go run ./examples/15-presets/
```
