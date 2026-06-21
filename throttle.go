package r8e

import (
	"math/rand/v2"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Throttler — Google-SRE client-side adaptive throttling
// ---------------------------------------------------------------------------.

type (
	// throttleConfig holds the parameters for a Throttler before clamp validates
	// them and applyConfig stores them on the throttler — the form options are
	// applied to, in both NewThrottler and Reconfigure.
	throttleConfig struct {
		classifier       func(error) bool
		window           time.Duration
		overloadRatio    float64
		maxRejectionRate float64
		minRequests      int
	}

	// ThrottleOption configures a Throttler / WithAdaptiveThrottle.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewThrottler's signature stable.
	ThrottleOption func(*throttleConfig)

	// throttleBucket is one time slice of the sliding window: the requests
	// attempted and the requests the backend accepted during the epoch it is
	// stamped with. A bucket whose epoch has aged out of the window is reused
	// (reset in place) rather than reallocated.
	throttleBucket struct {
		epoch    int64
		requests int64
		accepts  int64
	}

	// Throttler is a Google-SRE client-side adaptive throttler: a probabilistic
	// load shedder that rejects calls locally, before they reach a struggling
	// backend, in proportion to how heavily that backend is already rejecting.
	//
	// It keeps a sliding window of two counts — requests attempted and requests
	// the backend accepted — and, once requests exceed [OverloadRatio] times
	// accepts, sheds a new call with probability max(0, (requests - K*accepts) /
	// (requests + 1)) (the SRE "Handling Overload" formula). A locally shed call
	// still counts as a request but never as an accept, so sustained rejection
	// widens the gap and raises the probability. The probability is capped at
	// [MaxRejectionRate] (default 0.9) so a fraction of traffic always probes the
	// backend and recovery is detected promptly; it stays zero until
	// [MinRequests] have accumulated so a small sample cannot trigger shedding.
	//
	// Unlike a [CircuitBreaker], which trips fully open on a binary threshold, the
	// throttler dampens load gradually and proportionally — ideally easing a
	// recovering backend back to health before the breaker ever needs to trip.
	// It is distinct again from the [AdaptiveLimiter], which caps in-flight
	// concurrency rather than shedding by failure rate.
	//
	// The window is measured against the injected [Clock], so behaviour is
	// deterministic under a fake clock in tests. Construct one with NewThrottler;
	// it is safe for concurrent use. The two window counts move together under one
	// mutex — reading them, drawing against the probability, and recording the
	// outcome must stay mutually consistent, which a lock provides and independent
	// atomics do not.
	//
	// Pattern: Adaptive Throttler — Google-SRE client-side load shedding sheds
	// requests probabilistically by the live accept/request ratio, complementing
	// the circuit breaker's binary trip.
	Throttler struct {
		clock      Clock
		hooks      *Hooks
		classifier func(error) bool
		// sampler draws the [0, 1) value compared against the shed probability.
		// It is rand.Float64 in production and is overridden only by white-box
		// tests, which must set it before launching any concurrent Allow.
		sampler          func() float64
		window           time.Duration
		bucketNanos      int64
		minRequests      int64
		overloadRatio    float64
		maxRejectionRate float64
		buckets          [throttleBuckets]throttleBucket
		mu               sync.Mutex
	}
)

const (
	// throttleBuckets is the number of time slices the sliding window is divided
	// into; a finer ring decays old counts more smoothly. Internal, not exposed.
	throttleBuckets = 10

	// Default throttler parameters. overloadRatio K=2 is the Google SRE default
	// (a 2x request/accept gap is tolerated before any shedding begins);
	// maxRejectionRate caps shedding below 1 so the backend keeps being probed;
	// minRequests gates out small-sample noise; window is the span requests and
	// accepts are summed over. The 10s window is shorter than the SRE book's
	// ~2min and failsafe-go's 1min for faster pre-breaker reaction; widen it with
	// ThrottleWindow for low-throughput or steadier behaviour.
	defaultOverloadRatio    = 2.0
	defaultMaxRejectionRate = 0.9
	defaultMinRequests      = 10
	defaultThrottleWindow   = 10 * time.Second

	// minOverloadRatio floors K at 1: below it the client would shed even when
	// every forwarded request is accepted, which is never intended.
	minOverloadRatio = 1.0
)

// OverloadRatio sets K, the multiplier in the SRE throttling formula: the client
// begins shedding once requests exceed K times accepts. Higher is more
// permissive — 2.0 tolerates a 2x request/accept gap before any shedding.
// Values below 1.0 are clamped to the default. Default: 2.0.
func OverloadRatio(ratio float64) ThrottleOption {
	return func(cfg *throttleConfig) {
		cfg.overloadRatio = ratio
	}
}

// MaxRejectionRate caps the probability of local rejection, so at least
// 1-maxRejectionRate of traffic is always let through to keep probing the
// backend for recovery. Must be in (0, 1]; an out-of-range value resets to the
// default. Default: 0.9.
func MaxRejectionRate(rate float64) ThrottleOption {
	return func(cfg *throttleConfig) {
		cfg.maxRejectionRate = rate
	}
}

// ThrottleWindow sets the length of the sliding window over which requests and
// accepts are counted. A longer window reacts more slowly but is steadier. A
// non-positive value resets to the default. Default: 10s.
func ThrottleWindow(window time.Duration) ThrottleOption {
	return func(cfg *throttleConfig) {
		cfg.window = window
	}
}

// MinRequests sets the minimum number of requests that must fall within the
// window before any call is shed, so a handful of early failures cannot trigger
// shedding. A value below 1 resets to the default. Default: 10.
func MinRequests(n int) ThrottleOption {
	return func(cfg *throttleConfig) {
		cfg.minRequests = n
	}
}

// ThrottleClassifier overrides which downstream errors count as a backend
// rejection (a non-accept). By default every non-nil error counts as a
// rejection; with a classifier only errors for which it returns true do, so an
// error it rejects — a 404, a validation failure — is treated as an accept (the
// backend served the request, it just did not succeed). A nil classifier
// restores the default.
func ThrottleClassifier(fn func(error) bool) ThrottleOption {
	return func(cfg *throttleConfig) {
		cfg.classifier = fn
	}
}

// NewThrottler creates a Google-SRE adaptive throttler. Invalid parameters are
// clamped to defaults rather than panicking, matching NewRateLimiter's tolerant
// construction. The clock drives the sliding window; pass a non-nil Clock (the
// policy passes its own) and a non-nil *Hooks (the zero value Hooks is fine).
func NewThrottler(
	clock Clock,
	hooks *Hooks,
	opts ...ThrottleOption,
) *Throttler {
	cfg := throttleConfig{
		overloadRatio:    defaultOverloadRatio,
		maxRejectionRate: defaultMaxRejectionRate,
		minRequests:      defaultMinRequests,
		window:           defaultThrottleWindow,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	cfg.clamp()

	th := &Throttler{clock: clock, hooks: hooks, sampler: rand.Float64}
	th.applyConfig(cfg)

	return th
}

// applyConfig copies a validated config onto the throttler's live parameters and
// recomputes the derived bucket size, clearing the sliding-window history when
// that size changes (counts recorded under the old slice length cannot be
// reinterpreted under the new one). It is the single place the parameters are
// written, so NewThrottler and Reconfigure cannot drift as parameters are added.
// Call with t.mu held, or before the throttler is published (as NewThrottler
// does).
func (t *Throttler) applyConfig(cfg throttleConfig) {
	newBucketNanos := bucketNanosFor(cfg.window)
	if newBucketNanos != t.bucketNanos {
		t.buckets = [throttleBuckets]throttleBucket{}
		t.bucketNanos = newBucketNanos
	}

	t.classifier = cfg.classifier
	t.window = cfg.window
	t.overloadRatio = cfg.overloadRatio
	t.maxRejectionRate = cfg.maxRejectionRate
	t.minRequests = int64(cfg.minRequests)
}

// currentConfig snapshots the throttler's live tunable parameters as a
// throttleConfig — the read-back mirror of applyConfig. Reconfigure seeds from it
// so a partial update overlays the current values; pairing the two keeps a new
// parameter to one read site and one write site rather than a scattered field
// list. Call with t.mu held.
func (t *Throttler) currentConfig() throttleConfig {
	return throttleConfig{
		classifier:       t.classifier,
		window:           t.window,
		overloadRatio:    t.overloadRatio,
		maxRejectionRate: t.maxRejectionRate,
		minRequests:      int(t.minRequests),
	}
}

// bucketNanosFor returns the nanosecond span of one sliding-window bucket for
// the given window, flooring at 1ns so the epoch division is always safe.
func bucketNanosFor(window time.Duration) int64 {
	nanos := int64(window) / throttleBuckets
	if nanos < 1 {
		return 1
	}

	return nanos
}

// clamp repairs out-of-range parameters so the throttling arithmetic always has
// a sane overload ratio, a probing-preserving rejection cap, a positive window,
// and a non-trivial minimum sample.
func (c *throttleConfig) clamp() {
	if c.overloadRatio < minOverloadRatio {
		c.overloadRatio = defaultOverloadRatio
	}

	if c.maxRejectionRate <= 0 || c.maxRejectionRate > 1 {
		c.maxRejectionRate = defaultMaxRejectionRate
	}

	if c.window <= 0 {
		c.window = defaultThrottleWindow
	}

	if c.minRequests < 1 {
		c.minRequests = defaultMinRequests
	}
}

// Allow decides whether to admit a call. It counts this attempt as a request and
// draws against the current rejection probability; on a local shed it emits
// OnThrottled (outside the lock, so a user callback never runs inside the
// critical section) and returns [ErrThrottled], otherwise nil. Pair each admitted
// call with a [Throttler.Record] so the outcome feeds the window.
func (t *Throttler) Allow() error {
	if t.admit() {
		return nil
	}

	t.hooks.emitThrottled()

	return ErrThrottled
}

// admit counts the calling request and reports whether it is forwarded (true) or
// shed (false). It holds the lock only for the decision (via defer, so a panic in
// the sampler cannot strand the mutex); the OnThrottled hook is emitted by Allow
// after the lock is released, so no user callback runs under it.
func (t *Throttler) admit() bool {
	now := t.clock.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	prob := t.rejectProbabilityLocked(now)
	t.bucketFor(now).requests++

	// Forward unless a positive probability wins the draw (stated positively so
	// the admit/shed polarity reads without a double negative).
	return prob <= 0 || t.sampler() >= prob
}

// Record folds a completed call's outcome into the window: an accepted result
// increments accepts (the request itself was already counted by [Throttler.Allow]);
// a backend rejection adds nothing more, widening the request/accept gap that
// drives the probability. A locally shed call (Allow returned [ErrThrottled]) was
// never forwarded and must not be recorded.
func (t *Throttler) Record(err error) {
	if t.isReject(err) {
		return
	}

	now := t.clock.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.bucketFor(now).accepts++
}

// isReject reports whether a downstream outcome counts as a backend rejection. A
// nil error is always an accept; a non-nil error is a rejection unless a
// configured classifier says otherwise. The classifier is snapshotted under the
// lock so a concurrent Reconfigure cannot race the read, then invoked outside the
// lock so user code never runs under the mutex.
func (t *Throttler) isReject(err error) bool {
	if err == nil {
		return false
	}

	t.mu.Lock()
	classifier := t.classifier
	t.mu.Unlock()

	if classifier != nil {
		return classifier(err)
	}

	return true
}

// rejectProbabilityLocked computes the SRE client-side throttling probability
// max(0, (requests - K*accepts)/(requests+1)) over the current window, capped at
// maxRejectionRate and forced to zero until minRequests have accumulated so a
// small sample cannot trigger shedding. Must be called with t.mu held.
func (t *Throttler) rejectProbabilityLocked(now time.Time) float64 {
	requests, accepts := t.windowSums(now)
	if requests < t.minRequests {
		return 0
	}

	gap := float64(requests) - t.overloadRatio*float64(accepts)
	if gap <= 0 {
		return 0
	}

	prob := gap / (float64(requests) + 1)

	return min(prob, t.maxRejectionRate)
}

// windowSums totals the requests and accepts across every bucket whose epoch
// lies within the sliding window ending at now. Buckets older than the window
// (or never written) are skipped, so their counts decay out without explicit
// eviction. Must be called with t.mu held.
func (t *Throttler) windowSums(now time.Time) (requests, accepts int64) {
	current := t.epoch(now)
	oldest := current - throttleBuckets + 1

	for i := range t.buckets {
		bucket := &t.buckets[i]
		// Skip buckets outside the window: older than its oldest epoch, or — if
		// the injected clock ever steps backward — stamped with a future epoch.
		if bucket.epoch < oldest || bucket.epoch > current {
			continue
		}

		requests += bucket.requests
		accepts += bucket.accepts
	}

	return requests, accepts
}

// bucketFor returns the bucket for now's epoch, resetting it first when it still
// holds a stale epoch's counts (the ring slot is being reused for a new slice).
// Must be called with t.mu held.
func (t *Throttler) bucketFor(now time.Time) *throttleBucket {
	current := t.epoch(now)

	idx := current % throttleBuckets
	if idx < 0 {
		idx += throttleBuckets
	}

	bucket := &t.buckets[idx]
	if bucket.epoch != current {
		*bucket = throttleBucket{epoch: current}
	}

	return bucket
}

// epoch maps a timestamp to the monotonic index of the bucket-sized time slice
// it falls in — the counter the sliding window is expressed in.
func (t *Throttler) epoch(now time.Time) int64 {
	return now.UnixNano() / t.bucketNanos
}

// RejectionProbability returns the current probability that a call would be shed
// locally, as a point-in-time snapshot. Surfaced by Policy.Metrics as a gauge;
// zero whenever the throttler is letting all traffic through.
func (t *Throttler) RejectionProbability() float64 {
	now := t.clock.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	return t.rejectProbabilityLocked(now)
}

// Throttling reports whether the throttler is currently shedding any load (its
// rejection probability is above zero). Surfaced as a degraded health condition.
func (t *Throttler) Throttling() bool {
	return t.RejectionProbability() > 0
}

// Reconfigure retunes the throttler at runtime. Options are applied on top of
// the current parameters, so a partial update leaves the others unchanged
// (matching CircuitBreaker.Reconfigure). Changing the window resets the
// sliding-window history, since counts recorded under the old slice size cannot
// be reinterpreted under the new one; the window refills within one window span.
// Safe for concurrent use with the admission path.
func (t *Throttler) Reconfigure(opts ...ThrottleOption) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg := t.currentConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	cfg.clamp()
	t.applyConfig(cfg)
}
