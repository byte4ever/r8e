*[Lire en Français](README.fr.md)*

# Example 31 — Recover (panic → error)

Demonstrates `WithRecover`, which catches a panic from the user function and
turns it into a `*r8e.PanicError` instead of letting it crash the process — so
the rest of the resilience chain can retry it, fall back, or log it like any
other error.

## What it demonstrates

A single unrecovered panic deep in a handler normally takes the whole instance
down, killing every in-flight request with it. `WithRecover` wraps the innermost
call and converts the panic into an ordinary error value. The example walks
through three ways to use that:

1. **Panic → error.** A bare `WithRecover` policy catches the panic; the
   `OnPanic` hook fires, and the returned error unwraps to a `*PanicError`
   carrying the original panic value and the stack trace captured at recovery
   time.
2. **Panic + fallback.** Because the panic is now an error, `WithFallback`
   substitutes a safe default — the caller gets a usable value and a `nil`
   error, the panic absorbed end to end.
3. **Panic then retry.** A transient panic that clears on the next attempt is
   just another retryable failure. `WithRetry` re-runs the call and it succeeds;
   `PanicsRecovered` counts the recovery.

## Key concepts

| Concept | Detail |
|---|---|
| `WithRecover()` | Wraps the innermost call; converts any panic into a `*PanicError` instead of crashing |
| `ErrPanic` / `*PanicError` | Sentinel for `errors.Is`; the concrete error (via `errors.As`) carries `.Value` and `.Stack` |
| `OnPanic` hook | Fires at the moment of recovery — the place to wire metrics or alerting |
| `WithFallback` | Treats the recovered panic like any failed call and returns a default |
| `WithRetry` | Re-runs the call when the panic is transient; recover sits innermost so each attempt is wrapped |
| `Metrics().PanicsRecovered` | Counter incremented on every caught panic |

## When to use

- Calling into code you don't fully trust — third-party libraries, plugins, or
  handlers where a panic on bad input would otherwise crash the server.
- Any place where a single request's failure must not take down every other
  request sharing the process.
- When you want panics to flow through the same retry / fallback / circuit-breaker
  machinery as normal errors, rather than special-casing them.

## Run

```bash
go run ./examples/31-recover/
```

## Expected output

Three labelled scenarios. The first prints the caught panic value and the first
line of its stack trace; the second shows the fallback value with a `nil` error;
the third shows the panicking first attempt, the successful retry, and a
`panics_recovered=1` count. Output is deterministic (no randomness), except the
exact stack-trace line, which depends on the Go runtime.
