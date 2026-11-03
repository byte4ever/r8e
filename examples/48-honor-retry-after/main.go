// Example 48-honor-retry-after: Demonstrates HonorRetryAfter, which stops a
// retry instead of sending it before the time the server asked for.
//
// The problem it solves: retry honors a server's Retry-After hint, but caps it
// with MaxDelay. Against a quota, a hint of one hour capped to 50ms means the
// retry leaves 59 minutes early and the server rejects it again: each retry
// wastes a call and can count against the quota.
//
// With HonorRetryAfter, a raw hint above MaxDelay ends the retry. Do returns
// the last error as is, not wrapped with ErrRetriesExhausted, so the caller
// reads the full hint through RetryAfterProvider and can schedule the work for
// later. A hint at or below MaxDelay is still waited and retried.
//
// The demo calls a downstream that answers with a hint, built with
// r8e.RetryAfterError, under three retry policies.
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

// downstream rejects its first calls, as many as failures, with errQuota and a
// Retry-After hint, then answers.
type downstream struct {
	hint     time.Duration
	failures int
	attempts int
}

const maxDelay = 50 * time.Millisecond

// errQuota is the downstream's answer once its quota is spent, like an HTTP 429.
var errQuota = errors.New("quota exceeded")

func main() {
	ctx := context.Background()

	// Phase 1: without HonorRetryAfter, the one-hour hint is capped to
	// MaxDelay. Each retry leaves after about 50ms and meets the same quota.
	fmt.Println("=== Phase 1: default, the hint is capped by MaxDelay ===")

	capped := newPolicy("quota-capped")
	quotaSpent := &downstream{hint: time.Hour, failures: 3}
	start := time.Now()

	_, err := capped.Do(ctx, quotaSpent.call)
	fmt.Printf("  attempts=%d err=%q\n", quotaSpent.attempts, err)
	fmt.Printf("  waited at least 2 x MaxDelay: %v\n", time.Since(start) >= 2*maxDelay)
	fmt.Printf("  errors.Is(err, ErrRetriesExhausted)=%v\n", errors.Is(err, r8e.ErrRetriesExhausted))

	// Phase 2: with HonorRetryAfter, the same hint ends the retry after the
	// first attempt. The caller gets the downstream's error and its full hint.
	fmt.Println("\n=== Phase 2: HonorRetryAfter, a hint above MaxDelay stops the retry ===")

	honoring := newPolicy("quota-honoring", r8e.HonorRetryAfter())
	quotaSpent = &downstream{hint: time.Hour, failures: 3}

	_, err = honoring.Do(ctx, quotaSpent.call)
	fmt.Printf("  attempts=%d err=%q\n", quotaSpent.attempts, err)
	fmt.Printf("  errors.Is(err, ErrRetriesExhausted)=%v\n", errors.Is(err, r8e.ErrRetriesExhausted))

	var provider r8e.RetryAfterProvider
	if errors.As(err, &provider) {
		after, _ := provider.RetryAfter()
		fmt.Printf("  the caller reads the hint: retry after %v\n", after)
	}

	// Phase 3: a hint at or below MaxDelay is waited as usual, and the retry
	// succeeds once the downstream is ready.
	fmt.Println("\n=== Phase 3: HonorRetryAfter, a hint below MaxDelay is waited ===")

	shortWait := &downstream{hint: 20 * time.Millisecond, failures: 1}
	start = time.Now()

	result, err := honoring.Do(ctx, shortWait.call)
	fmt.Printf("  attempts=%d result=%q err=%v\n", shortWait.attempts, result, err)
	fmt.Printf("  waited at least the hint: %v\n", time.Since(start) >= shortWait.hint)
}

// newPolicy builds a retry policy of three attempts whose waits MaxDelay caps.
// The 1ms backoff is never used: every failure carries a hint.
func newPolicy(name string, opts ...r8e.RetryOption) *r8e.Policy[string] {
	retryOpts := append([]r8e.RetryOption{r8e.MaxDelay(maxDelay)}, opts...)

	return r8e.NewPolicy[string](name,
		r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond), retryOpts...),
	)
}

func (d *downstream) call(context.Context) (string, error) {
	d.attempts++

	if d.attempts <= d.failures {
		return "", fmt.Errorf("fetch report: %w", r8e.RetryAfterError(errQuota, d.hint))
	}

	return "report ready", nil
}
