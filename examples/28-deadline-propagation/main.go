// Example 28-deadline-propagation: Demonstrates hard deadline propagation on the
// time budget. By default the budget is cooperative and leaves ctx.Deadline()
// unset, so a downstream gRPC/HTTP callee can't shed early. With
// r8e.PropagateDeadline() the budget also exposes a real, clock-driven
// ctx.Deadline() that downstream callees observe and that cancels an in-flight
// attempt once the budget expires, surfacing r8e.ErrTimeBudgetExceeded.
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

	// A slow downstream call that respects context cancellation: it returns as
	// soon as its context is done (e.g. a gRPC call observing its deadline),
	// otherwise it would take a full second.
	slowCall := func(callCtx context.Context) (string, error) {
		if deadline, ok := callCtx.Deadline(); ok {
			fmt.Printf("    fn sees a deadline %v out\n",
				time.Until(deadline).Round(10*time.Millisecond))
		} else {
			fmt.Println("    fn sees NO deadline (cooperative budget only)")
		}

		select {
		case <-time.After(time.Second): // pretend the backend is slow
			return "late", nil
		case <-callCtx.Done():
			return "", callCtx.Err()
		}
	}

	run := func(label string, opts ...r8e.Option) {
		policy := r8e.NewPolicy[string](label, opts...)

		start := time.Now()
		_, err := policy.Do(ctx, slowCall)

		fmt.Printf("  %-18s finished in %4dms -> err: %v\n",
			label, time.Since(start).Milliseconds(), err)
	}

	retry := r8e.WithRetry(3, r8e.ConstantBackoff(10*time.Millisecond))
	budget := 200 * time.Millisecond

	fmt.Println("=== Cooperative budget (no propagation) ===")
	fmt.Println("  The budget cannot cancel the stuck in-flight call, so the slow")
	fmt.Println("  attempt runs to completion (~1s) before the budget matters.")
	run("cooperative", retry, r8e.WithTimeBudget(budget))

	fmt.Println("\n=== Hard deadline propagation ===")
	fmt.Println("  The attempt runs under the budget deadline and is cancelled at")
	fmt.Println("  200ms, returning ErrTimeBudgetExceeded.")
	run("propagated", retry, r8e.WithTimeBudget(budget, r8e.PropagateDeadline()))

	fmt.Println("\nThe propagated call returns ErrTimeBudgetExceeded wrapping the")
	fmt.Println("downstream context.DeadlineExceeded:")

	policy := r8e.NewPolicy[string]("demo",
		retry, r8e.WithTimeBudget(budget, r8e.PropagateDeadline()))

	_, err := policy.Do(ctx, slowCall)
	fmt.Printf("  errors.Is(err, ErrTimeBudgetExceeded)      = %v\n",
		errors.Is(err, r8e.ErrTimeBudgetExceeded))
	fmt.Printf("  errors.Is(err, context.DeadlineExceeded)   = %v\n",
		errors.Is(err, context.DeadlineExceeded))
}
