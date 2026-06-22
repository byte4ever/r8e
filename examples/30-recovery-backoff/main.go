// Package main demonstrates adaptive recovery backoff for the circuit breaker.
//
// Without backoff, a tripped circuit attempts a half-open probe every
// recoveryTimeout regardless of how many probes have already failed. With
// RecoveryBackoffMultiplier each failed probe doubles the wait before the next
// probe. RecoveryMaxBackoff caps the growth.
//
// Timeline in this example (recoveryTimeout=50ms, multiplier=2, max=200ms):
//   - trip from closed
//   - probe 1 after 50ms  → fails → next wait = 100ms
//   - probe 2 after 100ms → fails → next wait = 200ms (cap reached)
//   - probe 3 after 200ms → succeeds → breaker closes, counter resets
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

var errBackend = errors.New("backend unavailable")

func main() {
	ctx := context.Background()

	probeCount := 0
	failUntil := 2 // first two probes fail; third succeeds

	backend := func(_ context.Context) (string, error) {
		probeCount++
		if probeCount <= failUntil {
			fmt.Printf("  [backend] probe %d → fail\n", probeCount)

			return "", errBackend
		}

		fmt.Printf("  [backend] probe %d → ok\n", probeCount)

		return "pong", nil
	}

	policy := r8e.NewPolicy[string]("svc",
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:     func() { fmt.Println("  [hook] OPENED") },
			OnCircuitHalfOpen: func() { fmt.Println("  [hook] HALF-OPEN") },
			OnCircuitClose:    func() { fmt.Println("  [hook] CLOSED") },
		}),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(1),
			r8e.RecoveryTimeout(50*time.Millisecond),
			r8e.RecoveryBackoffMultiplier(2.0),
			r8e.RecoveryMaxBackoff(200*time.Millisecond),
		),
	)

	fail := func(_ context.Context) (string, error) { return "", errBackend }

	// Trip the breaker.
	fmt.Println("=== Trip the breaker ===")

	_, _ = policy.Do(ctx, fail) //nolint:errcheck // deliberate trip — error is expected

	// Probe attempts with increasing wait times.
	waits := []time.Duration{
		60 * time.Millisecond,  // > 50ms base: probe 1
		110 * time.Millisecond, // > 100ms (50ms × 2^1): probe 2
		210 * time.Millisecond, // > 200ms cap: probe 3
	}

	for i, wait := range waits {
		fmt.Printf("\n=== Attempt %d: waiting %v ===\n", i+1, wait)
		time.Sleep(wait)

		val, err := policy.Do(ctx, backend)
		if err != nil {
			fmt.Printf("  result: err=%v (state: %s)\n", err, policy.Metrics().CircuitState)
		} else {
			fmt.Printf("  result: %q (state: %s)\n", val, policy.Metrics().CircuitState)
		}
	}
}
