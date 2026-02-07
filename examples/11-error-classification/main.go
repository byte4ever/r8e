// Example 11-error-classification: Demonstrates Transient vs Permanent error
// classification and how it affects retry behavior.
package main

import (
	"context"
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
	result, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 3 {
			return "", r8e.Transient(fmt.Errorf("connection reset"))
		}
		return "recovered", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Permanent error: stops retries immediately ---
	fmt.Println("=== Permanent Error (stops retries) ===")
	attempt = 0
	_, err = policy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		return "", r8e.Permanent(fmt.Errorf("invalid API key"))
	})
	fmt.Printf("  err: %v\n\n", err)

	// --- Unclassified error: treated as transient by default ---
	fmt.Println("=== Unclassified Error (treated as transient) ===")
	attempt = 0
	_, err = policy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		return "", fmt.Errorf("some unclassified error")
	})
	fmt.Printf("  err: %v (retried all 5 attempts)\n\n", err)

	// --- Classification checks ---
	fmt.Println("=== Classification Checks ===")
	transientErr := r8e.Transient(fmt.Errorf("timeout"))
	permanentErr := r8e.Permanent(fmt.Errorf("bad request"))
	plainErr := fmt.Errorf("unknown")

	fmt.Printf("  Transient(timeout): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(transientErr), r8e.IsPermanent(transientErr))
	fmt.Printf("  Permanent(bad request): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(permanentErr), r8e.IsPermanent(permanentErr))
	fmt.Printf("  Plain(unknown): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(plainErr), r8e.IsPermanent(plainErr))
}
