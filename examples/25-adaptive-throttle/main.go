// Example 25-adaptive-throttle: Demonstrates the Google-SRE adaptive throttler
// shedding load locally as a backend starts rejecting requests.
//
// The problem it solves: when a backend is overloaded, blindly forwarding every
// request only piles more work onto a service that is already failing, deepening
// the outage and wasting the caller's own resources. The throttler keeps a
// sliding window of requests-attempted versus requests-accepted and, once the
// gap grows past OverloadRatio, probabilistically rejects new calls locally with
// ErrThrottled — before the work is even attempted. Unlike a circuit breaker it
// dampens load gradually and proportionally, and it recovers on its own once the
// failures age out of its window.
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
	// healthy flips the simulated backend between serving and rejecting; shed
	// counts the locally-shed calls so we can prove the throttler is working.
	var (
		healthy atomic.Bool
		shed    atomic.Int64
	)

	healthy.Store(true)

	policy := r8e.NewPolicy[int]("downstream",
		// OnThrottled fires for every call the throttler rejects locally — the
		// most direct way to observe shedding without inspecting metrics.
		r8e.WithHooks(&r8e.Hooks{
			OnThrottled: func() { shed.Add(1) },
		}),
		// The throttler is tuned for a fast, observable demo rather than for
		// production: a short window so failures age out quickly, and a low
		// request floor so shedding can engage after only a handful of calls.
		r8e.WithAdaptiveThrottle(
			r8e.OverloadRatio(2),              // SRE K: shed past a 2x request/accept gap
			r8e.MinRequests(10),               // need some traffic before shedding
			r8e.ThrottleWindow(2*time.Second), // short window for a snappy demo
			r8e.MaxRejectionRate(0.9),         // always keep probing — never shed 100%
		),
	)

	// call issues one request and reports its outcome. The three-way result lets
	// the caller distinguish a locally-shed call (never forwarded) from a
	// forwarded-but-rejected one — the whole point of adaptive throttling is that
	// the first kind never touches the backend at all.
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

	// burst runs count sequential calls and tallies how they ended, then prints
	// the throttler's live gauges so each phase can be read at a glance.
	burst := func(label string, count int) {
		shed.Store(0) // reset the per-burst shed counter

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

	// --- Healthy: every call succeeds, so requests track accepts and the
	// request/accept gap never crosses OverloadRatio — nothing is shed. ---
	burst("=== Healthy backend: no shedding ===", 100)

	// --- Overload: the backend rejects everything; shedding ramps up. ---
	// Sleep past the 2s window first so the earlier healthy traffic ages out;
	// otherwise those accepts would mask the outage and delay shedding.
	time.Sleep(2500 * time.Millisecond)
	healthy.Store(false)
	burst("=== Overloaded backend: throttler sheds load ===", 200)

	// --- Recovery: backend healthy again. The throttler has no explicit reset —
	// it self-heals once the failures age out of the window, so we wait out the
	// window before measuring and shedding clears on its own. ---
	healthy.Store(true)
	time.Sleep(2500 * time.Millisecond) // let the failures age out of the window
	burst("=== Recovered backend: shedding clears ===", 100)
}
