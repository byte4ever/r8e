// Example 11-error-classification: Transient vs Permanent errors and retries.
//
// Blindly retrying every error is wasteful and sometimes harmful: retrying a
// "connection reset" is sensible, but retrying an "invalid API key" just burns
// the attempt budget (and time) on something that will never succeed. r8e lets
// you tag an error's nature at the point you know it best — Transient(err) for
// "try again" and Permanent(err) for "give up now" — and the retry loop honours
// that tag. This example walks through all three cases (transient, permanent,
// and unclassified) plus the IsTransient/IsPermanent inspection helpers.
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

	// One generous retry budget (5 attempts) reused across all three scenarios,
	// so the only thing that changes the outcome is how each error is classified
	// — not the policy configuration.
	policy := r8e.NewPolicy[string]("error-class-demo",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond)),
	)

	// Transient case: the function fails twice with errors we explicitly wrap in
	// Transient(...), signalling "this is worth retrying". The retry loop keeps
	// going and the third attempt succeeds — exactly what we want for a flaky
	// network blip.
	fmt.Println("=== Transient Error (retried) ===")

	attempt := 0
	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 3 {
			return "", r8e.Transient(errors.New("connection reset"))
		}

		return "recovered", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Permanent case: an "invalid API key" will never fix itself, so we wrap it
	// in Permanent(...). Even though 4 retries remain in the budget, the loop
	// short-circuits after the very first attempt — no point hammering a
	// downstream that has already told us "no".
	fmt.Println("=== Permanent Error (stops retries) ===")

	attempt = 0
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", r8e.Permanent(errors.New("invalid API key"))
	})
	fmt.Printf("  err: %v\n\n", err)

	// Unclassified case: a plain errors.New(...) carries no tag. r8e's default
	// is optimistic — it assumes an unknown error might be transient — so all 5
	// attempts get consumed. The lesson: reach for Permanent(...) when you *know*
	// an error is fatal, otherwise the retry budget is spent on lost causes.
	fmt.Println("=== Unclassified Error (treated as transient) ===")

	attempt = 0
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", errors.New("some unclassified error")
	})
	fmt.Printf("  err: %v (retried all 5 attempts)\n\n", err)

	// Beyond driving retries, the classification is queryable. IsTransient and
	// IsPermanent let your own code branch on an error's nature — e.g. to decide
	// whether to surface a "try again later" message to a user. Note the
	// asymmetry confirmed below: unclassified errors report IsTransient=true
	// (the default) but IsPermanent=false (only an explicit tag makes it true).
	fmt.Println("=== Classification Checks ===")

	transientErr := r8e.Transient(errors.New("timeout"))
	permanentErr := r8e.Permanent(errors.New("bad request"))
	plainErr := errors.New("unknown")

	fmt.Printf("  Transient(timeout): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(transientErr), r8e.IsPermanent(transientErr))
	fmt.Printf("  Permanent(bad request): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(permanentErr), r8e.IsPermanent(permanentErr))
	fmt.Printf("  Plain(unknown): IsTransient=%v, IsPermanent=%v\n",
		r8e.IsTransient(plainErr), r8e.IsPermanent(plainErr))
}
