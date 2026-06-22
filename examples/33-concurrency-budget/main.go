// Example 33-concurrency-budget: Demonstrates the concurrency budget that caps
// how many retries may be in flight at once, as a fraction of live traffic with
// a floor. It is the concurrency-dimension complement of the retry budget (which
// throttles the retry RATE over time): under a burst of simultaneous failures,
// every caller retries at the same moment, multiplying load on the exact
// dependency that is already struggling — the classic retry storm. The budget
// admits only a bounded share of those retries concurrently and fails the rest
// fast with ErrConcurrencyBudgetExceeded, so the storm can't amplify the outage.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	var sheds atomic.Int64

	// MaxRatio(0.1) + MinConcurrency(2): with ~20 calls in flight the ceiling is
	// max(2, 0.1*20) = 2 concurrent retries; the rest are shed. A real service
	// would use the defaults (0.25 / 5).
	// A concurrency budget is inert on its own — it gates retries and hedges — so
	// it is paired with WithRetry here (configuring it with neither would panic in
	// NewPolicy). The deliberately tight MaxRatio(0.1)/MinConcurrency(2) makes the
	// shedding visible at this small scale; production code wants the gentler
	// defaults (0.25 / 5). The OnConcurrencyBudgetExceeded hook lets us count every
	// shed retry as it happens.
	policy := r8e.NewPolicy[string]("storm-guard",
		r8e.WithHooks(&r8e.Hooks{
			OnConcurrencyBudgetExceeded: func() { sheds.Add(1) },
		}),
		r8e.WithRetry(3, r8e.ConstantBackoff(20*time.Millisecond)),
		r8e.WithConcurrencyBudget(r8e.MaxRatio(0.1), r8e.MinConcurrency(2)),
	)

	down := errors.New("downstream unavailable")

	// Each call's first attempt is the baseline (never gated); it fails and the
	// call wants to retry. With many calls failing at once, the budget admits
	// only a couple of concurrent retries.
	failSlowly := func(_ context.Context) (string, error) {
		time.Sleep(30 * time.Millisecond)

		return "", r8e.Transient(down)
	}

	const calls = 20

	var (
		wg            sync.WaitGroup
		budgetShed    atomic.Int64
		retriesUsedUp atomic.Int64
	)

	fmt.Println("=== A retry storm hits a failing downstream ===")

	// Fire all 20 calls at once to recreate the storm: their first attempts all
	// fail together and all want to retry in the same instant. That simultaneity
	// is exactly what the budget exists to bound — only a couple of retries get a
	// permit, the rest are turned away immediately.
	for range calls {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := policy.Do(ctx, failSlowly)

			// Two distinct failure modes split the outcomes: a call shed by the
			// budget never got to retry (fast-failed to protect the backend),
			// whereas one that won a permit but kept failing exhausted its retries
			// normally. Telling them apart shows how much load the budget deflected.
			switch {
			case errors.Is(err, r8e.ErrConcurrencyBudgetExceeded):
				budgetShed.Add(1)
			case errors.Is(err, r8e.ErrRetriesExhausted):
				retriesUsedUp.Add(1)
			default:
				// A call that slipped through under the ceiling and exhausted
				// its retries lands above; nothing else is expected here.
			}
		}()
	}

	wg.Wait()

	fmt.Printf("  %d concurrent calls against a failing backend\n", calls)
	fmt.Printf("  retries shed by the budget:   %d\n", budgetShed.Load())
	fmt.Printf("  calls that exhausted retries: %d\n", retriesUsedUp.Load())
	fmt.Printf("  OnConcurrencyBudgetExceeded fired %d time(s)\n", sheds.Load())

	// --- Observability ---
	fmt.Println("\n=== Observability ===")

	metrics := policy.Metrics()
	fmt.Printf("  retries/hedges shed: %d\n", metrics.ConcurrencyBudgetExceeded)
	fmt.Printf("  permits in use now:  %d\n", metrics.ConcurrencyBudgetInUse)

	// Shedding is load protection, not a fault: the policy stays healthy and
	// ready. This matters operationally — a budget actively shedding a storm must
	// not flip a readiness probe and have the orchestrator kill a working instance.
	status := policy.HealthStatus()
	fmt.Printf("  health state:  %s\n", status.State)
	fmt.Printf("  still healthy: %t (readiness unaffected)\n", status.Healthy)
}
