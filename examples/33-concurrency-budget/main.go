// Example 33-concurrency-budget: Demonstrates the concurrency budget that caps
// how many retries may be in flight at once, as a fraction of live traffic with
// a floor. It is the concurrency-dimension complement of the retry budget (which
// throttles the retry RATE over time): under a burst of simultaneous failures,
// only a bounded share of calls may retry concurrently — the rest fail fast with
// ErrConcurrencyBudgetExceeded instead of piling load onto a struggling backend.
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

	for range calls {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := policy.Do(ctx, failSlowly)

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

	status := policy.HealthStatus()
	fmt.Printf("  health state:  %s\n", status.State)
	fmt.Printf("  still healthy: %t (readiness unaffected)\n", status.Healthy)
}
