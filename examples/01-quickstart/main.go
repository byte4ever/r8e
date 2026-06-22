// Example 01-quickstart: the smallest end-to-end r8e program.
//
// A single call into a remote dependency can hang, fail transiently, or keep
// failing while it is unhealthy. Rather than hand-roll a timeout, a retry loop,
// and a circuit breaker around every such call, r8e lets you declare them once
// as a Policy and run any function through that composed chain via Do. This
// example builds one policy with all three guards and executes a trivial
// function to show the wiring.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	// Compose three resilience patterns into one named policy. The options are
	// declarative: r8e sorts them into a sensible execution order itself, so the
	// order you list them here does not matter. Timeout bounds how long any
	// single attempt may run; retry re-runs transient failures with exponential
	// backoff (each wait roughly doubles, easing pressure on a struggling
	// dependency); the circuit breaker fast-fails once the downstream looks
	// unhealthy so we stop hammering it. The [string] type parameter is the
	// return type of the function we will run.
	policy := r8e.NewPolicy[string]("quickstart",
		r8e.WithTimeout(2*time.Second),
		r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
		r8e.WithCircuitBreaker(),
	)

	// Do runs the supplied function through the whole middleware chain. Here the
	// work trivially succeeds, but in real code this closure would be the call
	// you want protected. The context flows in so timeout/cancellation can reach
	// the work.
	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "Hello from r8e!", nil
		},
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("result:", result)
}
