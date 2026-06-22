// Example 10-full-policy: Compose every r8e resilience pattern into one policy.
//
// Real services rarely need a single guard — they want a timeout AND a circuit
// breaker AND retries AND a fallback working together. The trap is ordering:
// these middlewares only behave correctly when stacked in the right sequence
// (e.g. retry must sit inside the circuit breaker, not outside it). r8e removes
// that footgun by sorting patterns into a fixed, sane priority order, so the
// order you list the options in is irrelevant. This example wires up all of
// them at once and runs success and failure paths through the stack.
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

	// Hand r8e the full kitchen sink. We deliberately list the options out of
	// execution order to make the point: r8e assigns each pattern a fixed
	// priority and re-sorts them internally, so the policy below behaves
	// identically no matter how these lines are arranged.
	policy := r8e.NewPolicy[string]("full-policy",
		r8e.WithFallback("fallback value"),
		r8e.WithTimeout(2*time.Second),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(3),
			r8e.RecoveryTimeout(10*time.Second),
		),
		r8e.WithRateLimit(100),
		r8e.WithBulkhead(10),
		r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
		r8e.WithHedge(50*time.Millisecond),
		// Hooks give us a window into what the stack is doing without changing
		// its behaviour — handy here to actually see retries and the fallback
		// firing in the failure scenarios below.
		r8e.WithHooks(&r8e.Hooks{
			OnRetry:        func(attempt int, err error) { fmt.Printf("  [hook] retry #%d: %v\n", attempt, err) },
			OnTimeout:      func() { fmt.Println("  [hook] timeout") },
			OnFallbackUsed: func(err error) { fmt.Printf("  [hook] fallback used: %v\n", err) },
		}),
	)

	// Regardless of the option order above, r8e always runs them outside-in as:
	//   Fallback → Timeout → CircuitBreaker → RateLimiter
	//     → Bulkhead → Retry → Hedge → fn()
	// Fallback is outermost so it catches everything; retry/hedge are innermost
	// so they re-run the real function, not the surrounding guards.

	// Scenario 1: the function succeeds on the first try. Every pattern is a
	// transparent pass-through here — nothing trips — and we just get the value.
	fmt.Println("=== Successful Call (all patterns pass through) ===")

	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		return "all patterns composed successfully", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Scenario 2: the function fails on every attempt. Watch the chain react:
	// retry burns through its 3 attempts (each one logged by the OnRetry hook),
	// and once retries are exhausted the outermost fallback swallows the final
	// error and substitutes the static value — so the caller never sees a hard
	// failure.
	fmt.Println("=== Failing Call (retries exhausted → fallback) ===")

	attempt := 0
	result, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		return "", fmt.Errorf("failure #%d", attempt)
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Scenario 3: the same retry-then-fallback dynamic in isolation.
	fmt.Println("=== Another Failure (fallback) ===")

	// Stripping the policy down to just these two patterns makes the interaction
	// obvious — fallback is the safety net that turns "retries gave up" into a
	// usable value instead of a hard error.
	freshPolicy := r8e.NewPolicy[string]("fresh-policy",
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency fallback"),
	)
	result, err = freshPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("total failure")
	})
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
