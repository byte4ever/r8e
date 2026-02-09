// Example 03-circuit-breaker: Demonstrates the circuit breaker opening after
// failures and recovering through the half-open state.
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

	policy := r8e.NewPolicy[string]("circuit-breaker-demo",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(3),
			r8e.RecoveryTimeout(500*time.Millisecond),
			r8e.HalfOpenMaxAttempts(1),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:     func() { fmt.Println("  [hook] circuit breaker OPENED") },
			OnCircuitHalfOpen: func() { fmt.Println("  [hook] circuit breaker HALF-OPEN") },
			OnCircuitClose:    func() { fmt.Println("  [hook] circuit breaker CLOSED") },
		}),
	)

	callCount := 0
	shouldFail := true

	call := func(_ context.Context) (string, error) {
		callCount++
		if shouldFail {
			return "", fmt.Errorf("downstream error #%d", callCount)
		}

		return fmt.Sprintf("success (call #%d)", callCount), nil
	}

	// Phase 1: Trigger 3 failures to open the breaker.
	fmt.Println("=== Phase 1: Triggering failures to open the breaker ===")

	for range 4 {
		result, err := policy.Do(ctx, call)
		if err != nil {
			fmt.Printf("  call #%d error: %v\n", callCount, err)

			if errors.Is(err, r8e.ErrCircuitOpen) {
				fmt.Println("  (circuit is open — call was rejected)")
			}
		} else {
			fmt.Printf("  call #%d result: %s\n", callCount, result)
		}
	}

	// Phase 2: Wait for recovery timeout, then the breaker enters half-open.
	fmt.Println("\n=== Phase 2: Waiting for recovery timeout (500ms) ===")
	time.Sleep(600 * time.Millisecond)

	// Downstream recovers.
	shouldFail = false

	// Phase 3: Half-open probe succeeds, breaker closes.
	fmt.Println("\n=== Phase 3: Half-open probe (downstream recovered) ===")

	result, err := policy.Do(ctx, call)
	if err != nil {
		fmt.Printf("  call error: %v\n", err)
	} else {
		fmt.Printf("  call result: %s\n", result)
	}

	// Phase 4: Breaker is closed, calls flow normally.
	fmt.Println("\n=== Phase 4: Breaker closed, normal operation ===")

	result, err = policy.Do(ctx, call)
	if err != nil {
		fmt.Printf("  call error: %v\n", err)
	} else {
		fmt.Printf("  call result: %s\n", result)
	}
}
