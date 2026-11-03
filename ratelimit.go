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
		burst    int
		burstSet bool
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
	// off under server-signalled overload and recovers it afterwards.
	//
	// The acquire/refill hot path is lock-free. The writers of the rate, the
	// burst and the bucket capacity ([RateLimiter.Reconfigure], the config
	// reload of a policy and the AIMD step) are serialised by rateMu, so the
	// capacity always matches the rate and burst that won.
	// The AIMD step takes rateMu, then the controller's own mutex; nothing takes
	// them in the other order, and no user code (classifier, hook) runs under
	// either.
	RateLimiter struct {
		clock    Clock
		hooks    *Hooks
		aimd     *aimdState      // nil unless AIMD adaptation is enabled
		cfg      rateLimitConfig // grouped with the pointers to keep the GC scan range small
		rate     atomicFloat64   // tokens per second
		capacity atomic.Int64
		tokens   atomic.Int64
		lastNano atomic.Int64
		// burst is the bucket size set by [Burst], in whole tokens, or 0 when
		// the capacity derives from the rate. Guarded by rateMu.
		burst int64
		// rateMu serialises the writers of rate, burst and capacity:
		// Reconfigure, the config reload and the AIMD step. Allow and refill
		// never take it.
		rateMu sync.Mutex
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

	// MaxBurst is the largest bucket [Burst] accepts, in tokens: the most a
	// fixed-point token count can hold, or the largest int on a 32-bit
	// platform.
	MaxBurst = int(min(math.MaxInt64/fixedPointScale, math.MaxInt))
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

// Burst sets the bucket capacity to n tokens, apart from the rate. Without it
// the bucket holds one second of tokens, and at least one. The bucket starts
// full, so n is also the number of calls that can leave at once on start-up.
// [RateLimiter.Reconfigure] and the [AIMD] step change the rate, never a
// capacity set by Burst, with one exception: a rate of zero or less empties
// the bucket, Burst or not, and a later positive rate brings the Burst
// capacity back with an empty bucket. n must be between 1 and [MaxBurst]: any
// other value makes [NewRateLimiter], and so [NewPolicy], panic with
// [ErrRateLimitBurstOutOfRange].
//
// A bucket holds a quota only over its refill window. Against a quota of 450
// calls per sliding hour, Burst(450) at 0.125 req/s lets up to 900 calls through
// in an hour: 450 at once, then 450 more as the bucket refills. Burst(1) at the
// same rate never exceeds 451.
func Burst(n int) RateLimitOption {
	return func(cfg *rateLimitConfig) {
		cfg.burst = n
		cfg.burstSet = true
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
// For a positive rate, the bucket holds one second of tokens, and at least one
// token, so a rate below 1 req/s admits one call every 1/rate seconds; [Burst]
// sets another capacity. A rate of zero or less admits no call. It panics with
// [ErrRateLimitBurstOutOfRange] when a [Burst] is out of range.
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

	if err := cfg.validate(); err != nil {
		panic(err)
	}

	rl := &RateLimiter{
		clock: clock,
		hooks: hooks,
		cfg:   cfg,
		burst: int64(cfg.burst),
	}

	capacity := rl.capacityFor(rate)

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
// The bucket capacity is recomputed (one second of tokens, at least one token
// for a positive rate) unless [Burst] set it, and the current token count is
// clamped to the new capacity. A rate of zero or less, or NaN, empties the
// bucket, Burst or not; a later positive rate brings the Burst capacity back
// with an empty bucket. Safe for concurrent use with Allow and RecordOutcome.
func (rl *RateLimiter) Reconfigure(rate float64) {
	rl.rateMu.Lock()
	defer rl.rateMu.Unlock()

	rl.storeRateLocked(rate)
}

// applyReload applies a config reload to the rate limiter: a new rate, a new
// burst, or both, under one rateMu section, so no other writer (Reconfigure,
// the AIMD step) runs between the burst and the rate change. The lock-free
// refill reads the rate and the capacity separately: one refill window may add
// tokens at the new rate capped at the old capacity, and the post-CAS clamp
// keeps the bucket within the capacity. A nil field is left unchanged. The caller has checked burst
// with validBurst and that the limiter was built with [Burst].
func (rl *RateLimiter) applyReload(rate *float64, burst *int) {
	rl.rateMu.Lock()
	defer rl.rateMu.Unlock()

	if burst != nil {
		rl.burst = int64(*burst)
	}

	newRate := rl.rate.Load()
	if rate != nil {
		newRate = *rate
	}

	rl.storeRateLocked(newRate)
}

// validate reports a [Burst] out of range.
func (c *rateLimitConfig) validate() error {
	if c.burstSet && !validBurst(c.burst) {
		return ErrRateLimitBurstOutOfRange
	}

	return nil
}

// validBurst reports whether n is a bucket size [Burst] accepts.
func validBurst(n int) bool {
	return n >= 1 && n <= MaxBurst
}

// capacityFor returns the bucket size for a refill rate, in fixed-point
// tokens: the [Burst] size when one is set, else the size derived from the
// rate. A rate of zero or less, or NaN, keeps an empty bucket either way. The
// caller holds rl.rateMu, or is building rl.
func (rl *RateLimiter) capacityFor(rate float64) int64 {
	if !(rate > 0) { // also true for NaN, which admits nothing
		return 0
	}

	if rl.burst > 0 {
		return rl.burst * fixedPointScale
	}

	return bucketCapacity(rate)
}

// storeRateLocked publishes a new refill rate: it updates the rate and the
// capacity (see capacityFor), then clamps the live token count down to the new capacity
// (a smaller rate must not leave a backlog larger than the new bucket can
// hold). Growing the rate leaves the current tokens untouched — the larger
// bucket simply fills over time. The cells are atomic, so the lock-free
// acquire/refill path observes the change without coordination. The caller
// holds rl.rateMu, so two writers cannot interleave their stores and leave a
// capacity that does not match the rate.
func (rl *RateLimiter) storeRateLocked(rate float64) {
	newCapacity := rl.capacityFor(rate)

	rl.rate.Store(rate)
	rl.capacity.Store(newCapacity)
	rl.clampTokens(newCapacity)
}

// clampTokens lowers the token count to capacity if it is above it. Both rate
// writers and refill call it: a writer clamps after storing a smaller
// capacity, and refill clamps again after its own CAS, in case it added
// tokens against a capacity loaded before that store. Whichever runs last
// sees the smaller capacity, so the bucket never stays above it.
func (rl *RateLimiter) clampTokens(capacity int64) {
	for {
		current := rl.tokens.Load()
		if current <= capacity {
			return
		}

		if rl.tokens.CompareAndSwap(current, capacity) {
			return
		}
	}
}

// bucketCapacity returns the bucket size for a refill rate, in fixed-point
// tokens: one second of refill, and at least one token for any positive rate.
// A call costs a whole token, so a smaller bucket would never admit a call
// below 1 req/s, and an AIMD rate pushed under 1 req/s could never recover. A
// rate of zero or less, or NaN, keeps an empty bucket: it admits nothing.
func bucketCapacity(rate float64) int64 {
	if !(rate > 0) { // also true for NaN
		return 0
	}

	return int64(max(rate, 1) * float64(fixedPointScale))
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
		capacity := rl.capacity.Load()

		// A long idle period at a high rate can exceed the int64 range: cap
		// the addition at the capacity before converting it.
		addFloat := float64(elapsedNano) * rate

		addTokens := capacity
		if addFloat < float64(capacity) {
			addTokens = int64(addFloat)
		}

		if addTokens <= 0 {
			return
		}

		// Add tokens atomically, capping at capacity. The comparison never
		// overflows, even for a bucket of MaxBurst tokens.
		for {
			oldTokens := rl.tokens.Load()

			newTokens := capacity
			if addTokens < capacity-oldTokens {
				newTokens = oldTokens + addTokens
			}

			if rl.tokens.CompareAndSwap(oldTokens, newTokens) {
				// A rate writer may have stored a smaller capacity, and
				// clamped, after the load above.
				rl.clampTokens(rl.capacity.Load())

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

	newRate, changed := rl.adaptRate(ctrl, now, step)
	if !changed {
		return
	}

	// The hook is user code: it runs with no lock held, so it may call back
	// into the limiter.
	rl.hooks.emitRateAdapted(newRate)
}

// adaptRate runs one AIMD step: it reads the current rate, passes it through
// the controller's interval gate and step, and publishes the result, all under
// rateMu. A concurrent Reconfigure is therefore either seen by the step or
// applied after it, never overwritten by a rate computed from the old one. It
// reports the new rate and whether it changed.
func (rl *RateLimiter) adaptRate(
	ctrl *aimdState,
	now int64,
	step func(float64) float64,
) (float64, bool) {
	rl.rateMu.Lock()
	defer rl.rateMu.Unlock()

	newRate, changed := ctrl.adjust(now, rl.rate.Load(), step)
	if changed {
		rl.storeRateLocked(newRate)
	}

	return newRate, changed
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
