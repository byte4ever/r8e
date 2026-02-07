// Example 04-timeout: Demonstrates global timeout with context cancellation.
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

	policy := r8e.NewPolicy[string]("timeout-demo",
		r8e.WithTimeout(200*time.Millisecond),
	)

	// --- Fast call: completes within the timeout ---
	fmt.Println("=== Fast call (completes within timeout) ===")
	result, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
		return "fast response", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Slow call: exceeds the timeout ---
	fmt.Println("=== Slow call (exceeds 200ms timeout) ===")
	_, err = policy.Do(ctx, func(ctx context.Context) (string, error) {
		select {
		case <-time.After(1 * time.Second):
			return "this won't be reached", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if errors.Is(err, r8e.ErrTimeout) {
		fmt.Printf("  err: %v (timed out as expected)\n\n", err)
	}

	// --- Timeout distinguishes from parent context cancellation ---
	fmt.Println("=== Parent context cancelled ===")
	parentCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = policy.Do(parentCtx, func(ctx context.Context) (string, error) {
		select {
		case <-time.After(1 * time.Second):
			return "this won't be reached", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	fmt.Printf("  err: %v (parent cancelled, not timeout)\n", err)
}
