// Example 01-quickstart: Minimal Policy + Do usage.
//
// Creates a simple policy with timeout, retry, and circuit breaker,
// then executes a function through it.
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
	// Create a policy with three resilience patterns.
	policy := r8e.NewPolicy[string]("quickstart",
		r8e.WithTimeout(2*time.Second),
		r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
		r8e.WithCircuitBreaker(),
	)

	// Execute a function through the policy.
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
