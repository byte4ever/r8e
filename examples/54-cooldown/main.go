// Example 54-cooldown: Demonstrates a cooldown shared by two policies, which
// refuses calls locally until the deadline a server gave in a Retry-After
// hint.
//
// The problem it solves: after a 429 with a long Retry-After, every call that
// still goes out, on this route or on any other under the same quota, gets its
// own 429.
//
// NewCooldown builds one cooldown, and WithSharedCooldown attaches it to every
// policy under the quota. Any failed call whose error carries a Retry-After
// hint, such as r8e.RetryAfterError(err, d), arms it until now plus the hint.
// While it is armed, a call on any sharing policy fails at once with
// ErrCoolingDown, and fn does not run. Each policy reports its own refusals.
//
// The demo drives time with a manual clock passed through CooldownClock, so
// the deadline passes without any real wait.
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

// manualClock is a Clock whose time moves only when Advance is called. The
// cooldown only reads Now; NewTimer falls back to the real clock.
type manualClock struct {
	now time.Time
	mu  sync.Mutex
}

// errQuota is the error a 429 response stands for in this demo.
var errQuota = errors.New("429 too many requests")

func main() {
	ctx := context.Background()
	clock := &manualClock{now: time.Unix(0, 0)}

	// One cooldown for the whole quota, shared by two routes.
	quota := r8e.NewCooldown(r8e.CooldownClock(clock))

	search := r8e.NewPolicy[string]("search",
		r8e.WithSharedCooldown(quota),
		r8e.WithHooks(&r8e.Hooks{
			OnCooldownArmed: func(d time.Duration) { fmt.Printf("  search: OnCooldownArmed(%v)\n", d) },
		}),
	)
	export := r8e.NewPolicy[string]("export", r8e.WithSharedCooldown(quota))

	fmt.Println("=== search gets a 429 with Retry-After: 30s ===")

	call(ctx, "search", search, func() error { return r8e.RetryAfterError(errQuota, 30*time.Second) })
	fmt.Printf("  cooldown armed=%v remaining=%v\n", quota.Armed(), quota.Remaining())

	fmt.Println("\n=== Both routes are refused at once ===")

	clock.Advance(10 * time.Second)
	call(ctx, "search", search, nil)
	call(ctx, "export", export, nil)
	call(ctx, "export", export, nil)
	fmt.Printf("  CoolingDown metric: search=%d export=%d\n",
		search.Metrics().CoolingDown, export.Metrics().CoolingDown)
	fmt.Printf("  export health: %s\n", export.HealthStatus().State)

	fmt.Println("\n=== The deadline passes ===")

	clock.Advance(20 * time.Second)
	fmt.Printf("  cooldown armed=%v remaining=%v\n", quota.Armed(), quota.Remaining())
	call(ctx, "export", export, nil)
}

// call makes one call through policy and prints whether fn ran and what the
// caller got. fail, when not nil, gives the error fn returns.
func call(ctx context.Context, route string, policy *r8e.Policy[string], fail func() error) {
	ran := false

	result, err := policy.Do(ctx, func(context.Context) (string, error) {
		ran = true

		if fail != nil {
			return "", fail()
		}

		return route + " result", nil
	})

	fmt.Printf("  %s call: fn ran=%v result=%q err=%v cooling down=%v\n",
		route, ran, result, err, errors.Is(err, r8e.ErrCoolingDown))
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

// NewTimer returns a real timer: the cooldown never waits on one.
func (*manualClock) NewTimer(d time.Duration) r8e.Timer { //nolint:ireturn // the Clock interface returns a Timer
	return r8e.RealClock{}.NewTimer(d)
}

// Advance moves the manual time forward by d.
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}
