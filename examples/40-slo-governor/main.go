// Example 40-slo-governor: Demonstrates the SLO error-budget burn-rate governor
// shedding sheddable work — while always serving critical work — once the
// objective's error budget starts burning too fast.
//
// The problem it solves: a circuit breaker trips all-or-nothing, and an adaptive
// throttler sheds by the backend's live accept/request ratio. Neither knows what
// the service has PROMISED. An SLO governor does: given a target success rate
// (say 99%, an error budget of 1%), it watches how fast failures are spending
// that budget — the "burn rate" — and, when the budget is burning faster than
// sustainable across both a short and a long window, sheds load in proportion.
// Crucially it sheds by Sheddability: background/speculative work
// (SheddabilityAlways) is sacrificed first, while critical work
// (SheddabilityNever) is always admitted — so the remaining budget is spent on
// the calls that matter. A locally shed call is never recorded, so shedding the
// cheap traffic does not itself burn budget.
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

// errBackend is the backend's failure response — the thing that burns budget.
var errBackend = errors.New("backend error")

func main() {
	// errorRate is the backend's current failure probability (0..100, as an
	// integer percent so it is cheap to flip atomically); shed counts the
	// locally-shed calls so we can prove the governor is working.
	var (
		errorRate atomic.Int64
		shed      atomic.Int64
	)

	// A simple deterministic failure pattern: fail the first errorRate% of every
	// 100 calls. Avoids randomness so the demo output is stable.
	var seq atomic.Int64

	policy := r8e.NewPolicy[int]("checkout",
		// OnSLOShed fires for every call the governor sheds locally — the most
		// direct way to observe budget-protection shedding.
		r8e.WithHooks(&r8e.Hooks{
			OnSLOShed: func() { shed.Add(1) },
		}),
		// Target a 99% success rate (1% error budget). The windows are tuned for a
		// fast, observable demo rather than production: a short long-window so the
		// burn ages out quickly, and a low request floor so shedding can engage
		// after only a handful of calls.
		r8e.WithSLO(0.99,
			r8e.SLOLongWindow(2*time.Second),         // sustained-burn window
			r8e.SLOShortWindow(500*time.Millisecond), // responsiveness window
			r8e.BurnThreshold(2.0),                   // shed once burning >2x sustainable
			r8e.SLOMinRequests(20),                   // need some traffic before shedding
			r8e.MaxShedRate(0.9),                     // always keep probing — never shed 100%
		),
	)

	// call issues one request at the given Sheddability and reports its outcome.
	// The three-way result distinguishes a locally-shed call (never forwarded)
	// from a forwarded-but-failed one — the point of the governor is that shed
	// calls never touch the backend and never spend budget.
	call := func(s r8e.Sheddability) (forwarded, ok bool) {
		ctx := r8e.WithSheddability(context.Background(), s)

		_, err := policy.Do(ctx, func(_ context.Context) (int, error) {
			n := seq.Add(1) % 100
			if n < errorRate.Load() {
				return 0, errBackend
			}

			return 0, nil
		})

		switch {
		case errors.Is(err, r8e.ErrSLOShed):
			return false, false // shed locally, never reached the backend
		case err != nil:
			return true, false // forwarded, backend failed (burned budget)
		default:
			return true, true // forwarded, backend succeeded
		}
	}

	// burst runs count calls at one Sheddability and tallies how they ended.
	burst := func(label string, s r8e.Sheddability, count int) {
		shed.Store(0)

		var forwarded, served int

		for range count {
			fwd, ok := call(s)
			if fwd {
				forwarded++
			}

			if ok {
				served++
			}
		}

		metrics := policy.Metrics()

		fmt.Printf("%s\n", label)
		fmt.Printf("  forwarded to backend: %d/%d\n", forwarded, count)
		fmt.Printf("  shed locally:         %d\n", shed.Load())
		fmt.Printf("  served OK:            %d\n", served)
		fmt.Printf("  burn rate:            %.1fx\n", metrics.SLOBurnRate)
		fmt.Printf("  shed probability:     %.2f\n", metrics.SLOShedProbability)
		fmt.Printf("  health state:         %s\n\n", policy.HealthStatus().State)
	}

	// --- Healthy: every call succeeds, the budget is not burning, nothing is
	// shed regardless of Sheddability. ---
	errorRate.Store(0)
	burst("=== Healthy: budget intact, no shedding ===", r8e.SheddabilityDefault, 100)

	// --- Brownout: the backend fails 30% of calls. With a 1% budget that is a
	// ~30x burn rate, far past the 2x threshold, so the governor starts shedding.
	// The first calls below are forwarded and fail (building the burn) before
	// shedding engages — that is the warmup the MinRequests floor guards. ---
	errorRate.Store(30)
	burst("=== Brownout, DEFAULT traffic: governor sheds ~90% ===",
		r8e.SheddabilityDefault, 200)

	// --- Same brownout, sheddable (background) traffic: dropped as soon as any
	// shedding is active, so almost none reaches the backend. ---
	burst("=== Brownout, SHEDDABLE traffic: dropped first ===",
		r8e.SheddabilityAlways, 100)

	// --- Same brownout, critical traffic: always admitted, even at maximum burn,
	// so the remaining budget is spent on the calls that matter. ---
	burst("=== Brownout, CRITICAL traffic: always served ===",
		r8e.SheddabilityNever, 100)

	// --- Recovery: backend healthy again. The governor self-heals once the
	// failures age out of the window — no explicit reset — so we wait out the
	// long window and shedding clears on its own. ---
	errorRate.Store(0)
	time.Sleep(2500 * time.Millisecond) // let the failures age out of the window
	burst("=== Recovered: budget refilled, shedding clears ===",
		r8e.SheddabilityDefault, 100)
}
