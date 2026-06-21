package r8e

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// AdaptiveLimiter — adaptive concurrency limiting (Gradient2)
// ---------------------------------------------------------------------------.

type (
	// adaptiveConfig holds the parameters for an AdaptiveLimiter before they are
	// validated and stored on the limiter in NewAdaptiveLimiter.
	adaptiveConfig struct {
		initialLimit int
		minLimit     int
		maxLimit     int
		tolerance    float64
	}

	// AdaptiveOption configures an AdaptiveLimiter.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewAdaptiveLimiter's signature stable.
	AdaptiveOption func(*adaptiveConfig)

	// AdaptiveLimiter caps in-flight calls at a concurrency limit that it tunes
	// itself from observed latency, replacing the fixed ceiling of a [Bulkhead].
	// It follows Netflix's Gradient2 algorithm: each completed call samples its
	// round-trip time (RTT); a short-term (current) RTT rising above a smoothed
	// long-term baseline signals queueing downstream, so the limit is lowered,
	// and when latency is steady the limit drifts back up. Calls that arrive
	// while in-flight is at the current limit are rejected with
	// [ErrConcurrencyLimited]; the original request still surfaces that error.
	//
	// The limit only grows while the limiter is actually loaded (in-flight at or
	// above half the limit), so a quiet service is never pushed to probe higher,
	// and it never leaves the configured [MinLimit, MaxLimit] band. Because it is
	// driven by call RTT it needs a [Clock] (injected from the policy), which
	// makes it deterministic under a fake clock in tests.
	//
	// Construct one with NewAdaptiveLimiter; it is safe for concurrent use. The
	// limit, the in-flight count, and the latency baseline move together under
	// one mutex — admitting a call, recording its RTT, and recomputing the limit
	// must stay mutually consistent, which a lock provides and independent
	// atomics do not.
	//
	// Pattern: Adaptive Concurrency Limiter — a TCP-congestion-control-style
	// controller (Gradient2) sizes a concurrency semaphore from live latency
	// feedback instead of a fixed limit.
	AdaptiveLimiter struct {
		clock     Clock
		hooks     *Hooks
		limit     float64
		longRTT   float64
		minLimit  float64
		maxLimit  float64
		tolerance float64
		samples   float64
		inFlight  int
		mu        sync.Mutex
	}
)

const (
	// Gradient2 constants not exposed as options: the queue headroom added to
	// every limit estimate, the EMA smoothing applied to limit changes, and the
	// window of the long-term RTT average. They match Netflix's Gradient2
	// defaults.
	adaptiveQueueSize  = 4.0
	adaptiveSmoothing  = 0.2
	adaptiveLongWindow = 600.0

	// Default limiter parameters. minLimit is 1 (not Netflix's 20) so the limiter
	// is usable for small services; the band can be widened with options.
	defaultInitialLimit  = 20
	defaultMinLimit      = 1
	defaultMaxLimit      = 200
	defaultRTTTolerance = 1.5
	minRTTTolerance     = 1.0
)

// InitialLimit sets the concurrency limit the limiter starts from before it has
// observed any latency. Clamped into the [MinLimit, MaxLimit] band. Default: 20.
func InitialLimit(n int) AdaptiveOption {
	return func(cfg *adaptiveConfig) {
		cfg.initialLimit = n
	}
}

// MinLimit sets the floor the adaptive limit can never drop below; it must be at
// least 1 so the limiter always admits some traffic. Default: 1.
func MinLimit(n int) AdaptiveOption {
	return func(cfg *adaptiveConfig) {
		cfg.minLimit = n
	}
}

// MaxLimit sets the ceiling the adaptive limit can never rise above. Default:
// 200.
func MaxLimit(n int) AdaptiveOption {
	return func(cfg *adaptiveConfig) {
		cfg.maxLimit = n
	}
}

// RTTTolerance sets how much the current RTT may rise above the baseline before
// the limit is reduced: a value of 2.0 tolerates a 2x latency increase. Higher
// is more permissive. Values below 1.0 are clamped to the default. Default: 1.5.
func RTTTolerance(f float64) AdaptiveOption {
	return func(cfg *adaptiveConfig) {
		cfg.tolerance = f
	}
}

// NewAdaptiveLimiter creates an adaptive concurrency limiter. Invalid parameters
// are clamped to defaults rather than panicking, matching NewRateLimiter's
// tolerant construction. The clock supplies call timing; pass a non-nil Clock
// (the policy passes its own) and a non-nil *Hooks (the zero value [Hooks] is
// fine).
func NewAdaptiveLimiter(clock Clock, hooks *Hooks, opts ...AdaptiveOption) *AdaptiveLimiter {
	cfg := adaptiveConfig{
		initialLimit: defaultInitialLimit,
		minLimit:     defaultMinLimit,
		maxLimit:     defaultMaxLimit,
		tolerance:    defaultRTTTolerance,
	}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	return &AdaptiveLimiter{
		clock:     clock,
		hooks:     hooks,
		limit:     float64(cfg.initialLimit),
		minLimit:  float64(cfg.minLimit),
		maxLimit:  float64(cfg.maxLimit),
		tolerance: cfg.tolerance,
	}
}

// clamp repairs out-of-range parameters so the controller arithmetic always has
// a positive, ordered band and a sane tolerance.
func (c *adaptiveConfig) clamp() {
	if c.minLimit < 1 {
		c.minLimit = defaultMinLimit
	}

	if c.maxLimit < c.minLimit {
		c.maxLimit = c.minLimit
	}

	if c.tolerance < minRTTTolerance {
		c.tolerance = defaultRTTTolerance
	}

	// Keep the starting limit inside the band.
	c.initialLimit = min(max(c.initialLimit, c.minLimit), c.maxLimit)
}

// Acquire admits a call when in-flight is below the current limit. On admission
// it returns a completion function to call exactly once when the call finishes
// (typically via defer), which folds the call's round-trip time into the
// controller and retunes the limit. When the limiter is at its limit it returns
// a nil function and [ErrConcurrencyLimited].
//
// Returning a closure rather than a raw start time keeps the timing token
// internal, so it cannot be lost or mis-threaded by the caller.
func (a *AdaptiveLimiter) Acquire() (func(), error) {
	start, ok := a.admit()
	if !ok {
		a.hooks.emitConcurrencyRejected()

		return nil, ErrConcurrencyLimited
	}

	return func() { a.complete(start) }, nil
}

// admit increments in-flight and returns the admission time if a slot is free.
// The hook for a rejection is emitted by Acquire after the lock is released, so
// no user callback runs under the mutex.
func (a *AdaptiveLimiter) admit() (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inFlight >= int(a.limit) {
		return time.Time{}, false
	}

	a.inFlight++

	return a.clock.Now(), true
}

// complete folds a finished call's round-trip time into the controller. The
// OnConcurrencyLimitChanged hook is emitted after the lock is released, on the
// limit captured under it, so no user callback runs under the mutex.
func (a *AdaptiveLimiter) complete(start time.Time) {
	rtt := a.clock.Since(start)

	newLimit, changed := a.observe(rtt)
	if changed {
		a.hooks.emitConcurrencyLimitChanged(newLimit)
	}
}

// observe decrements in-flight and runs one Gradient2 step under the lock,
// returning the new integer limit and whether it moved.
func (a *AdaptiveLimiter) observe(rtt time.Duration) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Netflix samples the in-flight count including the completing call.
	inflight := a.inFlight
	a.inFlight--
	oldLimit := int(a.limit)
	a.recompute(rtt, inflight)
	newLimit := int(a.limit)

	return newLimit, newLimit != oldLimit
}

// recompute runs one Gradient2 control step: fold the sample into the latency
// baseline, then, while the limiter is loaded, adjust the limit from the latency
// gradient. The limit is left unchanged while app-limited (in-flight under half
// the limit), since there is no evidence more concurrency is needed. Must be
// called under a.mu.
func (a *AdaptiveLimiter) recompute(rtt time.Duration, inflight int) {
	// Guard against a zero/negative sample (e.g. an instantaneous call under a
	// fake clock) so the ratios below cannot divide by zero.
	short := max(float64(rtt), 1)

	a.foldBaseline(short)

	if float64(inflight) < a.limit/2 {
		return
	}

	// Floor 0.5 / cap 1.0: Gradient2 never shrinks the limit by more than half in
	// one step, and the RTT ratio alone never grows it — growth comes from the
	// +queueSize headroom. tolerance widens the no-shrink band (a 1.5x RTT rise
	// is tolerated by default).
	gradient := max(0.5, min(1.0, a.tolerance*a.longRTT/short))
	newLimit := a.limit*gradient + adaptiveQueueSize
	newLimit = a.limit*(1-adaptiveSmoothing) + newLimit*adaptiveSmoothing
	a.limit = min(max(newLimit, a.minLimit), a.maxLimit)
}

// foldBaseline maintains the long-term RTT baseline: it folds a sample in (a
// running mean over the first adaptiveLongWindow samples, so it converges
// quickly from the zero value, then a fixed-window exponential moving average),
// and decays the baseline when the current RTT has dropped sharply below it —
// latency returning to normal after a spike — so the limit can recover promptly.
// Must be called under a.mu.
func (a *AdaptiveLimiter) foldBaseline(sample float64) {
	a.samples++

	window := adaptiveLongWindow
	if a.samples < window {
		window = a.samples
	}

	a.longRTT += (sample - a.longRTT) / window

	if a.longRTT/sample > 2 {
		a.longRTT *= 0.95
	}
}

// Reconfigure retunes the limiter at runtime. Options are applied on top of the
// current parameters, so a partial update leaves the others unchanged (matching
// CircuitBreaker.Reconfigure). The live limit is clamped into the new band so a
// narrowed band takes effect immediately. InitialLimit has no effect here — it
// only seeds a fresh limiter. Safe for concurrent use with Acquire.
func (a *AdaptiveLimiter) Reconfigure(opts ...AdaptiveOption) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := adaptiveConfig{
		initialLimit: int(a.limit),
		minLimit:     int(a.minLimit),
		maxLimit:     int(a.maxLimit),
		tolerance:    a.tolerance,
	}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	a.minLimit = float64(cfg.minLimit)
	a.maxLimit = float64(cfg.maxLimit)
	a.tolerance = cfg.tolerance
	a.limit = min(max(a.limit, a.minLimit), a.maxLimit)
}

// Limit returns the current concurrency limit as a point-in-time snapshot.
// Surfaced by Policy.Metrics as a gauge.
func (a *AdaptiveLimiter) Limit() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return int(a.limit)
}

// InFlight returns the number of calls currently admitted. Surfaced by
// Policy.Metrics as a gauge.
func (a *AdaptiveLimiter) InFlight() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight
}

// Saturated reports whether in-flight has reached the current limit, so the next
// Acquire would be rejected. Surfaced as a degraded health condition.
func (a *AdaptiveLimiter) Saturated() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight >= int(a.limit)
}
