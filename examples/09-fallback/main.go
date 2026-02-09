// Example 09-fallback: Demonstrates static fallback and function fallback.
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

	// --- Static fallback ---
	fmt.Println("=== Static Fallback ===")

	staticPolicy := r8e.NewPolicy[string]("static-fallback",
		r8e.WithFallback("default value"),
	)

	result, err := staticPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("service unavailable")
		},
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Function fallback ---
	fmt.Println("=== Function Fallback ===")

	funcPolicy := r8e.NewPolicy[string]("func-fallback",
		r8e.WithFallbackFunc(func(err error) (string, error) {
			return fmt.Sprintf("fallback computed from error: %v", err), nil
		}),
	)

	result, err = funcPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("database connection refused")
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Fallback with function that also fails ---
	fmt.Println("=== Fallback Function That Also Fails ===")

	failingFbPolicy := r8e.NewPolicy[string]("failing-fallback",
		r8e.WithFallbackFunc(func(err error) (string, error) {
			return "", fmt.Errorf("fallback also failed: %w", err)
		}),
	)

	_, err = failingFbPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("primary failed")
	})
	fmt.Printf("  err: %v\n\n", err)

	// --- Successful call ignores fallback ---
	fmt.Println("=== Successful Call (fallback not used) ===")

	result, err = staticPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "primary success", nil
	})
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
