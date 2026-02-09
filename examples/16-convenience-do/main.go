// Example 16-convenience-do: Demonstrates the r8e.Do convenience function
// for one-off resilient calls without creating a named policy.
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

	// --- Simple one-off call with retry and timeout ---
	fmt.Println("=== One-off call with retry + timeout ===")

	attempt := 0
	result, err := r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			attempt++
			fmt.Printf("  attempt %d\n", attempt)

			if attempt < 3 {
				return "", r8e.Transient(errors.New("temporary glitch"))
			}

			return "one-off success", nil
		},
		r8e.WithTimeout(2*time.Second),
		r8e.WithRetry(3, r8e.ExponentialBackoff(50*time.Millisecond)),
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- One-off call with fallback ---
	fmt.Println("=== One-off call with fallback ===")

	result, err = r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("service unavailable")
		},
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency default"),
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- One-off call with no options (pass-through) ---
	fmt.Println("=== One-off call with no options (pass-through) ===")

	result, err = r8e.Do[string](ctx,
		func(_ context.Context) (string, error) {
			return "bare call", nil
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
