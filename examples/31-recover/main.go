// Package main demonstrates panic recovery via WithRecover.
//
// A user function that panics would normally crash the process. WithRecover
// catches the panic and converts it to a *r8e.PanicError so callers can
// handle it like any other error — including retrying, falling back, or logging
// the stack trace for diagnosis.
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

	if errors.Is(err, r8e.ErrPanic) {
		var pe *r8e.PanicError

		errors.As(err, &pe) //nolint:errcheck // As always succeeds when Is returned true

		fmt.Printf("  caught: %v\n", pe.Value)
		fmt.Printf("  stack (first line): %s\n", firstLine(pe.Stack))
	}

	// --- Scenario 2: panic + fallback returns safe default ---
	fmt.Println("\n=== Scenario 2: panic + fallback ===")

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
