# Example 16 — Convenience Do

Demonstrates the `r8e.Do` convenience function for one-off resilient calls
without creating a named policy.

## What it demonstrates

`r8e.Do[T]` creates an anonymous (unnamed) policy internally, executes the
function through it, and returns the result. The policy is not registered
with any `Registry` and is discarded after the call.

Three scenarios:

1. **Retry + timeout** — A function that fails twice before succeeding is
   wrapped with retry and timeout. The retries recover the transient failures
   and the result is returned.

2. **Fallback** — A function that always fails is wrapped with retry and a
   static fallback. After retries are exhausted, the fallback value is
   returned.

3. **No options (pass-through)** — Calling `Do` with no options creates a
   bare policy that simply passes through to the function. This is equivalent
   to calling the function directly.

## When to use

- One-off calls where creating and naming a policy isn't warranted.
- Quick prototyping or scripts where you want resilience without setup.
- Testing or benchmarking a function with different resilience options.

For production services, prefer `NewPolicy` with a name so the policy
registers in the health/readiness system.

## Key concepts

| Concept | Detail |
|---|---|
| `r8e.Do[T](ctx, fn, opts...)` | One-off resilient call without a named policy |
| Anonymous policy | Not registered in any `Registry`; no health reporting |
| Same options | Accepts the same `With*` options as `NewPolicy` |

## Run

```bash
go run ./examples/16-convenience-do/
```

## Expected output

```
=== One-off call with retry + timeout ===
  attempt 1
  attempt 2
  attempt 3
  result: "one-off success", err: <nil>

=== One-off call with fallback ===
  result: "emergency default", err: <nil>

=== One-off call with no options (pass-through) ===
  result: "bare call", err: <nil>
```
