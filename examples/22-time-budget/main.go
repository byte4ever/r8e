// Example 22-time-budget: Demonstrates a total time budget shared across the
// retry chain. Instead of sleeping a backoff and launching an attempt that
// cannot finish in time, retry stops early once the budget is spent — tighter
// than a per-attempt timeout, which only bounds each attempt.
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

	fmt.Println("=== Without a time budget: all attempts run ===")
	run("no-budget", r8e.WithRetry(5, backoff))

	fmt.Println("\n=== With a 350ms budget: retry stops early ===")
	run("budget-350ms",
		r8e.WithRetry(5, backoff),
		r8e.WithTimeBudget(350*time.Millisecond),
	)

	fmt.Println("\nThe budgeted policy returns ErrTimeBudgetExceeded once the next")
	fmt.Println("backoff would overrun the budget, rather than waiting out all retries.")
}
