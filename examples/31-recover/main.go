// Package main demonstrates panic recovery via WithRecover.
//
// A single unrecovered panic in a request handler takes the whole process
// down — one bad input or a nil dereference deep in a dependency, and every
// in-flight request on that instance dies with it. WithRecover wraps the
// innermost call, catches the panic, and converts it to a *r8e.PanicError so
// the rest of the chain can treat it like any ordinary error: retry it, fall
// back to a safe default, or just log the captured stack trace for diagnosis.
//
// This example shows three scenarios:
//  1. A simple panic caught and returned as an error.
//  2. Recovering from a panic with a static fallback value.
//  3. Retrying after a panic that clears on the next attempt.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// --- Scenario 1: panic converted to error ---
	fmt.Println("=== Scenario 1: panic → error ===")

	// Just WithRecover here — no retry or fallback — so we can see the raw
	// recovered error. The OnPanic hook fires at the moment of recovery, before
	// the error is returned, which is the right place to wire metrics or alerting.
	basic := r8e.NewPolicy[string]("basic-recover",
		r8e.WithHooks(&r8e.Hooks{
			OnPanic: func(value any) {
				fmt.Printf("  [hook] panic caught: %v\n", value)
			},
		}),
		r8e.WithRecover(),
	)

	_, err := basic.Do(ctx, func(_ context.Context) (string, error) {
		panic("something went wrong in the backend")
	})

	// The panic is now a normal error value. errors.Is keys off the ErrPanic
	// sentinel; errors.As then unwraps the concrete *PanicError to reach the
	// original panic value and the stack trace captured at recovery time —
	// the same diagnostic detail you would have lost in a process crash.
	if errors.Is(err, r8e.ErrPanic) {
		var pe *r8e.PanicError

		errors.As(err, &pe) //nolint:errcheck // As always succeeds when Is returned true

		fmt.Printf("  caught: %v\n", pe.Value)
		fmt.Printf("  stack (first line): %s\n", firstLine(pe.Stack))
	}

	// --- Scenario 2: panic + fallback returns safe default ---
	fmt.Println("\n=== Scenario 2: panic + fallback ===")

	// Because the panic is turned into an error first, WithFallback can treat it
	// exactly like a failed call and substitute a safe default. The caller gets a
	// usable value and a nil error — the panic is absorbed end to end. Order
	// matters: recover must sit inside fallback so there is an error to fall back on.
	withFallback := r8e.NewPolicy[string]("recover-fallback",
		r8e.WithRecover(),
		r8e.WithFallback("default-value"),
	)

	result, err := withFallback.Do(ctx, func(_ context.Context) (string, error) {
		panic("unrecoverable state")
	})

	fmt.Printf("  result=%q err=%v\n", result, err)

	// --- Scenario 3: panic on first attempt, success on retry ---
	fmt.Println("\n=== Scenario 3: panic then retry ===")

	// A transient panic (a flaky dependency, a race that clears on a fresh call)
	// is just another retryable failure once recovered. With recover innermost,
	// each retry attempt gets its own recovery wrapper, so retry sees a clean
	// error and tries again rather than the panic escaping past it.
	withRetry := r8e.NewPolicy[string]("recover-retry",
		r8e.WithRecover(),
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
	)

	attempt := 0

	result, err = withRetry.Do(ctx, func(_ context.Context) (string, error) {
		attempt++

		if attempt == 1 {
			fmt.Printf("  attempt %d → panic\n", attempt)
			panic("transient failure")
		}

		fmt.Printf("  attempt %d → ok\n", attempt)

		return "success", nil
	})

	fmt.Printf("  result=%q err=%v\n", result, err)
	fmt.Printf("  panics_recovered=%d\n", withRetry.Metrics().PanicsRecovered)
}

// firstLine returns the first non-empty line of a byte slice (the stack header).
func firstLine(data []byte) []byte {
	for i, ch := range data {
		if ch == '\n' {
			return data[:i]
		}
	}

	return data
}
