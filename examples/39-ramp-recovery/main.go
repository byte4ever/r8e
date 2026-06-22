// Example 39-ramp-recovery: Demonstrates the circuit breaker's slow-start ramp
// recovery. After the breaker trips and a half-open probe succeeds, it does NOT
// jump straight back to full traffic. Instead it enters the CircuitRamping state
// and admits a growing fraction of calls over a ramp window — easing a healing
// downstream back to load rather than slamming it with the full firehose the
// instant it looks healthy. Shed calls during the ramp return ErrCircuitRamping,
// distinct from ErrCircuitOpen.
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

	policy := r8e.NewPolicy[string]("ramp-recovery-demo",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(1),
			r8e.RecoveryTimeout(200*time.Millisecond),
			r8e.HalfOpenMaxAttempts(1),
			// After recovery, ramp admission from 10% to 100% over 1s, linearly.
			r8e.RampRecovery(1*time.Second),
			r8e.RampInitialFraction(0.1),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:    func() { fmt.Println("  [hook] circuit OPENED") },
			OnCircuitRamping: func() { fmt.Println("  [hook] circuit RAMPING (slow-start)") },
			OnCircuitClose:   func() { fmt.Println("  [hook] circuit CLOSED") },
		}),
	)

	healthy := false

	call := func(ctx context.Context) (string, error) {
		if !healthy {
			return "", errors.New("downstream down")
		}

		return "ok", ctx.Err()
	}

	// Phase 1: a failure trips the breaker.
	fmt.Println("=== Phase 1: downstream down — trip the breaker ===")

	_, err := policy.Do(ctx, call)
	fmt.Printf("  call failed: %v (state=%s)\n", err, policy.Metrics().CircuitState)

	// Phase 2: downstream recovers; after the recovery timeout a probe succeeds
	// and the breaker enters the slow-start ramp.
	fmt.Println("\n=== Phase 2: downstream recovered — probe enters the ramp ===")
	time.Sleep(250 * time.Millisecond)

	healthy = true

	_, err = policy.Do(ctx, call)
	fmt.Printf("  probe result: err=%v (state=%s)\n", err, policy.Metrics().CircuitState)

	// Phase 3: over the ramp window, send bursts of traffic and watch the
	// admitted fraction climb from ~10% toward 100% as the breaker eases the
	// downstream back to full load.
	fmt.Println("\n=== Phase 3: traffic ramps up over the window ===")

	const burst = 40

	for round := 1; round <= 8; round++ {
		admitted, shed := 0, 0

		for range burst {
			_, callErr := policy.Do(ctx, call)
			if errors.Is(callErr, r8e.ErrCircuitRamping) {
				shed++
			} else {
				admitted++
			}
		}

		m := policy.Metrics()
		fmt.Printf("  round %d: admitted %2d/%d, shed %2d  (gauge fraction %.0f%%, state=%s)\n",
			round, admitted, burst, shed, m.RampRecoveryFraction*100, m.CircuitState)

		time.Sleep(140 * time.Millisecond)
	}

	// Phase 4: the ramp window has elapsed — the breaker is fully closed.
	fmt.Println("\n=== Phase 4: ramp complete ===")

	if _, doErr := policy.Do(ctx, call); doErr != nil {
		fmt.Printf("  unexpected error: %v\n", doErr)
	}

	m := policy.Metrics()
	fmt.Printf("  final state=%s, ramp transitions=%d\n", m.CircuitState, m.CircuitRamps)
}
