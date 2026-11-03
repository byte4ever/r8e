// Example 50-shield-parent: Demonstrates ShieldParent, which stops a dead route
// from draining the retry budget it shares with its siblings.
//
// The problem it solves: in a budget tree (example 42), every failure of a
// child is also debited from its parent. Once a route is dead, its own budget
// cuts its retries, but each new call still makes a first attempt, and each
// failed first attempt still drains the parent. The shared parent ends up
// empty, and a healthy sibling loses its retries because of a route it has
// nothing to do with.
//
// ShieldParent on the child stops passing its failures to the parent while
// the child is exhausted (at or below half its capacity). A dead child then
// drains the parent by at most half of its own capacity, rounded up: 5 tokens
// for MaxTokens(10). Size the parent so that it stays above half its capacity
// after that drain, and the siblings keep their retries.
//
// The demo builds the same tree twice, a gateway budget of 20 tokens with two
// route budgets of 10, kills the first route, then sends one call with a
// transient failure to the second route.
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

// deadRouteCalls is the number of calls sent to the dead route, enough to
// drain an unshielded gateway.
const deadRouteCalls = 20

func main() {
	fmt.Println("=== Without ShieldParent ===")
	run()

	fmt.Println("\n=== With ShieldParent on the dead route ===")
	run(r8e.ShieldParent())
}

// run builds a gateway budget with two route budgets under it, kills the first
// route, then makes one call to the second route. shield holds the extra
// options of the dead route's budget.
func run(shield ...r8e.RetryBudgetOption) {
	ctx := context.Background()

	gateway := r8e.NewRetryBudget(r8e.MaxTokens(20), r8e.TokenRatio(0.1))

	deadOpts := append([]r8e.RetryBudgetOption{
		r8e.MaxTokens(10), r8e.TokenRatio(0.1), r8e.Parent(gateway),
	}, shield...)
	deadBudget := r8e.NewRetryBudget(deadOpts...)
	siblingBudget := r8e.NewRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1), r8e.Parent(gateway))

	deadRoute := r8e.NewPolicy[string]("",
		r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond)),
		r8e.WithSharedRetryBudget(deadBudget),
	)
	sibling := r8e.NewPolicy[string]("",
		r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond)),
		r8e.WithSharedRetryBudget(siblingBudget),
	)

	// The dead route fails every call. Its own budget soon cuts its retries,
	// but each call still makes a first attempt.
	var deadAttempts, deadFailures int

	for range deadRouteCalls {
		_, err := deadRoute.Do(ctx, func(context.Context) (string, error) {
			deadAttempts++

			return "", errors.New("route down")
		})
		if err != nil {
			deadFailures++
		}
	}

	fmt.Printf("  dead route: %d calls failed, %d attempts\n", deadFailures, deadAttempts)
	fmt.Printf("  budgets: gateway=%.1f/20 dead=%.1f/10 sibling=%.1f/10\n",
		gateway.Tokens(), deadBudget.Tokens(), siblingBudget.Tokens())
	fmt.Printf("  gateway.Exhausted()=%v\n", gateway.Exhausted())

	// The sibling route has one transient failure, then answers. It succeeds
	// only if its retry is allowed by its own budget and by the gateway.
	var siblingAttempts int

	result, err := sibling.Do(ctx, func(context.Context) (string, error) {
		siblingAttempts++
		if siblingAttempts == 1 {
			return "", errors.New("transient blip")
		}

		return "sibling answered", nil
	})

	fmt.Printf("  sibling route: attempts=%d result=%q err=%v\n", siblingAttempts, result, err)
}
