// Package main demonstrates adaptive recovery backoff for the circuit breaker.
//
// Without backoff, a tripped circuit attempts a half-open probe every
// recoveryTimeout regardless of how many probes have already failed — so a
// downstream that stays down keeps getting hammered at a fixed cadence, exactly
// when it can least afford the extra load. With RecoveryBackoffMultiplier each
// failed probe scales the wait before the next one (here it doubles), backing off
// the way a retrying client should; RecoveryMaxBackoff caps that growth so the
// breaker still re-checks within a bounded interval, and the backoff resets once
// the breaker successfully closes.
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

	// The backend is scripted to fail its first two half-open probes and succeed
	// on the third, so we can watch the wait between probes grow (50→100→200ms)
	// before the breaker finally closes.
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

	// The hooks narrate every state transition so the backoff is observable
	// without reading metrics. FailureThreshold(1) trips on the very first
	// failure to keep the demo short; the three recovery knobs are what this
	// example is about — a 50ms base probe interval that doubles on each failed
	// probe up to a 200ms ceiling.
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

	// Each wait is set just past the current backoff so the breaker is willing to
	// admit one probe per attempt. The waits grow with the backoff (60 > 50,
	// 110 > 100, 210 > 200); sleeping less than the current backoff would leave
	// the breaker open and the call would be rejected outright rather than probed.
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
