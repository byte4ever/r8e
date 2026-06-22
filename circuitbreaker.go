package r8e

import (
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------.

type (
	circuitBreakerConfig struct {
		failureThreshold    int
		recoveryTimeout     time.Duration
		halfOpenMaxAttempts int

		// Slow-call-rate trip (opt-in via SlowCallRate). slowCallDuration is the
		// latency above which a completed call is "slow"; slowCallRateThreshold
		// is the fraction of slow calls in the window that opens the breaker.
		// Detection is OFF unless both are > 0. The window is count-based: the
		// last slowCallWindow verdicts, evaluated only once slowCallMinCalls have
		// been observed.
		slowCallDuration      time.Duration
		slowCallRateThreshold float64
		slowCallWindow        int
		slowCallMinCalls      int

		// Adaptive recovery backoff (opt-in via RecoveryBackoffMultiplier).
		// After each failed half-open probe, the recovery wait is multiplied by
		// recoveryBackoffMultiplier. A value <= 0 disables the feature (default).
		// recoveryMaxBackoff caps the computed duration; 0 means no cap.
		recoveryBackoffMultiplier float64
		recoveryMaxBackoff        time.Duration

		// Slow-start ramp recovery (opt-in via RampRecovery). After the breaker
		// recovers through half-open, admission grows from rampInitialFraction to
		// full over rampRecoveryWindow following the Envoy slow-start curve
		// max(rampInitialFraction, timeFactor^(1/rampAggression)). A window <= 0
		// disables it (default): half-open closes straight to full traffic.
		rampRecoveryWindow  time.Duration
		rampAggression      float64
		rampInitialFraction float64
	}

	// CircuitBreakerOption configures a circuit breaker.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config; each constructor returns one, keeping NewCircuitBreaker's
	// signature stable as options are added.
	CircuitBreakerOption func(*circuitBreakerConfig)

	// CircuitState is the lifecycle state of a [CircuitBreaker], reported by
	// [CircuitBreaker.State]. Its values are the Circuit* constants; using a
	// named type lets consumers switch on the constants rather than matching
	// bare string literals that could silently drift.
	CircuitState string

	// CircuitBreaker tracks the health of a dependency and fails fast when it's
	// down.
	//
	// Pattern: Circuit Breaker — fast-fails calls to unhealthy downstream;
	// auto-recovers via half-open probe after timeout. State transitions are
	// guarded by a mutex so the (state, counters) tuple mutates atomically as a
	// unit — the cheap, linearizable choice the Go concurrency guidance
	// prescribes for a multi-field state machine.
	CircuitBreaker struct {
		clock Clock
		hooks *Hooks

		// sampler draws the [0, 1) value compared against the ramp admission
		// fraction while ramping (see rampFractionAt); rand.Float64 in production,
		// overridable in tests for determinism. Read without the lock — it is set
		// once at construction and never mutated.
		sampler func() float64

		lastFailure time.Time

		// rampStart is when the breaker entered the ramping state, the origin for
		// the slow-start curve. Guarded by mu.
		rampStart time.Time

		// slowWin is the count-based slow-call window (see slowCallWindow),
		// allocated lazily on first observation. Guarded by mu.
		slowWin slowCallWindow

		cfg circuitBreakerConfig

		failureCount      int
		halfOpenSuccesses int
		halfOpenInFlight  int // probes currently admitted in half-open

		// recoveryAttempt counts consecutive failed half-open probes since the last
		// closed→open transition. Used by currentRecoveryTimeout to scale the next
		// recovery wait. Reset to zero when the breaker closes, or on a new trip
		// from closed state. Guarded by mu.
		recoveryAttempt int

		mu    sync.Mutex
		state uint32 // stateClosed | stateOpen | stateHalfOpen
	}

	// slowCallWindow is a count-based sliding window of the most recent slow/fast
	// call verdicts. slow mirrors the number of slow entries so the fraction is
	// O(1) per observation; filled is how many of the ring's slots have been
	// written (capped at the ring length). It is not safe for concurrent use —
	// the circuit breaker guards it with its mutex.
	slowCallWindow struct {
		ring   []bool
		pos    int
		filled int
		slow   int
	}

	// callInput is the raw measurement of one completed call handed to the
	// breaker: how long it took and whether it returned an error.
	callInput struct {
		elapsed time.Duration
		failed  bool
	}

	// callOutcome is a call classified for the state machine: whether it failed
	// and whether it was slow (its latency exceeded the threshold). Built once in
	// recordOutcome and never mutated afterwards. Passing it as a struct rather
	// than as separate bool parameters keeps the recordX helpers free of
	// control-flag coupling.
	callOutcome struct {
		failed bool
		slow   bool
	}
)

// Circuit breaker states.
const (
	stateClosed   uint32 = 0
	stateOpen     uint32 = 1
	stateHalfOpen uint32 = 2
	stateRamping  uint32 = 3

	// CircuitClosed is the state in which calls pass through normally.
	CircuitClosed CircuitState = "closed"
	// CircuitOpen is the state in which calls fail fast without reaching the
	// dependency.
	CircuitOpen CircuitState = "open"
	// CircuitHalfOpen is the state in which a bounded number of probe calls are
	// admitted to test whether the dependency has recovered.
	CircuitHalfOpen CircuitState = "half_open"
	// CircuitRamping is the slow-start state entered after recovery: a growing
	// fraction of traffic is admitted over the ramp window before the breaker
	// fully closes (see [RampRecovery]).
	CircuitRamping CircuitState = "ramping"
)

func defaultCircuitBreakerConfig() circuitBreakerConfig {
	return circuitBreakerConfig{
		failureThreshold:    5,
		recoveryTimeout:     30 * time.Second,
		halfOpenMaxAttempts: 1,
		// Slow-call detection is disabled by default (slowCallDuration and
		// slowCallRateThreshold are zero); the window sizes are pre-seeded so
		// SlowCallRate alone enables a usable detector without further tuning.
		slowCallWindow:   100,
		slowCallMinCalls: 10,
		// Ramp recovery is disabled by default (rampRecoveryWindow is zero); the
		// curve params are pre-seeded so RampRecovery alone yields a sensible
		// linear ramp from 10% without further tuning.
		rampAggression:      1.0,
		rampInitialFraction: 0.1,
	}
}

// FailureThreshold sets the number of consecutive failures before opening.
func FailureThreshold(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.failureThreshold = n
	}
}

// RecoveryTimeout sets how long to wait in open state before transitioning to
// half-open.
func RecoveryTimeout(d time.Duration) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.recoveryTimeout = d
	}
}

// HalfOpenMaxAttempts sets the number of successful probes needed to close from
// half-open.
func HalfOpenMaxAttempts(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.halfOpenMaxAttempts = n
	}
}

// SlowCallRate enables slow-call-rate tripping (off by default): a completed
// call whose latency exceeds duration is "slow", and the breaker opens when the
// fraction of slow calls in the recent window reaches rate (in (0, 1]). This
// catches "brownouts" — a downstream that is slow but not yet failing — which
// the failure-count trip alone misses. It is independent of and additive to the
// consecutive-failure trip (see [FailureThreshold]); the breaker opens on
// whichever condition fires first.
//
// Latency is measured with the breaker's [Clock] across the work the breaker
// wraps, which (inside a [Policy]) includes any inner retry and hedge attempts
// — the same granularity at which the breaker records success and failure.
//
// rate is clamped to [0, 1] and duration must be > 0; if either resolves to a
// non-positive enabling value the detector stays off. Tune the window with
// [SlowCallWindow] and [SlowCallMinCalls].
func SlowCallRate(duration time.Duration, rate float64) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.slowCallDuration = duration
		cfg.slowCallRateThreshold = clampUnitInterval(rate)
	}
}

// SlowCallWindow sets the size of the count-based slow-call window — the number
// of most-recent calls whose slow/fast verdicts are aggregated into the rate.
// Values below 1 are ignored. Default 100. Has no effect unless slow-call
// detection is enabled via [SlowCallRate].
func SlowCallWindow(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		if n >= 1 {
			cfg.slowCallWindow = n
		}
	}
}

// SlowCallMinCalls sets the minimum number of observed calls before the
// slow-call rate is evaluated, so the breaker does not trip on a tiny,
// unrepresentative sample. Values below 1 are ignored. Default 10. Has no
// effect unless slow-call detection is enabled via [SlowCallRate].
func SlowCallMinCalls(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		if n >= 1 {
			cfg.slowCallMinCalls = n
		}
	}
}

// RecoveryBackoffMultiplier enables exponential backoff on the recovery timeout
// after consecutive failed half-open probes. After each probe that re-opens the
// breaker, the next recovery wait is recoveryTimeout × factor^n, where n is the
// number of consecutive failed probes. The first trip from closed always waits
// one full recoveryTimeout (n=0). Pair with [RecoveryMaxBackoff] to cap growth.
//
// A factor ≤ 0 disables backoff (default: no backoff, base timeout always used).
// A factor between 0 and 1 shortens the wait on each probe (anti-backoff — use
// with caution). A factor > 1 is the typical use case.
func RecoveryBackoffMultiplier(factor float64) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.recoveryBackoffMultiplier = factor
	}
}

// RecoveryMaxBackoff caps the recovery timeout computed by
// [RecoveryBackoffMultiplier]. It has no effect unless RecoveryBackoffMultiplier
// is set to a value > 0. A non-positive duration means no configured cap.
// Default: 0 (no cap).
func RecoveryMaxBackoff(d time.Duration) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.recoveryMaxBackoff = d
	}
}

// RampRecovery enables slow-start ramp recovery (off by default). After the
// breaker recovers through half-open it does not jump straight to full traffic
// but enters the [CircuitRamping] state and admits a growing fraction over
// window — easing a healing downstream back to load rather than slamming it.
// During the ramp, shed calls are rejected with [ErrCircuitRamping], and a
// failed or slow call reopens the breaker. A window <= 0 disables the feature.
// Tune the curve with [RampAggression] and the starting fraction with
// [RampInitialFraction].
func RampRecovery(window time.Duration) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.rampRecoveryWindow = window
	}
}

// RampAggression sets the curvature of the ramp admission curve,
// max(initial, timeFactor^(1/aggression)). The default 1.0 ramps admission
// linearly across the window; a value > 1 ramps up faster early (convex), a
// value in (0, 1) slower. Values <= 0 are ignored. Has no effect unless ramp
// recovery is enabled via [RampRecovery].
func RampAggression(a float64) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		if a > 0 {
			cfg.rampAggression = a
		}
	}
}

// RampInitialFraction sets the fraction of traffic admitted at the very start of
// the ramp, in [0, 1] (clamped). It floors the admission curve so the breaker
// never admits less than this share while ramping. Default 0.1 (10%, matching
// Envoy's slow-start minimum weight). Has no effect unless ramp recovery is
// enabled via [RampRecovery].
func RampInitialFraction(f float64) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.rampInitialFraction = clampUnitInterval(f)
	}
}

// clampUnitInterval clamps rate into [0, 1], the valid range for a fraction.
func clampUnitInterval(rate float64) float64 {
	if rate < 0 {
		return 0
	}

	if rate > 1 {
		return 1
	}

	return rate
}

// currentRecoveryTimeout returns the effective recovery wait for the current
// open period. With no backoff configured (the default) it returns the base
// recoveryTimeout unchanged. With [RecoveryBackoffMultiplier] > 0 it scales
// the base by factor^recoveryAttempt, optionally capped by recoveryMaxBackoff.
// Caller must hold mu.
func (cb *CircuitBreaker) currentRecoveryTimeout() time.Duration {
	if cb.cfg.recoveryBackoffMultiplier <= 0 || cb.recoveryAttempt == 0 {
		return cb.cfg.recoveryTimeout
	}

	factor := math.Pow(cb.cfg.recoveryBackoffMultiplier, float64(cb.recoveryAttempt))

	// Guard against overflow when converting to int64 (time.Duration). We use
	// 9e18 rather than float64(math.MaxInt64) because the latter rounds up to
	// 2^63 (9.22e18), which overflows back to negative on int64 conversion.
	// 9e18 is a safe conservative bound (~285 years).
	const safeMax = 9e18

	ns := float64(cb.cfg.recoveryTimeout) * factor
	if ns > safeMax || math.IsInf(ns, 1) {
		ns = safeMax
	}

	d := time.Duration(ns)
	if cb.cfg.recoveryMaxBackoff > 0 && d > cb.cfg.recoveryMaxBackoff {
		return cb.cfg.recoveryMaxBackoff
	}

	return d
}

// NewCircuitBreaker creates a circuit breaker with the given options.
func NewCircuitBreaker(
	clock Clock,
	hooks *Hooks,
	opts ...CircuitBreakerOption,
) *CircuitBreaker {
	cfg := defaultCircuitBreakerConfig()
	for _, o := range opts {
		o(&cfg)
	}

	return &CircuitBreaker{
		clock:   clock,
		hooks:   hooks,
		sampler: rand.Float64,
		cfg:     cfg,
	}
}

// Reconfigure updates the breaker's thresholds at runtime using the same
// options as [NewCircuitBreaker]. The current state and counters are
// preserved; the new thresholds apply to subsequent decisions. One exception:
// changing the slow-call window size (see [SlowCallWindow]) resets that window's
// accumulated history on the next recorded call. When [RecoveryBackoffMultiplier]
// transitions from disabled (≤ 0) to enabled (> 0), the accumulated probe-failure
// counter is reset so the first probe after reconfiguration uses the base timeout.
// Ramp-recovery options (see [RampRecovery]) apply to subsequent admission
// decisions; a breaker already in the [CircuitRamping] state keeps ramping
// against the new window and curve (a shortened window simply completes sooner).
func (cb *CircuitBreaker) Reconfigure(opts ...CircuitBreakerOption) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	prevMultiplier := cb.cfg.recoveryBackoffMultiplier

	for _, opt := range opts {
		opt(&cb.cfg)
	}

	if prevMultiplier <= 0 && cb.cfg.recoveryBackoffMultiplier > 0 {
		cb.recoveryAttempt = 0
	}
}

// Allow checks if a call should be allowed. Returns nil if the breaker is
// closed, or half-open with a probe slot available. Returns ErrCircuitOpen if
// the breaker is open and the recovery timeout hasn't elapsed, or if half-open
// already has halfOpenMaxAttempts probes in flight.
// The state-transition methods capture the lifecycle hook to fire in a local
// and invoke it AFTER releasing cb.mu, so a user-supplied callback can never
// run inside the critical section (which would deadlock on re-entry or stall
// every caller behind a slow hook).

func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()

	var (
		emit func()
		err  error
	)

	switch cb.state {
	case stateOpen:
		if cb.clock.Since(cb.lastFailure) <= cb.currentRecoveryTimeout() {
			err = ErrCircuitOpen

			break
		}

		// Recovery timeout elapsed: transition to half-open and admit this
		// call as the first probe.
		cb.state = stateHalfOpen
		cb.halfOpenSuccesses = 0
		cb.halfOpenInFlight = 1
		emit = cb.hooks.emitCircuitHalfOpen

	case stateHalfOpen:
		// Admit at most halfOpenMaxAttempts concurrent probes; reject the rest
		// so a recovering downstream is not hit by a thundering herd.
		if cb.halfOpenInFlight >= cb.cfg.halfOpenMaxAttempts {
			err = ErrCircuitOpen

			break
		}

		cb.halfOpenInFlight++

	case stateRamping:
		elapsed := cb.clock.Since(cb.rampStart)
		if elapsed >= cb.cfg.rampRecoveryWindow {
			// Ramp window elapsed: recovery is complete — close and admit.
			emit = cb.closeLocked()

			break
		}

		// Admit a growing fraction of traffic and shed the rest, so a still-
		// fragile downstream sees load ramp up rather than spike to full.
		if cb.sampler() >= cb.rampFractionAt(elapsed) {
			err = ErrCircuitRamping
		}

	default:
		// stateClosed: allow the call.
	}

	cb.mu.Unlock()

	if emit != nil {
		emit()
	}

	return err
}

// Record observes a completed call: its latency (measured with the breaker's
// [Clock]) and whether it failed. It folds the call into the consecutive-
// failure trip and, when slow-call detection is enabled (see [SlowCallRate]),
// into the slow-call-rate trip. Prefer this over [CircuitBreaker.RecordSuccess]
// / [CircuitBreaker.RecordFailure] when slow-call detection is enabled, so the
// call's latency is taken into account; those two treat the call as fast.
func (cb *CircuitBreaker) Record(elapsed time.Duration, err error) {
	cb.recordOutcome(callInput{elapsed: elapsed, failed: err != nil})
}

// RecordSuccess records a successful call, treated as fast (latency 0). With
// slow-call detection enabled (see [SlowCallRate]) it records a non-slow verdict
// regardless of the real latency, so use [CircuitBreaker.Record] for
// latency-aware recording.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.recordOutcome(callInput{})
}

// RecordFailure records a failed call, treated as fast (latency 0). With
// slow-call detection enabled (see [SlowCallRate]) it records a non-slow verdict
// regardless of the real latency, so use [CircuitBreaker.Record] for
// latency-aware recording.
func (cb *CircuitBreaker) RecordFailure() {
	cb.recordOutcome(callInput{failed: true})
}

// recordOutcome is the single entry point behind Record/RecordSuccess/
// RecordFailure. It classifies the call (deriving the slow verdict and pushing
// it into the window), updates the failure counter and any resulting state
// transition under one lock, then fires the captured lifecycle hook outside the
// critical section (see Allow).
func (cb *CircuitBreaker) recordOutcome(in callInput) {
	cb.mu.Lock()

	out := callOutcome{failed: in.failed}
	if cb.slowCallEnabled() {
		out.slow = in.elapsed > cb.cfg.slowCallDuration
		cb.slowWin.observe(out, cb.cfg.slowCallWindow)
	}

	var emit func()

	switch cb.state {
	case stateClosed:
		emit = cb.recordClosed(out)
	case stateHalfOpen:
		emit = cb.recordHalfOpen(out)
	case stateRamping:
		emit = cb.recordRamping(out)
	default:
		// stateOpen: a failure recorded while already open drives no transition
		// but still advances the recovery baseline (the historical contract —
		// reachable only via a standalone caller, since Allow rejects first).
		if out.failed {
			cb.lastFailure = cb.clock.Now()
		}
	}

	cb.mu.Unlock()

	if emit != nil {
		emit()
	}
}

// openLocked transitions the breaker to open: it sets the state, resets the
// half-open probe counters, and (re)starts the recovery clock from now. It is
// the sole writer of lastFailure on an open transition (the non-opening failure
// paths stamp it themselves), and returns the supplied trip hook for the caller
// to fire after unlock. Callers are responsible for updating recoveryAttempt
// before calling (recordClosed resets it; recordHalfOpen bumps it via
// bumpRecoveryAttemptLocked). Caller must hold mu.
func (cb *CircuitBreaker) openLocked(emit func()) func() {
	cb.state = stateOpen
	cb.halfOpenSuccesses = 0
	cb.halfOpenInFlight = 0
	cb.lastFailure = cb.clock.Now()

	return emit
}

// bumpRecoveryAttemptLocked increments recoveryAttempt when adaptive recovery
// backoff is configured (recoveryBackoffMultiplier > 0). Called by
// recordHalfOpen before a half-open → open transition. Caller must hold mu.
func (cb *CircuitBreaker) bumpRecoveryAttemptLocked() {
	if cb.cfg.recoveryBackoffMultiplier > 0 {
		cb.recoveryAttempt++
	}
}

// recordClosed applies a closed-state outcome and returns the hook to fire (or
// nil). The breaker opens on whichever trips first: the consecutive-failure
// count reaching failureThreshold — which takes precedence on a call that is
// both failing and slow — or, independently, the slow-call rate reaching its
// threshold (which can happen on a slow but successful call). Caller must hold
// mu.
func (cb *CircuitBreaker) recordClosed(out callOutcome) func() {
	if out.failed {
		cb.failureCount++
		if cb.failureCount >= cb.cfg.failureThreshold {
			cb.recoveryAttempt = 0
			return cb.openLocked(cb.hooks.emitCircuitOpen)
		}
	} else {
		cb.failureCount = 0
	}

	if cb.slowCallEnabled() &&
		cb.slowWin.tripped(cb.cfg.slowCallMinCalls, cb.cfg.slowCallRateThreshold) {
		cb.recoveryAttempt = 0
		return cb.openLocked(cb.emitOpenedBySlowCall)
	}

	if out.failed {
		// A failure that tripped neither the count nor the slow rate still
		// advances the recovery baseline (the historical contract).
		cb.lastFailure = cb.clock.Now()
	}

	return nil
}

// recordHalfOpen applies a half-open probe outcome and returns the hook to fire
// (or nil). A failed OR slow probe means the downstream is still unhealthy and
// reopens the breaker; only a fast success counts toward closing. Caller must
// hold mu.
func (cb *CircuitBreaker) recordHalfOpen(out callOutcome) func() {
	cb.releaseProbe()

	if out.failed {
		cb.bumpRecoveryAttemptLocked()
		return cb.openLocked(cb.hooks.emitCircuitOpen)
	}

	if out.slow {
		// Reopened by a slow (not failed) probe — surface the slow-call reason.
		cb.bumpRecoveryAttemptLocked()
		return cb.openLocked(cb.emitOpenedBySlowCall)
	}

	cb.halfOpenSuccesses++
	if cb.halfOpenSuccesses < cb.cfg.halfOpenMaxAttempts {
		return nil
	}

	// Enough probes succeeded. With ramp recovery enabled, ease traffic back in
	// gradually through the ramping state instead of jumping straight to full
	// load.
	if cb.rampEnabled() {
		return cb.enterRampLocked()
	}

	return cb.closeLocked()
}

// closeLocked transitions the breaker to the closed state, clearing the failure
// and probe counters and resetting the adaptive-recovery backoff so the next
// trip starts from the base recoveryTimeout. It returns the close hook for the
// caller to fire after unlock. Used both when half-open closes directly and when
// the ramp window completes (see Allow). Caller must hold mu.
func (cb *CircuitBreaker) closeLocked() func() {
	cb.state = stateClosed
	cb.failureCount = 0
	cb.halfOpenSuccesses = 0
	cb.halfOpenInFlight = 0
	cb.recoveryAttempt = 0

	return cb.hooks.emitCircuitClose
}

// enterRampLocked transitions a recovered half-open breaker into the ramping
// state, where admission grows from rampInitialFraction to full over
// rampRecoveryWindow. It stamps the ramp origin on the breaker's clock and
// clears the half-open probe counters. recoveryAttempt is deliberately left
// untouched: reaching the ramp is only partial recovery, so a failure during the
// ramp keeps growing the adaptive backoff; only a full close (closeLocked)
// resets it. Caller must hold mu.
func (cb *CircuitBreaker) enterRampLocked() func() {
	cb.state = stateRamping
	cb.rampStart = cb.clock.Now()
	cb.halfOpenSuccesses = 0
	cb.halfOpenInFlight = 0

	return cb.hooks.emitCircuitRamping
}

// recordRamping applies an outcome observed while ramping. A failed or slow call
// proves the downstream is not yet healthy and reopens the breaker — bumping the
// adaptive-recovery backoff, exactly like a failed half-open probe. A fast
// success lets the ramp continue; its progression is purely time-driven, so the
// ramping → closed transition happens lazily in Allow once the window elapses.
// Caller must hold mu.
func (cb *CircuitBreaker) recordRamping(out callOutcome) func() {
	if out.failed {
		cb.bumpRecoveryAttemptLocked()
		return cb.openLocked(cb.hooks.emitCircuitOpen)
	}

	if out.slow {
		cb.bumpRecoveryAttemptLocked()
		return cb.openLocked(cb.emitOpenedBySlowCall)
	}

	return nil
}

// emitOpenedBySlowCall fires both the circuit-open transition and the
// slow-call-rate cause hook, so a slow-call open is counted as a circuit open
// AND surfaced as the specific cause (SlowCallRateExceeded is a subset of
// CircuitOpens).
func (cb *CircuitBreaker) emitOpenedBySlowCall() {
	cb.hooks.emitCircuitOpen()
	cb.hooks.emitSlowCallRateExceeded()
}

// slowCallEnabled reports whether slow-call-rate tripping is active. Both the
// duration and the rate threshold must be positive (see [SlowCallRate]).
func (cb *CircuitBreaker) slowCallEnabled() bool {
	return cb.cfg.slowCallDuration > 0 && cb.cfg.slowCallRateThreshold > 0
}

// rampEnabled reports whether slow-start ramp recovery is active (see
// [RampRecovery]).
func (cb *CircuitBreaker) rampEnabled() bool {
	return cb.cfg.rampRecoveryWindow > 0
}

// rampFractionAt returns the fraction of traffic to admit, in [0, 1], for a call
// arriving elapsed into the current ramp window, using the breaker's configured
// curve. Caller ensures rampRecoveryWindow > 0.
func (cb *CircuitBreaker) rampFractionAt(elapsed time.Duration) float64 {
	return rampFraction(
		elapsed,
		cb.cfg.rampRecoveryWindow,
		cb.cfg.rampAggression,
		cb.cfg.rampInitialFraction,
	)
}

// rampFraction computes the Envoy slow-start admission fraction in [initial, 1]:
// max(initial, timeFactor^(1/aggression)) where timeFactor is elapsed/window
// clamped to [0, 1]. It assumes window > 0; a non-positive aggression falls back
// to linear (1.0), and an elapsed past the window admits everything. Kept as a
// free function so the pure curve math is unit- and fuzz-testable in isolation.
func rampFraction(elapsed, window time.Duration, aggression, initial float64) float64 {
	if elapsed >= window {
		return 1
	}

	timeFactor := float64(elapsed) / float64(window)
	if timeFactor < 0 {
		timeFactor = 0
	}

	exponent := 1.0
	if aggression > 0 {
		exponent = 1 / aggression
	}

	return math.Max(initial, math.Pow(timeFactor, exponent))
}

// observe appends one call outcome's slow/fast verdict, sizing the ring to size
// on first use and reallocating it — which resets the accumulated history —
// whenever size changes (e.g. after a [SlowCallWindow] reconfigure). A size
// below 1 is floored to 1 so the ring is always indexable.
func (w *slowCallWindow) observe(out callOutcome, size int) {
	length := size
	if length < 1 {
		length = 1
	}

	if len(w.ring) != length {
		// A fixed-size ring addressed purely by index (never appended to), so
		// the non-zero make length is intentional.
		w.ring = make([]bool, length) //nolint:makezero // index-addressed ring
		w.pos, w.filled, w.slow = 0, 0, 0
	}

	if w.filled == len(w.ring) {
		if w.ring[w.pos] {
			w.slow--
		}
	} else {
		w.filled++
	}

	w.ring[w.pos] = out.slow
	if out.slow {
		w.slow++
	}

	w.pos = (w.pos + 1) % len(w.ring)
}

// fraction is the current slow-call fraction in [0, 1], or 0 when no calls have
// been observed.
func (w *slowCallWindow) fraction() float64 {
	if w.filled == 0 {
		return 0
	}

	return float64(w.slow) / float64(w.filled)
}

// tripped reports whether the slow fraction has reached threshold, gated by
// minCalls so a tiny, unrepresentative sample cannot trip the breaker. A
// minCalls below 1 is floored to 1.
func (w *slowCallWindow) tripped(minCalls int, threshold float64) bool {
	gate := minCalls
	if gate < 1 {
		gate = 1
	}

	if w.filled < gate {
		return false
	}

	return w.fraction() >= threshold
}

// SlowCallFraction returns the current fraction of slow calls in the breaker's
// window, in [0, 1]. It is 0 when slow-call detection is disabled (see
// [SlowCallRate]) or no calls have been observed yet. Useful as a gauge to
// watch brownout pressure build before the breaker trips.
func (cb *CircuitBreaker) SlowCallFraction() float64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if !cb.slowCallEnabled() {
		return 0
	}

	return cb.slowWin.fraction()
}

// RampRecoveryFraction returns the fraction of traffic the breaker is currently
// admitting while ramping back to full load after recovery, in [0, 1]. It is 0
// when the breaker is not in the [CircuitRamping] state — closed, open or
// half-open — or ramp recovery is disabled (see [RampRecovery]). Note that 0 is
// also the live value at the very start of a ramp configured with
// RampInitialFraction(0); use [CircuitBreaker.State] == [CircuitRamping] to tell
// "ramping at 0%" from "not ramping". Useful as a gauge to watch a downstream
// ease back to full traffic.
func (cb *CircuitBreaker) RampRecoveryFraction() float64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state != stateRamping {
		return 0
	}

	return cb.rampFractionAt(cb.clock.Since(cb.rampStart))
}

// releaseProbe decrements the in-flight half-open probe counter, flooring at
// zero so RecordSuccess/RecordFailure calls without a matching Allow (or more
// results than admitted probes) cannot drive it negative. Caller must hold mu.
func (cb *CircuitBreaker) releaseProbe() {
	if cb.halfOpenInFlight > 0 {
		cb.halfOpenInFlight--
	}
}

// State returns the current state: [CircuitClosed], [CircuitOpen],
// [CircuitHalfOpen], or [CircuitRamping].
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateClosed:
		return CircuitClosed
	case stateOpen:
		return CircuitOpen
	case stateHalfOpen:
		return CircuitHalfOpen
	case stateRamping:
		return CircuitRamping
	default:
		// An unrecognised internal state fails safe to open (not serving),
		// matching circuitCondition's fail-direction — a future state added
		// without updating this switch can never be reported as healthy.
		return CircuitOpen
	}
}
