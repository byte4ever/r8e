// Example 02-retry: Demonstrates all backoff strategies, MaxDelay,
// PerAttemptTimeout, and RetryIf.
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

	// --- Constant backoff ---
	fmt.Println("=== Constant Backoff ===")
	attempt := 0
	p := r8e.NewPolicy[string]("constant",
		r8e.WithRetry(3, r8e.ConstantBackoff(100*time.Millisecond)),
	)
	result, err := p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 3 {
			return "", r8e.Transient(fmt.Errorf("temporary failure"))
		}
		return "success on attempt 3", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Exponential backoff with MaxDelay ---
	fmt.Println("=== Exponential Backoff + MaxDelay ===")
	attempt = 0
	p = r8e.NewPolicy[string]("exponential",
		r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond),
			r8e.MaxDelay(500*time.Millisecond),
		),
	)
	result, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 4 {
			return "", r8e.Transient(fmt.Errorf("still failing"))
		}
		return "success on attempt 4", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Linear backoff ---
	fmt.Println("=== Linear Backoff ===")
	attempt = 0
	p = r8e.NewPolicy[string]("linear",
		r8e.WithRetry(3, r8e.LinearBackoff(100*time.Millisecond)),
	)
	result, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 2 {
			return "", r8e.Transient(fmt.Errorf("one more try"))
		}
		return "success on attempt 2", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Exponential jitter backoff ---
	fmt.Println("=== Exponential Jitter Backoff ===")
	attempt = 0
	p = r8e.NewPolicy[string]("jitter",
		r8e.WithRetry(4, r8e.ExponentialJitterBackoff(50*time.Millisecond)),
	)
	result, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 3 {
			return "", r8e.Transient(fmt.Errorf("jittery failure"))
		}
		return "success on attempt 3", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Permanent error stops retries ---
	fmt.Println("=== Permanent Error (stops retries) ===")
	attempt = 0
	p = r8e.NewPolicy[string]("permanent",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond)),
	)
	_, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		return "", r8e.Permanent(fmt.Errorf("invalid input"))
	})
	fmt.Printf("  err: %v (only 1 attempt, stopped by Permanent)\n\n", err)

	// --- PerAttemptTimeout ---
	fmt.Println("=== PerAttemptTimeout ===")
	attempt = 0
	p = r8e.NewPolicy[string]("per-attempt-timeout",
		r8e.WithRetry(3, r8e.ConstantBackoff(50*time.Millisecond),
			r8e.PerAttemptTimeout(100*time.Millisecond),
		),
	)
	result, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		if attempt < 3 {
			// Simulate a slow call that exceeds per-attempt timeout.
			select {
			case <-time.After(200 * time.Millisecond):
				return "too slow", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "fast response", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- RetryIf predicate ---
	fmt.Println("=== RetryIf Predicate ===")
	errNotFound := errors.New("not found")
	attempt = 0
	p = r8e.NewPolicy[string]("retry-if",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond),
			r8e.RetryIf(func(err error) bool {
				// Only retry if the error is NOT "not found".
				return !errors.Is(err, errNotFound)
			}),
		),
	)
	_, err = p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)
		return "", fmt.Errorf("wrap: %w", errNotFound)
	})
	fmt.Printf("  err: %v (stopped by RetryIf on attempt 1)\n", err)
}
