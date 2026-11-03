// Example 46-error-codes: Demonstrates r8e.ErrorCode, which returns the stable
// code of the r8e sentinel an error carries.
//
// The problem it solves: a log line or a metric needs a short, bounded label
// that says why a call failed. The error message is not one: it changes with
// every wrap and can carry request data, so a metric labelled with it grows
// without bound. errors.Is tests one sentinel at a time, and a switch over
// every sentinel must follow each new release of the library.
//
// ErrorCode walks the error chain and returns the code of the first r8e
// sentinel it finds, such as "circuit_open" or "timeout", even when the
// sentinel is wrapped. Three sentinels are envelopes: ErrRetriesExhausted,
// ErrTimeBudgetExceeded and ErrConcurrencyBudgetExceeded wrap the error that
// ended the last attempt, so they yield to the cause they carry. An error that
// carries only an envelope reports the envelope, and an error that carries no
// r8e sentinel reports "".
//
// This demo prints the code of five errors, then counts them by code as a
// metric would.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// failures counts the errors by code, as a metric with a "reason"
	// label would. The codes form a small, fixed set, so the label stays
	// bounded.
	failures := make(map[string]int)

	record := func(scenario string, err error) {
		code := r8e.ErrorCode(err)
		failures[reasonLabel(code)]++

		fmt.Printf("  %s\n    err=%q\n    code=%q\n", scenario, err, code)
	}

	fmt.Println("=== ErrorCode for five errors ===")

	// A breaker that one failure opens. The first call fails with a plain
	// error, which carries no r8e sentinel; the second is rejected by the open
	// breaker.
	payments := r8e.NewPolicy[string]("payments",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(1),
			r8e.RecoveryTimeout(time.Minute),
		),
	)

	_, err := payments.Do(ctx, func(context.Context) (string, error) {
		return "", errors.New("payments unavailable")
	})
	record("1. plain error from the downstream", err)

	_, err = payments.Do(ctx, func(context.Context) (string, error) {
		return "paid", nil
	})
	record("2. call rejected by the open breaker", err)

	// The code survives the caller's own wrap.
	record("3. the same rejection, wrapped by the caller", fmt.Errorf("checkout: %w", err))

	// A client policy retries calls to an inventory policy whose timeout cuts
	// each one. ErrRetriesExhausted is an envelope: the code is the cause it
	// carries, the inner policy's timeout.
	inventory := r8e.NewPolicy[string]("inventory",
		r8e.WithTimeout(20*time.Millisecond),
	)
	client := r8e.NewPolicy[string]("inventory-client",
		r8e.WithRetry(2, r8e.ConstantBackoff(time.Millisecond)),
	)

	_, err = client.Do(ctx, func(ctx context.Context) (string, error) {
		return inventory.Do(ctx, hangingDownstream)
	})
	record("4. retries exhausted around a timeout", err)

	// When the cause carries no r8e sentinel, the envelope is the only code.
	_, err = client.Do(ctx, func(context.Context) (string, error) {
		return "", errors.New("connection reset")
	})
	record("5. retries exhausted around a plain error", err)

	fmt.Println("\n=== Failures by reason label ===")

	for _, label := range slices.Sorted(maps.Keys(failures)) {
		fmt.Printf("  reason=%s count=%d\n", label, failures[label])
	}
}

// reasonLabel turns a code into a metric label. An error with no r8e sentinel
// has the code "", which a metric backend may drop, so it gets a label of its
// own.
func reasonLabel(code string) string {
	if code == "" {
		return "other"
	}

	return code
}

// hangingDownstream never answers; it only returns once its context ends.
func hangingDownstream(ctx context.Context) (string, error) {
	<-ctx.Done()

	return "", fmt.Errorf("wait for stock: %w", ctx.Err())
}
