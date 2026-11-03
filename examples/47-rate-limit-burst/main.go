// Example 47-rate-limit-burst: Demonstrates the Burst option of the rate
// limiter, which sizes the token bucket apart from the rate.
//
// The problem it solves: by default the bucket holds one second of tokens and
// starts full. At 10 req/s, 10 calls can leave at the same instant, on
// start-up or after a quiet second. That is fine for most downstreams, but not
// for one that only takes a few calls at once, or for a quota that counts
// calls over a window. Burst(n) sets the bucket to n tokens, whatever the rate:
// n calls can leave at once, then the calls follow the rate.
//
// Burst also survives a reload. Reconfigure changes the rate without touching
// a capacity set by Burst, and the config field rate_limit_burst retunes it.
// A reload cannot add a burst to a policy built without one.
//
// The demo drives the policies with a manual clock, so the output does not
// depend on the speed of the machine.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/byte4ever/r8e"
)

// manualClock is a Clock whose time moves only when Advance is called. Reject
// mode never waits, so the rate limiter only reads Now; NewTimer falls back to
// the real clock.
type manualClock struct {
	now time.Time
	mu  sync.Mutex
}

func main() {
	ctx := context.Background()
	clock := &manualClock{now: time.Unix(0, 0)}

	// Phase 1: without Burst, a 10 req/s limiter holds 10 tokens, so 10 calls
	// leave at once.
	fmt.Println("=== Phase 1: default bucket, one second of tokens ===")

	plain := r8e.NewPolicy[string]("rl-default",
		r8e.WithClock(clock),
		r8e.WithRateLimit(10),
	)
	fmt.Printf("  12 calls at t=0: %s\n", burstOf(ctx, plain, 12))

	// Phase 2: Burst(3) at the same rate lets 3 calls leave at once. After
	// that, a token comes back every 100ms, so the calls follow the rate.
	fmt.Println("\n=== Phase 2: Burst(3), then the steady rate ===")

	limited := r8e.NewPolicy[string]("rl-burst",
		r8e.WithClock(clock),
		r8e.WithRateLimit(10, r8e.Burst(3)),
	)
	fmt.Printf("  12 calls at t=0: %s\n", burstOf(ctx, limited, 12))

	for range 3 {
		clock.Advance(100 * time.Millisecond)
		fmt.Printf("  +100ms, 2 calls: %s\n", burstOf(ctx, limited, 2))
	}

	// Phase 3: a reload of the rate keeps the burst, and rate_limit_burst
	// retunes it. A long pause refills the bucket up to its capacity only.
	fmt.Println("\n=== Phase 3: reload ===")

	reload(limited, &r8e.PolicyConfig{RateLimit: ptr(20.0)}, "rate_limit=20")
	clock.Advance(time.Second)
	fmt.Printf("  after 1s, 12 calls: %s\n", burstOf(ctx, limited, 12))

	reload(limited, &r8e.PolicyConfig{RateLimitBurst: ptr(6)}, "rate_limit_burst=6")
	clock.Advance(time.Second)
	fmt.Printf("  after 1s, 12 calls: %s\n", burstOf(ctx, limited, 12))

	reload(plain, &r8e.PolicyConfig{RateLimitBurst: ptr(6)}, "rate_limit_burst=6 on rl-default")
}

// burstOf makes n calls at the current instant and returns one mark per call:
// "+" for an admitted call, "x" for one the rate limiter rejected, followed by
// the count of admitted calls.
func burstOf(ctx context.Context, policy *r8e.Policy[string], n int) string {
	var (
		marks    []byte
		admitted int
	)

	for range n {
		_, err := policy.Do(ctx, func(context.Context) (string, error) {
			return "ok", nil
		})

		switch {
		case err == nil:
			admitted++

			marks = append(marks, '+')
		case errors.Is(err, r8e.ErrRateLimited):
			marks = append(marks, 'x')
		default:
			marks = append(marks, '?')
		}
	}

	return fmt.Sprintf("%s (%d admitted)", marks, admitted)
}

// reload applies cfg to the policy as a config reload would and prints the
// outcome.
func reload(policy *r8e.Policy[string], cfg *r8e.PolicyConfig, what string) {
	if err := policy.Reconfigure(*cfg); err != nil {
		fmt.Printf("  reload %s: %v\n", what, err)

		return
	}

	fmt.Printf("  reload %s: ok\n", what)
}

// ptr returns a pointer to v, for the optional fields of PolicyConfig.
func ptr[T any](v T) *T {
	return &v
}

// Now returns the manual time.
func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Since returns the manual time elapsed since t.
func (c *manualClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

// NewTimer returns a real timer: reject mode never waits on one.
func (*manualClock) NewTimer(d time.Duration) r8e.Timer { //nolint:ireturn // the Clock interface returns a Timer
	return r8e.RealClock{}.NewTimer(d)
}

// Advance moves the manual time forward by d.
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}
