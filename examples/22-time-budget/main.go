// Example 22-time-budget: Demonstrates WithTimeBudget — one *total* time budget
// shared across the whole retry chain. A per-attempt timeout only bounds each
// attempt, so N retries can still add up to N times the timeout; the problem it
// solves is the *sum* of all that waiting blowing a caller's own deadline.
// Before each retry the budget checks whether the next backoff alone would
// overrun the remaining time and, if so, stops early with ErrTimeBudgetExceeded
// instead of sleeping out a doomed attempt.
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
	down := errors.New("downstream unavailable")

	run := func(label string, opts ...r8e.Option) {
		policy := r8e.NewPolicy[string](label, opts...)

		attempts := 0
		start := time.Now()

		// The work always fails as transient, so retry keeps firing — this lets us
		// count how many attempts actually ran and how long the chain took before
		// either exhausting retries or hitting the budget.
		_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			attempts++

			return "", r8e.Transient(down)
		})

		fmt.Printf("  %-22s %d attempt(s) in %4dms  -> %v\n",
			label, attempts, time.Since(start).Milliseconds(), err)
	}

	// Retry up to 5 times with exponential backoff: 100, 200, 400, 800ms — about
	// 1.5s of sleeping if every attempt is allowed to run.
	backoff := r8e.ExponentialBackoff(100 * time.Millisecond)

	// Baseline: no budget, so retry sleeps every backoff and runs the full 5+1
	// attempts — the chain takes as long as the backoffs sum to (~1.5s).
	fmt.Println("=== Without a time budget: all attempts run ===")
	run("no-budget", r8e.WithRetry(5, backoff))

	// Same retry config, but capped at 350ms total. After the 100ms and 200ms
	// backoffs (~300ms spent), the next 400ms sleep would overrun the remaining
	// budget, so retry bails out early rather than waiting on a doomed attempt.
	fmt.Println("\n=== With a 350ms budget: retry stops early ===")
	run("budget-350ms",
		r8e.WithRetry(5, backoff),
		r8e.WithTimeBudget(350*time.Millisecond),
	)

	fmt.Println("\nThe budgeted policy returns ErrTimeBudgetExceeded once the next")
	fmt.Println("backoff would overrun the budget, rather than waiting out all retries.")
}
