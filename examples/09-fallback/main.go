// Example 09-fallback: Demonstrates the fallback pattern — the last line of
// defence that hands back a usable value when the protected call has failed for
// good. Instead of surfacing an error to the end user, you degrade gracefully
// to a safe default. This shows both flavours: a static fallback (fixed value,
// error swallowed) and a function fallback (computes from the error, and may
// itself fail). Because fallback is the outermost middleware, a failure inside
// the fallback function has nothing left to catch it and propagates out.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// --- Static fallback ---
	// The simplest form: when the call fails, return a fixed value and swallow
	// the error entirely. Use this when any sane default is better than an
	// error page — e.g. an empty feature flag, a cached "service unavailable"
	// notice. The caller below sees "default value" with a nil error.
	fmt.Println("=== Static Fallback ===")

	staticPolicy := r8e.NewPolicy[string]("static-fallback",
		r8e.WithFallback("default value"),
	)

	result, err := staticPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("service unavailable")
		},
	)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Function fallback ---
	// When the fallback needs to depend on what went wrong, pass a function: it
	// receives the original error and computes a value. Handy for tailoring the
	// degraded response to the failure, or for logging/metrics before recovery.
	fmt.Println("=== Function Fallback ===")

	funcPolicy := r8e.NewPolicy[string]("func-fallback",
		r8e.WithFallbackFunc(func(err error) (string, error) {
			return fmt.Sprintf("fallback computed from error: %v", err), nil
		}),
	)

	result, err = funcPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("database connection refused")
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Fallback with function that also fails ---
	// A fallback function isn't obliged to succeed. If it returns an error,
	// that error is what the caller gets — fallback is the outermost layer, so
	// there is no further middleware to rescue it. Here we wrap the original
	// error to preserve the cause, demonstrating the error passes straight out.
	fmt.Println("=== Fallback Function That Also Fails ===")

	failingFbPolicy := r8e.NewPolicy[string]("failing-fallback",
		r8e.WithFallbackFunc(func(err error) (string, error) {
			return "", fmt.Errorf("fallback also failed: %w", err)
		}),
	)

	_, err = failingFbPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("primary failed")
	})
	fmt.Printf("  err: %v\n\n", err)

	// --- Successful call ignores fallback ---
	// Fallback only triggers on failure: when the primary succeeds it is never
	// invoked and the result passes through untouched, with zero overhead on
	// the happy path. We reuse staticPolicy to make the contrast explicit.
	fmt.Println("=== Successful Call (fallback not used) ===")

	result, err = staticPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "primary success", nil
	})
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
