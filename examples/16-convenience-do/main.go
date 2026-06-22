// Example 16-convenience-do: Demonstrates the r8e.Do convenience function
// for one-off resilient calls without creating a named policy.
//
// Normally you build a named Policy once and reuse it, so it can register in
// the health/readiness system and amortize setup. But for a throwaway call —
// a script, a prototype, a one-shot startup probe — that ceremony is overkill.
// r8e.Do wraps a function in an anonymous policy on the spot, runs it through
// the same retry/timeout/fallback machinery, and discards the policy after.
// This example walks three shapes: retry+timeout, fallback, and bare pass-through.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// Scenario 1: the function fails twice then succeeds — exactly the shape
	// of a flaky dependency. We mark the early failures as Transient so retry
	// knows they are worth retrying, and cap the total wall time with a
	// timeout so a hung dependency can't block us forever. Without the retry,
	// the first transient glitch would surface as an error to the caller.
	fmt.Println("=== One-off call with retry + timeout ===")

	attempt := 0
	result, err := r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			attempt++
			fmt.Printf("  attempt %d\n", attempt)

			if attempt < 3 {
				// Transient marks the error as retriable; a plain error
				// would short-circuit retry as permanent.
				return "", r8e.Transient(errors.New("temporary glitch"))
			}

			return "one-off success", nil
		},
		// Same With* options as a named policy. Exponential backoff spaces
		// out the retries so we don't hammer a struggling dependency.
		r8e.WithTimeout(2*time.Second),
		r8e.WithRetry(3, r8e.ExponentialBackoff(50*time.Millisecond)),
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Scenario 2: the dependency is down for good. Retry will burn its
	// budget and still fail, so we pair it with a static fallback — a
	// degraded-but-usable default. The fallback turns an exhausted-retry
	// error into a successful (nil error) return, so the caller keeps
	// working instead of propagating the outage upward.
	fmt.Println("=== One-off call with fallback ===")

	result, err = r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("service unavailable")
		},
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency default"),
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Scenario 3: Do with no options. The anonymous policy has nothing to
	// add, so it is a transparent pass-through — equivalent to calling the
	// function directly. Useful when resilience options are computed
	// dynamically and may legitimately be empty: the call site stays uniform.
	fmt.Println("=== One-off call with no options (pass-through) ===")

	result, err = r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			return "bare call", nil
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
