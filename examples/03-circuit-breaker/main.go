// Example 03-circuit-breaker: the breaker's full open/half-open/closed cycle.
//
// When a downstream is genuinely down, retrying every call just wastes time and
// piles load onto a service that can't answer — and ties up the caller's own
// resources waiting. A circuit breaker watches the failure rate and, once a
// dependency looks unhealthy, "trips open" to reject calls instantly with
// ErrCircuitOpen instead of attempting them. After a cooldown it lets a single
// probe through (half-open) to test recovery, then closes again if the probe
// succeeds. This example drives all four phases and uses hooks to log every
// state transition.
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

	// Tune the breaker tightly so the demo is short and deterministic: open after
	// 3 consecutive failures, stay open for 500ms before probing, and require
	// just 1 successful probe to close. In production these numbers would be
	// looser and chosen from real traffic. The hooks fire on each transition,
	// which is the idiomatic place to emit metrics or alerts when a dependency
	// trips or recovers.
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

	// shouldFail is our hand on the downstream's health: flipping it to false
	// later simulates the dependency recovering. callCount just numbers the calls
	// so the output is easy to follow.
	callCount := 0
	shouldFail := true

	call := func(_ context.Context) (string, error) {
		callCount++
		if shouldFail {
			return "", fmt.Errorf("downstream error #%d", callCount)
		}

		return fmt.Sprintf("success (call #%d)", callCount), nil
	}

	// Phase 1: Drive the breaker open. We loop 4 times: the first 3 calls reach
	// the downstream and fail, hitting FailureThreshold(3) and tripping the
	// breaker open. The 4th call never reaches the downstream at all — it is
	// rejected immediately with ErrCircuitOpen, which is the whole point: once
	// open, the breaker stops wasting effort on a dead dependency.
	fmt.Println("=== Phase 1: Triggering failures to open the breaker ===")

	for range 4 {
		result, err := policy.Do(ctx, call)
		if err != nil {
			fmt.Printf("  call #%d error: %v\n", callCount, err)

			// Distinguish a real downstream failure from a fast-fail rejection so
			// the output makes the open state visible.
			if errors.Is(err, r8e.ErrCircuitOpen) {
				fmt.Println("  (circuit is open — call was rejected)")
			}
		} else {
			fmt.Printf("  call #%d result: %s\n", callCount, result)
		}
	}

	// Phase 2: Give the breaker its cooldown. We sleep 600ms — comfortably past
	// the 500ms RecoveryTimeout — so the breaker becomes eligible to half-open and
	// allow a probe on the next call. (The transition to half-open is lazy: it
	// happens on the next Do, not on a timer.)
	fmt.Println("\n=== Phase 2: Waiting for recovery timeout (500ms) ===")
	time.Sleep(600 * time.Millisecond)

	// Flip the simulated downstream back to healthy so the upcoming probe will
	// actually succeed.
	shouldFail = false

	// Phase 3: Fire the recovery probe. This call is admitted as the half-open
	// probe; because the downstream now succeeds, the breaker closes and normal
	// operation resumes. Had it failed, the breaker would snap straight back to
	// open and wait out another cooldown.
	fmt.Println("\n=== Phase 3: Half-open probe (downstream recovered) ===")

	result, err := policy.Do(ctx, call)
	if err != nil {
		fmt.Printf("  call error: %v\n", err)
	} else {
		fmt.Printf("  call result: %s\n", result)
	}

	// Phase 4: Back to closed, so calls flow straight through to the (now healthy)
	// downstream with no gatekeeping — confirming the breaker has fully recovered.
	fmt.Println("\n=== Phase 4: Breaker closed, normal operation ===")

	result, err = policy.Do(ctx, call)
	if err != nil {
		fmt.Printf("  call error: %v\n", err)
	} else {
		fmt.Printf("  call result: %s\n", result)
	}
}
