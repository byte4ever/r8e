// Example 25-adaptive-throttle: Demonstrates the Google-SRE adaptive throttler
// shedding load locally as a backend starts rejecting requests. While the
// backend is healthy nothing is shed; once it fails most calls, the throttler
// rejects a growing fraction with ErrThrottled — before the work is even
// attempted — and recovers on its own once the failures age out of its window.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

// errOverloaded is the backend's "I am overloaded" response.
var errOverloaded = errors.New("backend overloaded")

func main() {
	// healthy flips the simulated backend between serving and rejecting.
	var (
		healthy atomic.Bool
		shed    atomic.Int64
	)

	healthy.Store(true)

	policy := r8e.NewPolicy[int]("downstream",
		r8e.WithHooks(&r8e.Hooks{
			OnThrottled: func() { shed.Add(1) },
		}),
		r8e.WithAdaptiveThrottle(
			r8e.OverloadRatio(2),              // SRE K: shed past a 2x request/accept gap
			r8e.MinRequests(10),               // need some traffic before shedding
			r8e.ThrottleWindow(2*time.Second), // short window for a snappy demo
			r8e.MaxRejectionRate(0.9),         // always keep probing
		),
	)

	// call issues one request and reports its outcome.
	call := func() (forwarded, ok bool) {
		_, err := policy.Do(context.Background(), func(_ context.Context) (int, error) {
			if healthy.Load() {
				return 0, nil
			}

			return 0, errOverloaded
		})

		switch {
		case errors.Is(err, r8e.ErrThrottled):
			return false, false // shed locally, never reached the backend
		case err != nil:
			return true, false // forwarded, backend rejected
		default:
			return true, true // forwarded, backend accepted
		}
	}

	// burst runs count sequential calls and tallies how they ended.
	burst := func(label string, count int) {
		shed.Store(0)

		var forwarded, accepted int

		for range count {
			fwd, ok := call()
			if fwd {
				forwarded++
			}

			if ok {
				accepted++
			}
		}

		fmt.Printf("%s\n", label)
		fmt.Printf("  forwarded to backend: %d/%d\n", forwarded, count)
		fmt.Printf("  shed locally:         %d\n", shed.Load())
		fmt.Printf("  accepted:             %d\n", accepted)

		m := policy.Metrics()
		fmt.Printf("  reject probability:   %.2f\n", m.ThrottleProbability)
		fmt.Printf("  health state:         %s\n\n", policy.HealthStatus().State)
	}

	// --- Healthy: every call succeeds, nothing is shed ---
	burst("=== Healthy backend: no shedding ===", 100)

	// --- Overload: the backend rejects everything; shedding ramps up ---
	// Let the healthy traffic age out first so the window reflects the outage.
	time.Sleep(2500 * time.Millisecond)
	healthy.Store(false)
	burst("=== Overloaded backend: throttler sheds load ===", 200)

	// --- Recovery: backend healthy again; wait out the window, shedding stops ---
	healthy.Store(true)
	time.Sleep(2500 * time.Millisecond) // let the failures age out of the window
	burst("=== Recovered backend: shedding clears ===", 100)
}
