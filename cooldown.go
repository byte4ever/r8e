package r8e

import (
	"context"
	"sync"
	"time"
)

type (
	// Cooldown refuses calls locally until the deadline a server gave in a
	// Retry-After hint. After a 429 with a one-hour Retry-After, every call
	// that still goes out gets its own 429; a cooldown armed by the first hint
	// stops them at the client instead.
	//
	// Any error carrying a [RetryAfterProvider] arms the cooldown until now
	// plus its hint, capped by [MaxCooldown]; [CooldownIf] narrows which
	// errors arm it. A later hint only extends the deadline, never shortens
	// it, and a success does not disarm it.
	//
	// Build one with [NewCooldown] and share it across policies with
	// [WithSharedCooldown], or let [WithCooldown] build one for a single
	// policy. It is safe for concurrent use.
	//
	// Pattern: Cooldown — a deadline set by the server's own hint gates the
	// calls of every policy sharing it, so the client stops before the server
	// has to refuse.
	Cooldown struct {
		clock   Clock
		trigger func(error) bool
		until   time.Time
		mu      sync.Mutex
		limit   time.Duration
	}

	// CooldownOption configures a [Cooldown].
	//
	// Pattern: Functional Options — composable optional settings applied to
	// the private config, keeping NewCooldown's signature stable.
	CooldownOption func(*cooldownConfig)

	cooldownConfig struct {
		clock   Clock
		trigger func(error) bool
		limit   time.Duration
	}
)

// defaultMaxCooldown caps a cooldown when [MaxCooldown] is not given: a hint
// of several days must not stop a client for that long.
const defaultMaxCooldown = time.Hour

// MaxCooldown caps how long one hint keeps the cooldown armed. Default: one
// hour. A non-positive d panics in [NewCooldown], [WithCooldown] and
// [NewPolicy].
func MaxCooldown(d time.Duration) CooldownOption {
	return func(cfg *cooldownConfig) {
		cfg.limit = d
	}
}

// CooldownIf restricts which errors arm the cooldown, for example to a 429:
// an error arms it only when fn returns true and the error carries a
// Retry-After hint. A nil fn keeps the default, every hinted error.
func CooldownIf(fn func(error) bool) CooldownOption {
	return func(cfg *cooldownConfig) {
		cfg.trigger = fn
	}
}

// CooldownClock sets the clock a standalone [Cooldown] reads. [WithCooldown]
// uses the policy's clock. Default: [RealClock].
func CooldownClock(c Clock) CooldownOption {
	return func(cfg *cooldownConfig) {
		cfg.clock = c
	}
}

// NewCooldown builds a cooldown, disarmed. It panics with
// [ErrCooldownNonPositiveMax] when [MaxCooldown] is not positive.
func NewCooldown(opts ...CooldownOption) *Cooldown {
	cfg := cooldownConfig{clock: RealClock{}, limit: defaultMaxCooldown}
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.limit <= 0 {
		panic(ErrCooldownNonPositiveMax)
	}

	return &Cooldown{clock: cfg.clock, trigger: cfg.trigger, limit: cfg.limit}
}

// Remaining reports how long the cooldown stays armed, zero when it is not.
func (c *Cooldown) Remaining() time.Duration {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return max(c.until.Sub(c.clock.Now()), 0)
}

// Armed reports whether the cooldown refuses calls right now.
func (c *Cooldown) Armed() bool {
	return c.Remaining() > 0
}

// SetMaxCooldown replaces the cap applied to later hints; a deadline already
// set is kept. A non-positive d is ignored.
func (c *Cooldown) SetMaxCooldown(limit time.Duration) {
	if limit <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.limit = limit
}

// observe arms the cooldown from err when err carries a Retry-After hint and
// passes the trigger. It reports the duration it armed for, and false when the
// deadline did not move: no hint, a rejected error, or a deadline already
// later.
func (c *Cooldown) observe(err error) (time.Duration, bool) {
	if c == nil || err == nil {
		return 0, false
	}

	if c.trigger != nil && !c.trigger(err) {
		return 0, false
	}

	hint, ok := retryAfterFromError(err)
	if !ok || hint <= 0 {
		return 0, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	armedFor := min(hint, c.limit)

	until := c.clock.Now().Add(armedFor)
	if !until.After(c.until) {
		return 0, false
	}

	c.until = until

	return armedFor, true
}

// newCooldownEntry builds the cooldown middleware for a policy without a
// retry: it refuses a call while the cooldown is armed (see cooldownGate) and
// arms the cooldown from the call's error.
func newCooldownEntry[T any](cd *Cooldown, hooks *Hooks) PatternEntry[T] {
	return cooldownGate[T](cd, hooks, func(err error) {
		if armedFor, armed := cd.observe(err); armed {
			hooks.emitCooldownArmed(armedFor)
		}
	})
}

// newCooldownGateEntry builds the cooldown middleware for a policy with a
// retry: it only refuses calls, since the retry loop arms the cooldown from
// each attempt (see DoRetry).
func newCooldownGateEntry[T any](cd *Cooldown, hooks *Hooks) PatternEntry[T] {
	return cooldownGate[T](cd, hooks, func(error) {})
}

// cooldownGate refuses a call with ErrCoolingDown while the cooldown is armed,
// so no attempt leaves and the breaker inside never sees the refusal, and
// hands the error of a call it let through to afterCall.
func cooldownGate[T any](cd *Cooldown, hooks *Hooks, afterCall func(error)) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityCooldown,
		Name:     "cooldown",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if cd.Armed() {
					hooks.emitCoolingDown()

					var zero T

					// Returned as-is: IsLocalRejection compares it by
					// identity.
					return zero, ErrCoolingDown
				}

				val, err := next(ctx)
				afterCall(err)

				return val, err
			}
		},
	}
}
