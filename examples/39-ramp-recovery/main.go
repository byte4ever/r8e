// Example 39-ramp-recovery: Demonstrates the circuit breaker's slow-start ramp
// recovery. After the breaker trips and a half-open probe succeeds, it does NOT
// jump straight back to full traffic. Instead it enters the CircuitRamping state
// and admits a growing fraction of calls over a ramp window — easing a healing
// downstream back to load rather than slamming it with the full firehose the
// instant it looks healthy (Envoy/Istio outlier-detection slow-start).
//
// The problem it solves: a downstream that just recovered is usually still
// fragile — cold caches, cold connection pools, a half-warmed JIT. A breaker that
// snaps from open straight to 100% admission can re-overwhelm it on the first
// probe success and flap right back open. Ramp recovery admits a growing fraction
// of traffic over a window, so the downstream re-warms under gradually rising
// load. This run trips the breaker, lets the probe enter the ramp, then sends
// bursts of traffic and watches the admitted fraction climb from ~10% to 100%.
// Calls shed during the ramp return ErrCircuitRamping, distinct from
// ErrCircuitOpen so callers can tell "recovering" apart from "still down".
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
			// A single failure trips the breaker — kept tiny so the demo opens on the
			// very first failed call rather than needing a run-up of failures.
			r8e.FailureThreshold(1),
			// Stay open for 200ms before allowing a probe, so the downstream gets a
			// breather instead of being retried instantly.
			r8e.RecoveryTimeout(200*time.Millisecond),
			// Let exactly one probe through to test the waters before ramping.
			r8e.HalfOpenMaxAttempts(1),
			// The heart of the demo: after the probe succeeds, ramp admission from
			// 10% to 100% over 1s (linearly) instead of snapping straight to full load.
			r8e.RampRecovery(1*time.Second),
			// Floor the ramp at 10% so even the first instant after recovery lets a
			// trickle through, rather than starting from zero admission.
			r8e.RampInitialFraction(0.1),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:    func() { fmt.Println("  [hook] circuit OPENED") },
			OnCircuitRamping: func() { fmt.Println("  [hook] circuit RAMPING (slow-start)") },
			OnCircuitClose:   func() { fmt.Println("  [hook] circuit CLOSED") },
		}),
	)

	// A flag we flip to model the downstream going from down to recovered, so the
	// same call function drives both the trip and the recovery without rewiring.
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
	// Wait past the 200ms recovery timeout so the breaker is willing to half-open
	// and let a probe through.
	time.Sleep(250 * time.Millisecond)

	// The downstream is now healthy, so the probe will succeed — which is what
	// moves the breaker into the ramping state rather than back fully closed.
	healthy = true

	_, err = policy.Do(ctx, call)
	fmt.Printf("  probe result: err=%v (state=%s)\n", err, policy.Metrics().CircuitState)

	// Phase 3: over the ramp window, send bursts of traffic and watch the
	// admitted fraction climb from ~10% toward 100% as the breaker eases the
	// downstream back to full load.
	fmt.Println("\n=== Phase 3: traffic ramps up over the window ===")

	// A fat burst per round so the admitted-vs-shed split is a meaningful sample
	// of the current admission fraction rather than coin-flip noise.
	const burst = 40

	for round := 1; round <= 8; round++ {
		admitted, shed := 0, 0

		for range burst {
			_, callErr := policy.Do(ctx, call)
			// ErrCircuitRamping (not ErrCircuitOpen) is the breaker's signal that the
			// call was shed by the ramp, not because the downstream is still down.
			if errors.Is(callErr, r8e.ErrCircuitRamping) {
				shed++
			} else {
				admitted++
			}
		}

		// The admitted fraction tracks the gauge, both climbing toward 100% as the
		// 1s ramp window elapses across the eight rounds.
		m := policy.Metrics()
		fmt.Printf("  round %d: admitted %2d/%d, shed %2d  (gauge fraction %.0f%%, state=%s)\n",
			round, admitted, burst, shed, m.RampRecoveryFraction*100, m.CircuitState)

		// Spread the rounds across the ramp window (8 x 140ms > 1s) so we watch the
		// fraction grow step by step instead of jumping straight to fully closed.
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
