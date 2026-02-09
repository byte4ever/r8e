// Example 11-error-classification: Demonstrates Transient vs Permanent error
// classification and how it affects retry behavior.
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

	policy := r8e.NewPolicy[string]("error-class-demo",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond)),
	)

	// --- Transient error: retried until success ---
	fmt.Println("=== Transient Error (retried) ===")

	attempt := 0
	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 3 {
			return "", r8e.Transient(errors.New("connection reset"))
		}

		return "recovered", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Permanent error: stops retries immediately ---
	fmt.Println("=== Permanent Error (stops retries) ===")

	attempt = 0
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", r8e.Permanent(errors.New("invalid API key"))
	})
	fmt.Printf("  err: %v\n\n", err)

	// --- Unclassified error: treated as transient by default ---
	fmt.Println("=== Unclassified Error (treated as transient) ===")

	attempt = 0
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", errors.New("some unclassified error")
	})
	fmt.Printf("  err: %v (retried all 5 attempts)\n\n", err)

	// --- Classification checks ---
	fmt.Println("=== Classification Checks ===")

	transientErr := r8e.Transient(errors.New("timeout"))
	permanentErr := r8e.Permanent(errors.New("bad request"))
	plainErr := errors.New("unknown")

	fmt.Printf("  Transient(timeout): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(transientErr), r8e.IsPermanent(transientErr))
	fmt.Printf("  Permanent(bad request): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(permanentErr), r8e.IsPermanent(permanentErr))
	fmt.Printf("  Plain(unknown): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(plainErr), r8e.IsPermanent(plainErr))
}
