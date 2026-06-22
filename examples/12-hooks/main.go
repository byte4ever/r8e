// Example 12-hooks: Observability via lifecycle hooks.
//
// A resilience policy makes decisions you can't see from the outside: it
// retries, opens a circuit, sheds a rate-limited request, falls back. Without
// visibility, that's a black box — you can't feed those events into metrics,
// logs, or alerts. r8e's Hooks struct exposes a callback for every such
// transition, letting you observe the machinery without altering its behaviour.
// This example registers all 12 hooks once, then runs three small policies to
// trip retry, bulkhead, and fallback hooks so you can see them fire.
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

	// Wire up every hook the library offers. In production these callbacks would
	// increment Prometheus counters or emit structured logs; here they just
	// print, so each scenario below visibly announces which transitions it hit.
	// The same hooks struct is shared by all three policies — hooks are just
	// observers, so reusing one set across policies is perfectly fine.
	hooks := r8e.Hooks{
		OnRetry: func(attempt int, err error) {
			fmt.Printf(
				"  [OnRetry]            attempt=%d err=%v\n",
				attempt,
				err,
			)
		},
		OnCircuitOpen:      func() { fmt.Println("  [OnCircuitOpen]      circuit breaker opened") },
		OnCircuitClose:     func() { fmt.Println("  [OnCircuitClose]     circuit breaker closed") },
		OnCircuitHalfOpen:  func() { fmt.Println("  [OnCircuitHalfOpen]  circuit breaker half-open") },
		OnTimeout:          func() { fmt.Println("  [OnTimeout]          request timed out") },
		OnRateLimited:      func() { fmt.Println("  [OnRateLimited]      request rate limited") },
		OnBulkheadFull:     func() { fmt.Println("  [OnBulkheadFull]     bulkhead at capacity") },
		OnBulkheadAcquired: func() { fmt.Println("  [OnBulkheadAcquired] bulkhead slot acquired") },
		OnBulkheadReleased: func() { fmt.Println("  [OnBulkheadReleased] bulkhead slot released") },
		OnHedgeTriggered:   func() { fmt.Println("  [OnHedgeTriggered]   hedge request fired") },
		OnHedgeWon:         func() { fmt.Println("  [OnHedgeWon]         hedge request won") },
		OnFallbackUsed:     func(err error) { fmt.Printf("  [OnFallbackUsed]     error=%v\n", err) },
	}

	// Scenario 1: a function that fails twice before succeeding. We expect
	// OnRetry to fire on each of the two retries (carrying the attempt number
	// and the triggering error), and no fallback — the third attempt succeeds,
	// so the safety net is never needed.
	fmt.Println("=== Retry Hooks ===")

	p := r8e.NewPolicy[string]("retry-hooks",
		r8e.WithRetry(3, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("fallback"),
		r8e.WithHooks(&hooks),
	)
	attempt := 0
	result, _ := p.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", fmt.Errorf("fail %d", attempt)
			}

			return "success", nil
		},
	)
	fmt.Printf("  result: %q\n\n", result)

	// Scenario 2: a single call through a bulkhead. Even on the happy path the
	// bulkhead emits a matched pair of lifecycle events — OnBulkheadAcquired when
	// the concurrency slot is taken and OnBulkheadReleased when it's handed back.
	// Seeing both confirms slots are released and not leaked.
	fmt.Println("=== Bulkhead Hooks ===")

	bhPolicy := r8e.NewPolicy[string]("bh-hooks",
		r8e.WithBulkhead(1),
		r8e.WithHooks(&hooks),
	)
	result, _ = bhPolicy.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			return "bulkhead call", nil
		},
	)
	fmt.Printf("  result: %q\n\n", result)

	// Scenario 3: a function that always fails. Retries run out, then
	// OnFallbackUsed fires with the final error just before the fallback value is
	// substituted. This is the hook you'd alert on — repeated fallback usage
	// means the real dependency is unhealthy.
	fmt.Println("=== Fallback Hooks ===")

	fbPolicy := r8e.NewPolicy[string]("fb-hooks",
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency"),
		r8e.WithHooks(&hooks),
	)
	result, _ = fbPolicy.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("total failure")
		},
	)
	fmt.Printf("  result: %q\n", result)
}
