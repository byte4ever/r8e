package r8e

import (
	"context"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type rateLimitConfig struct {
	blocking bool
}

// RateLimitOption configures rate limiter behavior.
type RateLimitOption func(*rateLimitConfig)

// RateLimitBlocking makes the rate limiter wait for a token instead of rejecting.
func RateLimitBlocking() RateLimitOption {
	return func(cfg *rateLimitConfig) {
		cfg.blocking = true
	}
}

// ---------------------------------------------------------------------------
// RateLimiter
// ---------------------------------------------------------------------------

// fixedPointScale converts floating-point tokens to fixed-point integers.
// Using 1e9 gives nanosecond-level precision for token fractions.
const fixedPointScale int64 = 1_000_000_000

// RateLimiter controls the rate of calls using a token bucket algorithm.
//
// Pattern: Rate Limiter — token bucket controls call throughput;
// lock-free via atomic CAS for token acquisition and refill.
type RateLimiter struct {
	rate     float64 // tokens per second
	capacity int64   // max tokens in fixed-point (rate * fixedPointScale)
	clock    Clock
	hooks    *Hooks
	cfg      rateLimitConfig

	tokens    atomic.Int64 // current tokens in fixed-point
	lastNano  atomic.Int64 // last refill timestamp (unix nano)
}

// NewRateLimiter creates a rate limiter that allows rate tokens per second.
func NewRateLimiter(rate float64, clock Clock, hooks *Hooks, opts ...RateLimitOption) *RateLimiter {
	var cfg rateLimitConfig
	for _, o := range opts {
		o(&cfg)
	}

	capacity := int64(rate * float64(fixedPointScale))

	rl := &RateLimiter{
		rate:     rate,
		capacity: capacity,
		clock:    clock,
		hooks:    hooks,
		cfg:      cfg,
	}

	// Start with a full bucket.
	rl.tokens.Store(capacity)
	rl.lastNano.Store(clock.Now().UnixNano())

	return rl
}

// refill adds tokens based on elapsed time since the last refill. It uses a
// CAS loop to atomically update both the token count and the last-refill
// timestamp, ensuring lock-free correctness under concurrent access.
func (rl *RateLimiter) refill() {
	for {
		oldLastNano := rl.lastNano.Load()
		nowNano := rl.clock.Now().UnixNano()
		elapsedNano := nowNano - oldLastNano

		if elapsedNano <= 0 {
			return
		}

		// Try to claim this time window by updating lastNano.
		if !rl.lastNano.CompareAndSwap(oldLastNano, nowNano) {
			// Another goroutine refilled; retry to see if there's more elapsed time.
			continue
		}

		// Calculate tokens to add: elapsed_seconds * rate, in fixed-point.
		// elapsedNano * rate gives tokens in nanosecond-scaled units, which is
		// already in our fixed-point representation (since scale = 1e9 = nanos/sec).
		addTokens := int64(float64(elapsedNano) * rl.rate)

		if addTokens <= 0 {
			return
		}

		// Add tokens atomically, capping at capacity.
		for {
			oldTokens := rl.tokens.Load()
			newTokens := oldTokens + addTokens
			if newTokens > rl.capacity {
				newTokens = rl.capacity
			}
			if rl.tokens.CompareAndSwap(oldTokens, newTokens) {
				return
			}
		}
	}
}

// tryAcquire attempts to decrement one token using a CAS loop.
// Returns true if a token was successfully acquired.
func (rl *RateLimiter) tryAcquire() bool {
	oneToken := fixedPointScale

	for {
		current := rl.tokens.Load()
		if current < int64(oneToken) {
			return false
		}
		if rl.tokens.CompareAndSwap(current, current-int64(oneToken)) {
			return true
		}
	}
}

// Allow attempts to acquire a token. In reject mode (default), returns ErrRateLimited
// if no token is available. In blocking mode, waits for a token (respects ctx cancellation).
func (rl *RateLimiter) Allow(ctx context.Context) error {
	// Refill based on elapsed time, then try to acquire.
	rl.refill()
	if rl.tryAcquire() {
		return nil
	}

	// No token available.
	if !rl.cfg.blocking {
		rl.hooks.emitRateLimited()
		return ErrRateLimited
	}

	// Blocking mode: wait for a token, respecting context cancellation.
	for {
		// Check context before sleeping.
		if err := ctx.Err(); err != nil {
			return err
		}

		// Sleep briefly, then retry.
		timer := rl.clock.NewTimer(time.Millisecond)
		select {
		case <-timer.C():
			rl.refill()
			if rl.tryAcquire() {
				return nil
			}
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// Saturated returns true if the bucket is empty (no tokens available).
func (rl *RateLimiter) Saturated() bool {
	rl.refill()
	return rl.tokens.Load() < fixedPointScale
}
