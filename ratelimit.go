package r8e

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------.

type (
	rateLimitConfig struct {
		aimd     *aimdConfig
		blocking bool
	}

	// RateLimitOption configures rate limiter behavior.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewRateLimiter's signature stable.
	RateLimitOption func(*rateLimitConfig)

	// RateLimiter controls the rate of calls using a token bucket algorithm.
	//
	// Pattern: Rate Limiter — token bucket controls call throughput;
	// lock-free via atomic CAS for token acquisition and refill.
	//
	// When AIMD adaptation is enabled (see [AIMD]) the refill rate is no longer
	// fixed: [RateLimiter.RecordOutcome] folds each call's outcome into an
	// additive-increase / multiplicative-decrease controller that backs the rate
	// off under server-signalled overload and recovers it afterwards. The token
	// bucket itself stays lock-free; only the AIMD adjustment is serialised by
	// the aimd controller's own mutex, which never touches the acquire/refill
	// hot path.
	RateLimiter struct {
		clock    Clock
		hooks    *Hooks
		aimd     *aimdState      // nil unless AIMD adaptation is enabled
		cfg      rateLimitConfig // grouped with the pointers to keep the GC scan range small
		rate     atomicFloat64   // tokens per second
		capacity atomic.Int64
		tokens   atomic.Int64
		lastNano atomic.Int64
	}

	// atomicFloat64 is a lock-free float64 cell, storing the value as its
	// IEEE-754 bit pattern in an atomic.Uint64.
	atomicFloat64 struct {
		bits atomic.Uint64
	}

	// aimdConfig holds the AIMD tunables before resolve fills the defaults that
	// depend on the base rate and a RateLimiter stores them. It is the form
	// [AIMDOption] values are applied to, shared by NewRateLimiter and
	// ReconfigureAIMD.
	aimdConfig struct {
		classifier func(error) bool
		minRate    float64
		maxRate    float64
		increase   float64
		backoff    float64
		interval   time.Duration
	}

	// AIMDOption configures the AIMD adaptive rate controller (see [AIMD]).
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping the [AIMD] signature stable.
	AIMDOption func(*aimdConfig)

	// aimdState is a RateLimiter's live AIMD controller: the validated tunables
	// plus the timestamp of the last rate adjustment. Every field is guarded by
	// mu — the controller is consulted and updated infrequently (at most once per
	// interval), so a mutex is simpler and cheaper than the compound atomics a
	// lock-free version would need. nil on a RateLimiter without AIMD.
	aimdState struct {
		classifier func(error) bool
		increase   float64
		backoff    float64
		minRate    float64
		maxRate    float64
		// baseRate is the rate passed to WithRateLimit / NewRateLimiter. It is the
		// stable fallback resolve uses to refill defaults — both at construction
		// and at reconfigure — so a reset-to-default (e.g. AIMDMaxRate(0)) never
		// derives a default from a value being reset, which would otherwise pin
		// the controller to a degenerate rate. It never changes after construction.
		baseRate   float64
		interval   int64 // nanoseconds
		lastAdjust int64 // unixnano of the last adjustment
		mu         sync.Mutex
	}
)

// fixedPointScale converts floating-point tokens to fixed-point integers.
// Using 1e9 gives nanosecond-level precision for token fractions.
const (
	fixedPointScale int64 = 1_000_000_000

	// Default AIMD parameters. backoffRatio 0.9 matches Netflix's AIMDLimit; the
	// 1s interval bounds how often the rate moves (at most one adjustment per
	// interval, so a burst of overload signals causes a single decrease, not one
	// per error); the rate floor and additive step default to a tenth and a
	// twentieth of the configured (ceiling) rate so recovery from one backoff
	// takes a couple of clean intervals.
	defaultAIMDBackoff   = 0.9
	defaultAIMDInterval  = time.Second
	defaultAIMDRateFloor = 10 // minRate default = ceiling / this
	defaultAIMDStepRatio = 20 // additive step default = ceiling / this
)

// Load returns the stored value.
func (a *atomicFloat64) Load() float64 {
	return math.Float64frombits(a.bits.Load())
}

// Store sets the value.
func (a *atomicFloat64) Store(value float64) {
	a.bits.Store(math.Float64bits(value))
}

// RateLimitBlocking makes the rate limiter wait for a token instead of
// rejecting.
func RateLimitBlocking() RateLimitOption {
	return func(cfg *rateLimitConfig) {
		cfg.blocking = true
	}
}

// AIMD enables additive-increase / multiplicative-decrease adaptation of the
// rate limiter's refill rate, turning the configured rate into a starting and
// ceiling value rather than a fixed one. After each call the policy feeds the
// outcome back (via [RateLimiter.RecordOutcome]); an outcome the classifier
// flags as server overload multiplies the rate by [AIMDBackoff] (default 0.9),
// any other outcome adds [AIMDIncrease] back, and the rate is held within
// [AIMDMinRate, AIMDMaxRate]. At most one adjustment is made per [AIMDInterval]
// (default 1s), so a burst of rejections backs the rate off once rather than
// collapsing it.
//
// By default overload means an error carrying a server retry hint (an
// [ErrRateLimited] or any error with a Retry-After via [RetryAfterProvider], as
// the httpx StatusError supplies for HTTP 429/503); a business error does not
// slow the rate. Override the signal with [AIMDClassifier]. The standalone
// constructor is [NewRateLimiter]; through a policy use WithRateLimit(rate,
// AIMD(...)).
func AIMD(opts ...AIMDOption) RateLimitOption {
	return func(cfg *rateLimitConfig) {
		ac := &aimdConfig{}
		for _, o := range opts {
			o(ac)
		}

		cfg.aimd = ac
	}
}

// AIMDMinRate sets the floor the adaptive rate is never reduced below, so a
// struggling backend still receives a trickle of probing traffic. A
// non-positive value resets to the default (one tenth of the ceiling rate).
func AIMDMinRate(rate float64) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.minRate = rate
	}
}

// AIMDMaxRate sets the ceiling the adaptive rate is never raised above. A
// non-positive value resets to the default (the rate passed to WithRateLimit /
// NewRateLimiter), which is the usual contractual maximum.
func AIMDMaxRate(rate float64) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.maxRate = rate
	}
}

// AIMDBackoff sets the multiplicative factor applied to the rate on each
// overload interval (newRate = rate * factor). It must be in (0, 1); an
// out-of-range value resets to the default 0.9.
func AIMDBackoff(factor float64) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.backoff = factor
	}
}

// AIMDIncrease sets the additive step (tokens per second) added back to the rate
// on each clean interval. A non-positive value resets to the default (one
// twentieth of the ceiling rate).
func AIMDIncrease(step float64) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.increase = step
	}
}

// AIMDInterval sets the minimum time between two rate adjustments. A shorter
// interval reacts faster but moves the rate more abruptly; a longer one is
// steadier. A non-positive value resets to the default 1s.
func AIMDInterval(d time.Duration) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.interval = d
	}
}

// AIMDClassifier overrides which downstream errors count as server overload (and
// so trigger a multiplicative decrease). By default an error is overload only if
// it is [ErrRateLimited] or carries a Retry-After hint (see [RetryAfterProvider]);
// every other error — a validation failure, a 404 — leaves the rate untouched. A
// nil classifier restores the default.
func AIMDClassifier(fn func(error) bool) AIMDOption {
	return func(cfg *aimdConfig) {
		cfg.classifier = fn
	}
}

// defaultAIMDOverload is the default AIMD overload signal: an error is overload
// when it is (or wraps) [ErrRateLimited], or carries a server Retry-After hint
// (an HTTP 429/503 surfaced through the httpx StatusError, or any
// [RetryAfterProvider]). It is deliberately std-lib-only — HTTP status parsing
// stays at the httpx edge — so callers needing richer detection pass
// [AIMDClassifier].
func defaultAIMDOverload(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrRateLimited) {
		return true
	}

	_, ok := retryAfterFromError(err)

	return ok
}

// resolve fills the AIMD defaults that depend on the base rate, clamps the
// tunables into their valid ranges, and installs the default classifier when none
// was given. base is the rate passed to WithRateLimit / NewRateLimiter (the
// stable [aimdState.baseRate], the same value on a fresh limiter and on every
// reconfigure) — never a value being reset, so resetting maxRate cannot derive
// its own replacement from the reset value.
func (c *aimdConfig) resolve(base float64) {
	if c.backoff <= 0 || c.backoff >= 1 {
		c.backoff = defaultAIMDBackoff
	}

	if c.interval <= 0 {
		c.interval = defaultAIMDInterval
	}

	if c.maxRate <= 0 {
		c.maxRate = base
	}

	if c.minRate <= 0 {
		c.minRate = c.maxRate / defaultAIMDRateFloor
	}

	if c.minRate > c.maxRate {
		c.minRate = c.maxRate
	}

	if c.increase <= 0 {
		c.increase = c.maxRate / defaultAIMDStepRatio
	}

	if c.classifier == nil {
		c.classifier = defaultAIMDOverload
	}
}

// NewRateLimiter creates a rate limiter that allows rate tokens per second.
func NewRateLimiter(
	rate float64,
	clock Clock,
	hooks *Hooks,
	opts ...RateLimitOption,
) *RateLimiter {
	var cfg rateLimitConfig
	for _, o := range opts {
		o(&cfg)
	}

	capacity := int64(rate * float64(fixedPointScale))

	rl := &RateLimiter{
		clock: clock,
		hooks: hooks,
		cfg:   cfg,
	}

	rl.rate.Store(rate)
	rl.capacity.Store(capacity)
	// Start with a full bucket.
	rl.tokens.Store(capacity)
	rl.lastNano.Store(clock.Now().UnixNano())

	if cfg.aimd != nil {
		cfg.aimd.resolve(rate)
		rl.aimd = newAIMDState(cfg.aimd, rate, clock.Now().UnixNano())
	}

	return rl
}

// newAIMDState builds the live AIMD controller from a resolved config, recording
// the base rate (the WithRateLimit / NewRateLimiter rate) as the stable default
// source and stamping lastAdjust with the construction time so the first
// adjustment waits one full interval rather than firing on the very first
// outcome.
func newAIMDState(cfg *aimdConfig, baseRate float64, nowNano int64) *aimdState {
	return &aimdState{
		classifier: cfg.classifier,
		increase:   cfg.increase,
		backoff:    cfg.backoff,
		minRate:    cfg.minRate,
		maxRate:    cfg.maxRate,
		baseRate:   baseRate,
		interval:   int64(cfg.interval),
		lastAdjust: nowNano,
	}
}

// Reconfigure changes the token-refill rate (tokens per second) at runtime.
// The bucket capacity is recomputed and the current token count is clamped to
// the new capacity. Safe for concurrent use with Allow.
func (rl *RateLimiter) Reconfigure(rate float64) {
	rl.storeRate(rate)
}

// storeRate publishes a new refill rate: it updates the rate and the derived
// capacity, then clamps the live token count down to the new capacity (a smaller
// rate must not leave a backlog larger than the new bucket can hold). Growing the
// rate leaves the current tokens untouched — the larger bucket simply fills over
// time. The rate and capacity cells are atomic, so the lock-free acquire/refill
// path observes the change without coordination; callers that need adjustments
// serialised (Reconfigure, the AIMD controller) provide their own ordering.
func (rl *RateLimiter) storeRate(rate float64) {
	newCapacity := int64(rate * float64(fixedPointScale))

	rl.rate.Store(rate)
	rl.capacity.Store(newCapacity)

	// Clamp the current tokens down to the new capacity.
	for {
		current := rl.tokens.Load()
		if current <= newCapacity {
			return
		}

		if rl.tokens.CompareAndSwap(current, newCapacity) {
			return
		}
	}
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
			// Another goroutine refilled; retry to see if there's more elapsed
			// time.
			continue
		}

		// Calculate tokens to add: elapsed_seconds * rate, in fixed-point.
		// elapsedNano * rate gives tokens in nanosecond-scaled units, which is
		// already in our fixed-point representation (since scale = 1e9 =
		// nanos/sec).
		rate := rl.rate.Load()
		addTokens := int64(float64(elapsedNano) * rate)

		if addTokens <= 0 {
			return
		}

		capacity := rl.capacity.Load()

		// Add tokens atomically, capping at capacity.
		for {
			oldTokens := rl.tokens.Load()

			newTokens := oldTokens + addTokens
			if newTokens > capacity {
				newTokens = capacity
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
		if current < oneToken {
			return false
		}

		if rl.tokens.CompareAndSwap(current, current-oneToken) {
			return true
		}
	}
}

// Allow attempts to acquire a token. In reject mode (default), returns
// ErrRateLimited if no token is available. In blocking mode, waits for a token
// (respects ctx cancellation).
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
			return err //nolint:wrapcheck // preserving context error identity
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

			return ctx.Err() //nolint:wrapcheck // preserving context error identity
		}
	}
}

// Saturated returns true if the bucket is empty (no tokens available).
//
// It is not side-effect-free: like Allow it first refills the bucket for
// elapsed time (an atomic CAS update), so calling it from a health probe
// advances the limiter's refill clock. This is safe for concurrent use but
// means HealthStatus is an observer that also nudges refill timing.
func (rl *RateLimiter) Saturated() bool {
	rl.refill()
	return rl.tokens.Load() < fixedPointScale
}

// CurrentRate returns the limiter's current refill rate in tokens per second.
// Without AIMD this is the configured (or last Reconfigured) rate; with AIMD it
// is the live adapted rate, moving within [AIMDMinRate, AIMDMaxRate].
func (rl *RateLimiter) CurrentRate() float64 {
	return rl.rate.Load()
}

// RecordOutcome feeds one completed call's outcome into the AIMD controller. An
// outcome the classifier flags as server overload decreases the rate
// multiplicatively; any other outcome increases it additively; the rate stays
// within [AIMDMinRate, AIMDMaxRate]. At most one adjustment is applied per
// [AIMDInterval], so a burst of overload signals backs the rate off once rather
// than repeatedly. It is a no-op on a limiter without AIMD and is safe for
// concurrent use. Pair it with [RateLimiter.Allow] — the policy calls it once per
// user call, after the inner work returns.
func (rl *RateLimiter) RecordOutcome(err error) {
	ctrl := rl.aimd
	if ctrl == nil {
		return
	}

	overload := ctrl.classify(err)
	now := rl.clock.Now().UnixNano()

	// The AIMD step: multiplicative decrease on overload, additive increase
	// otherwise, each clamped to its bound. It is a closure so adjust stays
	// direction-agnostic (it gates and stamps; the step decides the value). adjust
	// invokes it under ctrl.mu, so it reads the lock-guarded tunables directly.
	step := func(cur float64) float64 {
		if overload {
			return max(ctrl.minRate, cur*ctrl.backoff)
		}

		return min(ctrl.maxRate, cur+ctrl.increase)
	}

	newRate, changed := ctrl.adjust(now, rl.rate.Load(), step)
	if !changed {
		return
	}

	rl.storeRate(newRate)
	rl.hooks.emitRateAdapted(newRate)
}

// classify snapshots the classifier under the lock (so a concurrent
// ReconfigureAIMD cannot race the read) and invokes it outside the lock, never
// running user code in the critical section.
func (a *aimdState) classify(err error) bool {
	a.mu.Lock()
	classifier := a.classifier
	a.mu.Unlock()

	return classifier(err)
}

// adjust applies step to cur and stamps lastAdjust when the rate changes, all
// under the lock so concurrent outcomes cannot both move the rate within one
// interval. step is invoked under the lock and may read the tunables directly.
// It returns the new rate and whether it changed: the call is a no-op when the
// interval since the last adjustment has not elapsed, or when the rate is already
// pinned at the relevant bound (so a pinned rate never consumes the interval,
// leaving the controller free to react the instant the other direction is
// wanted).
func (a *aimdState) adjust(now int64, cur float64, step func(float64) float64) (float64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if now-a.lastAdjust < a.interval {
		return cur, false
	}

	// Exact float equality is the right "already pinned at a bound" test: at a
	// bound the clamp returns the bound value verbatim (min(maxRate, cur+increase)
	// == maxRate when cur == maxRate; max(minRate, cur*backoff) == minRate when
	// cur == minRate), so next == cur holds exactly and the interval is not
	// consumed — leaving the controller free to move the other way immediately.
	next := step(cur)
	if next == cur {
		return cur, false
	}

	a.lastAdjust = now

	return next, true
}

// ReconfigureAIMD overlays new AIMD tunables at runtime: any option left unset
// keeps its current value. It returns [ErrAIMDWithoutRateLimit] if the limiter
// was not built with [AIMD] (adaptation cannot be enabled after construction,
// matching how the other adaptive patterns reconfigure their parameters but not
// their presence). Safe for concurrent use with Allow and RecordOutcome.
func (rl *RateLimiter) ReconfigureAIMD(opts ...AIMDOption) error {
	if rl.aimd == nil {
		return ErrAIMDWithoutRateLimit
	}

	rl.aimd.reconfigure(opts...)

	return nil
}

// reconfigure overlays new tunables onto the live AIMD state: it seeds an
// aimdConfig from the current values, applies the options, re-resolves against
// the stable base rate, and writes them back — all under the lock. An option left
// unset keeps its current value; an option that explicitly passes a non-positive
// value resets that field to its default (derived from baseRate, never from the
// value being reset). The last-adjust timestamp is left untouched so a runtime
// reconfigure does not reset the interval gate.
func (a *aimdState) reconfigure(opts ...AIMDOption) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := a.currentConfigLocked()
	for _, o := range opts {
		o(&cfg)
	}

	cfg.resolve(a.baseRate)

	a.classifier = cfg.classifier
	a.increase = cfg.increase
	a.backoff = cfg.backoff
	a.minRate = cfg.minRate
	a.maxRate = cfg.maxRate
	a.interval = int64(cfg.interval)
}

// currentConfigLocked snapshots the live AIMD tunables as an aimdConfig — the
// read-back mirror ReconfigureAIMD seeds from so a partial update overlays the
// current values. Must be called with a.mu held.
func (a *aimdState) currentConfigLocked() aimdConfig {
	return aimdConfig{
		classifier: a.classifier,
		minRate:    a.minRate,
		maxRate:    a.maxRate,
		increase:   a.increase,
		backoff:    a.backoff,
		interval:   time.Duration(a.interval),
	}
}
