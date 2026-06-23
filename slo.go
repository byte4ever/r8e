package r8e

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// SLOGovernor — SLO error-budget burn-rate load shedding
// ---------------------------------------------------------------------------.

type (
	// sloConfig holds the parameters for an SLOGovernor before clamp validates
	// them and applyConfig stores them on the governor — the form options are
	// applied to, in both NewSLOGovernor and Reconfigure.
	sloConfig struct {
		classifier    func(error) bool
		target        float64
		shortWindow   time.Duration
		longWindow    time.Duration
		burnThreshold float64
		maxShedRate   float64
		minRequests   int
	}

	// SLOOption configures an SLOGovernor / WithSLO.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewSLOGovernor's signature stable.
	SLOOption func(*sloConfig)

	// sloBucket is one time slice of a sliding window: the calls served and the
	// subset that burned error budget during the epoch it is stamped with. A
	// bucket whose epoch has aged out of the window is reused (reset in place)
	// rather than reallocated.
	sloBucket struct {
		epoch    int64
		total    int64
		failures int64
	}

	// sloWindow is a sliding window of served outcomes: a ring of epoch-stamped
	// buckets summing the total calls and the budget-burning failures over a
	// span. The governor keeps two — a short (fast) and a long (slow) window —
	// so it can require both to agree before shedding (see [SLOGovernor]). Its
	// methods take the now timestamp and must be called with the governor mutex
	// held; the window holds no lock of its own.
	sloWindow struct {
		bucketNanos int64
		buckets     [sloBuckets]sloBucket
	}

	// SLOGovernor is a client-side load shedder driven by an SLO error budget:
	// it sheds calls locally, in proportion to how fast the service-level
	// objective's error budget is burning, so a sustained failure surge spends
	// the budget on the calls that matter most instead of on sheddable work.
	//
	// An SLO target (e.g. 0.999) implies an error budget 1-target (0.001 — the
	// fraction of calls allowed to fail). The burn rate is the observed served
	// error rate divided by that budget: 1 means the budget is being spent
	// exactly at the sustainable pace, 14.4 is the Google-SRE "fast burn" page
	// threshold (2% of a 30-day budget in 1h). The governor measures the burn
	// rate over two sliding windows — a short, responsive one and a long,
	// steady one — and sheds only when BOTH exceed [BurnThreshold]. This
	// multiwindow rule (Google SRE "Alerting on SLOs") engages quickly on a real
	// burn yet ignores a brief spike that shows in only the short window.
	//
	// When engaged, a new call is shed with probability
	// max(0, 1 - BurnThreshold/burnRate) capped at [MaxShedRate], scaled by the
	// short window's burn rate so shedding tracks the current intensity. The
	// probability is applied through the call's [Sheddability]: a
	// [SheddabilityNever] call is always admitted, a [SheddabilityAlways] call
	// is shed as soon as any shedding is active, and a [SheddabilityDefault]
	// call is shed with the probability. A locally shed call is never recorded,
	// so shedding sheddable traffic does not itself burn budget — it preserves
	// budget for the critical traffic that keeps flowing.
	//
	// Unlike the [Throttler], which sheds by the live backend accept/request
	// ratio to protect a struggling backend, the governor sheds by the error
	// budget of a stated objective: the two are complementary and can run
	// together. Unlike a [CircuitBreaker]'s binary trip, it dampens load
	// proportionally and keeps probing.
	//
	// The windows are measured against the injected [Clock], so behaviour is
	// deterministic under a fake clock in tests. Construct one with
	// NewSLOGovernor; it is safe for concurrent use. The two windows move
	// together under one mutex — reading the burn rate, drawing against the shed
	// probability, and recording an outcome must stay mutually consistent, which
	// a lock provides and independent atomics do not.
	//
	// Pattern: SLO Burn-Rate Governor — multiwindow error-budget load shedding
	// sheds traffic proportionally to how fast the SLO error budget is burning.
	SLOGovernor struct {
		clock      Clock
		hooks      *Hooks
		classifier func(error) bool
		// sampler draws the [0, 1) value compared against the shed probability.
		// It is rand.Float64 in production and is overridden only by white-box
		// tests, which must set it before launching any concurrent Allow.
		sampler       func() float64
		shortWindowD  time.Duration
		longWindowD   time.Duration
		minRequests   int64
		target        float64
		burnThreshold float64
		maxShedRate   float64
		short         sloWindow
		long          sloWindow
		mu            sync.Mutex
	}
)

const (
	// sloBuckets is the number of time slices each sliding window is divided
	// into; a finer ring decays old counts more smoothly. Internal, not exposed.
	sloBuckets = 10

	// Default SLOGovernor parameters. The long window catches a sustained burn;
	// the short window (≈ long/12, the Google SRE 1h:5m window ratio) confirms
	// the burn is still happening now, so shedding disengages promptly when it
	// stops. burnThreshold 2.0 starts shedding only once the budget is burning
	// at least twice as fast as sustainable; maxShedRate caps shedding below 1
	// so the service keeps being probed; minRequests gates out small-sample
	// noise in the short window. The sub-minute windows are shorter than the SRE
	// book's hours for faster client-side reaction; widen them with
	// SLOLongWindow / SLOShortWindow for steadier behaviour.
	defaultSLOTarget      = 0.99
	defaultSLOLongWindow  = time.Minute
	defaultSLOShortWindow = 5 * time.Second
	defaultBurnThreshold  = 2.0
	defaultMaxShedRate    = 0.9
	defaultSLOMinRequests = 20

	// sloShortLongRatio reshapes a short window that is not strictly inside the
	// long one (a misconfiguration): the short window is reset to long/ratio so
	// the multiwindow rule keeps meaning two distinct spans.
	sloShortLongRatio = 12
)

// SLOLongWindow sets the long, steady sliding window over which the burn rate is
// summed — the window that catches a sustained budget burn. A longer window
// reacts more slowly but is steadier. A non-positive value resets to the
// default. Default: 1m.
func SLOLongWindow(window time.Duration) SLOOption {
	return func(cfg *sloConfig) {
		cfg.longWindow = window
	}
}

// SLOShortWindow sets the short, responsive sliding window. The governor sheds
// only when both the short and the long window exceed [BurnThreshold], so the
// short window confirms a burn is current and lets shedding disengage promptly
// when it stops. It must be strictly shorter than the long window; a
// non-positive value, or one not shorter than the long window, resets it to a
// fraction of the long window. Default: 5s.
func SLOShortWindow(window time.Duration) SLOOption {
	return func(cfg *sloConfig) {
		cfg.shortWindow = window
	}
}

// BurnThreshold sets the error-budget burn rate at or below which the governor
// never sheds. Above it, shedding ramps in proportion to how far the burn rate
// exceeds the threshold. A burn rate of 1 spends the budget at the sustainable
// pace, so a threshold of 2.0 starts protecting the budget once it is burning
// twice as fast as sustainable. Values at or below 0 reset to the default.
// Default: 2.0.
func BurnThreshold(rate float64) SLOOption {
	return func(cfg *sloConfig) {
		cfg.burnThreshold = rate
	}
}

// MaxShedRate caps the probability of local shedding, so at least 1-maxShedRate
// of [SheddabilityDefault] traffic always reaches the service to keep probing
// for recovery. Must be in (0, 1]; an out-of-range value resets to the default.
// Default: 0.9.
func MaxShedRate(rate float64) SLOOption {
	return func(cfg *sloConfig) {
		cfg.maxShedRate = rate
	}
}

// SLOMinRequests sets the minimum number of served calls that must fall within
// the short window before any call is shed, so a handful of early failures
// cannot trigger shedding. A value below 1 resets to the default. Default: 20.
func SLOMinRequests(n int) SLOOption {
	return func(cfg *sloConfig) {
		cfg.minRequests = n
	}
}

// SLOClassifier overrides which downstream errors burn error budget. By default
// every non-nil error counts as a budget-burning failure; with a classifier
// only errors for which it returns true do, so an error it rejects — a 404, a
// validation failure — is treated as a success of the objective (the service
// served the request correctly, the result was just not a 2xx). A nil
// classifier restores the default.
func SLOClassifier(fn func(error) bool) SLOOption {
	return func(cfg *sloConfig) {
		cfg.classifier = fn
	}
}

// NewSLOGovernor creates an SLO error-budget burn-rate load shedder for the
// given target success rate (e.g. 0.999). Invalid parameters — a target outside
// (0, 1) included — are clamped to defaults rather than panicking, matching
// NewThrottler's tolerant construction. The clock drives the sliding windows;
// pass a non-nil Clock (the policy passes its own) and a non-nil *Hooks (the
// zero value Hooks is fine).
func NewSLOGovernor(
	target float64,
	clock Clock,
	hooks *Hooks,
	opts ...SLOOption,
) *SLOGovernor {
	cfg := sloConfig{
		target:        target,
		longWindow:    defaultSLOLongWindow,
		shortWindow:   defaultSLOShortWindow,
		burnThreshold: defaultBurnThreshold,
		maxShedRate:   defaultMaxShedRate,
		minRequests:   defaultSLOMinRequests,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	cfg.clamp()

	gov := &SLOGovernor{clock: clock, hooks: hooks, sampler: rand.Float64}
	gov.applyConfig(cfg)

	return gov
}

// applyConfig copies a validated config onto the governor's live parameters and
// resizes each window, clearing a window's history when its bucket size changes
// (counts recorded under the old slice length cannot be reinterpreted under the
// new one). It is the single place the parameters are written, so NewSLOGovernor
// and Reconfigure cannot drift as parameters are added. Call with gov.mu held,
// or before the governor is published (as NewSLOGovernor does).
func (g *SLOGovernor) applyConfig(cfg sloConfig) {
	resizeSLOWindow(&g.short, cfg.shortWindow)
	resizeSLOWindow(&g.long, cfg.longWindow)

	g.classifier = cfg.classifier
	g.target = cfg.target
	g.shortWindowD = cfg.shortWindow
	g.longWindowD = cfg.longWindow
	g.burnThreshold = cfg.burnThreshold
	g.maxShedRate = cfg.maxShedRate
	g.minRequests = int64(cfg.minRequests)
}

// currentConfig snapshots the governor's live tunable parameters as a sloConfig
// — the read-back mirror of applyConfig. Reconfigure seeds from it so a partial
// option update overlays the current values; pairing the two keeps a new
// parameter to one read site and one write site. Call with gov.mu held.
func (g *SLOGovernor) currentConfig() sloConfig {
	return sloConfig{
		classifier:    g.classifier,
		target:        g.target,
		shortWindow:   g.shortWindowD,
		longWindow:    g.longWindowD,
		burnThreshold: g.burnThreshold,
		maxShedRate:   g.maxShedRate,
		minRequests:   int(g.minRequests),
	}
}

// resizeSLOWindow recomputes a window's bucket span for the given window length
// and, when it changes, clears the ring (counts under the old slice size cannot
// be reinterpreted under the new one). Call with gov.mu held.
func resizeSLOWindow(w *sloWindow, window time.Duration) {
	nb := sloBucketNanos(window)
	if nb != w.bucketNanos {
		*w = sloWindow{bucketNanos: nb}
	}
}

// sloBucketNanos returns the nanosecond span of one window bucket, flooring at
// 1ns so the epoch division is always safe.
func sloBucketNanos(window time.Duration) int64 {
	nanos := int64(window) / sloBuckets
	if nanos < 1 {
		return 1
	}

	return nanos
}

// clamp repairs out-of-range parameters so the burn-rate arithmetic always has
// a valid target (and therefore a positive error budget), two distinct windows,
// a positive threshold, a probing-preserving shed cap, and a non-trivial
// minimum sample.
func (c *sloConfig) clamp() {
	if c.target <= 0 || c.target >= 1 {
		c.target = defaultSLOTarget
	}

	if c.longWindow <= 0 {
		c.longWindow = defaultSLOLongWindow
	}

	// The short window must sit strictly inside the long one for the multiwindow
	// rule to compare two distinct spans.
	if c.shortWindow <= 0 || c.shortWindow >= c.longWindow {
		c.shortWindow = c.longWindow / sloShortLongRatio
		if c.shortWindow <= 0 {
			c.shortWindow = 1
		}
	}

	if c.burnThreshold <= 0 {
		c.burnThreshold = defaultBurnThreshold
	}

	if c.maxShedRate <= 0 || c.maxShedRate > 1 {
		c.maxShedRate = defaultMaxShedRate
	}

	if c.minRequests < 1 {
		c.minRequests = defaultSLOMinRequests
	}
}

// Allow decides whether to admit a call, applying the [Sheddability] stamped on
// ctx by [WithSheddability] to the current shed probability (see [SLOGovernor]
// for the per-level behaviour). On a local shed it emits OnSLOShed (outside the
// lock, so no user callback runs inside the critical section) and returns
// [ErrSLOShed], otherwise nil. Pair each admitted call with an
// [SLOGovernor.Record] so the outcome feeds the windows; a shed call must NOT be
// recorded.
func (g *SLOGovernor) Allow(ctx context.Context) error {
	if g.admit(SheddabilityFromCtx(ctx)) {
		return nil
	}

	g.hooks.emitSLOShed()

	return ErrSLOShed
}

// admit reports whether a call is forwarded (true) or shed (false) under the
// current shed probability and the call's Sheddability. It holds the lock only
// for the decision (via defer, so a panic in the sampler cannot strand the
// mutex); the OnSLOShed hook is emitted by Allow after the lock is released, so
// no user callback runs under it. Unlike the throttler, admit counts nothing —
// only served outcomes are recorded (by Record), so a locally shed call never
// burns budget.
func (g *SLOGovernor) admit(shed Sheddability) bool {
	now := g.clock.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	prob := g.shedProbabilityLocked(now)

	switch shed {
	case SheddabilityNever:
		// Critical calls bypass shedding entirely.
		return true
	case SheddabilityAlways:
		// Sheddable calls are dropped as soon as any shedding is active.
		return prob <= 0
	default:
		// Default: forward unless a positive probability wins the draw (stated
		// positively so the admit/shed polarity reads without a double negative).
		return prob <= 0 || g.sampler() >= prob
	}
}

// Record folds a served call's outcome into both windows: every recorded call
// increments the window total, and a budget-burning error increments failures.
// A locally shed call (Allow returned [ErrSLOShed]) was never served and must
// not be recorded.
func (g *SLOGovernor) Record(err error) {
	failed := g.isBurn(err)
	now := g.clock.Now()

	var failures int64
	if failed {
		failures = 1
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.short.observe(now, failures)
	g.long.observe(now, failures)
}

// isBurn reports whether a downstream outcome burns error budget. A nil error
// never does; a non-nil error does unless a configured classifier says
// otherwise. The classifier is snapshotted under the lock so a concurrent
// Reconfigure cannot race the read, then invoked outside the lock so user code
// never runs under the mutex.
func (g *SLOGovernor) isBurn(err error) bool {
	if err == nil {
		return false
	}

	g.mu.Lock()
	classifier := g.classifier
	g.mu.Unlock()

	if classifier != nil {
		return classifier(err)
	}

	return true
}

// shedProbabilityLocked computes the probability a new call is shed: zero unless
// the short window has at least minRequests samples AND both the short and the
// long window's burn rate exceed burnThreshold (the multiwindow rule), in which
// case it is shedFraction of the short window's burn rate. Must be called with
// gov.mu held.
func (g *SLOGovernor) shedProbabilityLocked(now time.Time) float64 {
	shortBurn, shortSamples := g.burnRateLocked(&g.short, now)
	// The short window has the fewest samples, so gating it gates both windows.
	if shortSamples < g.minRequests {
		return 0
	}

	longBurn, _ := g.burnRateLocked(&g.long, now)
	if shortBurn < g.burnThreshold || longBurn < g.burnThreshold {
		return 0
	}

	return shedFraction(shortBurn, g.burnThreshold, g.maxShedRate)
}

// burnRateLocked returns window w's error-budget burn rate and its sample count:
// the served error rate (failures/total) divided by the error budget
// (1-target). A window with no samples burns at rate 0. Must be called with
// gov.mu held.
func (g *SLOGovernor) burnRateLocked(
	w *sloWindow,
	now time.Time,
) (rate float64, samples int64) {
	total, failures := w.sums(now)
	if total == 0 {
		return 0, 0
	}

	errorRate := float64(failures) / float64(total)
	errorBudget := 1 - g.target // target ∈ (0, 1) ⇒ errorBudget ∈ (0, 1)

	return errorRate / errorBudget, total
}

// shedFraction returns the proportional shed probability for a burn rate above
// the threshold, capped at maxShed: 0 at or below the threshold, rising toward
// maxShed as the burn rate climbs. Pure so it can be fuzzed; the result is
// always within [0, maxShed].
func shedFraction(burnRate, threshold, maxShed float64) float64 {
	if threshold <= 0 || burnRate <= threshold {
		return 0
	}

	prob := 1 - threshold/burnRate

	return max(0, min(prob, maxShed))
}

// observe folds one served outcome into the window: total++ always, plus
// failures (0 for a success, 1 for a budget-burning error) added to the bucket's
// failure count. The bucket for now's epoch is reset first when it still holds a
// stale epoch's counts (the ring slot is being reused). Must be called with
// gov.mu held.
func (w *sloWindow) observe(now time.Time, failures int64) {
	bucket := w.bucketFor(now)
	bucket.total++
	bucket.failures += failures
}

// sums totals the served calls and budget-burning failures across every bucket
// whose epoch lies within the window ending at now. Buckets older than the
// window (or never written) are skipped, so their counts decay out without
// explicit eviction. Must be called with gov.mu held.
func (w *sloWindow) sums(now time.Time) (total, failures int64) {
	current := w.epoch(now)
	oldest := current - sloBuckets + 1

	for i := range w.buckets {
		bucket := &w.buckets[i]
		// Skip buckets outside the window: older than its oldest epoch, or — if
		// the injected clock ever steps backward — stamped with a future epoch.
		if bucket.epoch < oldest || bucket.epoch > current {
			continue
		}

		total += bucket.total
		failures += bucket.failures
	}

	return total, failures
}

// bucketFor returns the bucket for now's epoch, resetting it first when it still
// holds a stale epoch's counts (the ring slot is being reused for a new slice).
// Must be called with gov.mu held.
func (w *sloWindow) bucketFor(now time.Time) *sloBucket {
	current := w.epoch(now)

	idx := current % sloBuckets
	if idx < 0 {
		idx += sloBuckets
	}

	bucket := &w.buckets[idx]
	if bucket.epoch != current {
		*bucket = sloBucket{epoch: current}
	}

	return bucket
}

// epoch maps a timestamp to the monotonic index of the bucket-sized time slice
// it falls in — the counter the sliding window is expressed in.
func (w *sloWindow) epoch(now time.Time) int64 {
	return now.UnixNano() / w.bucketNanos
}

// BurnRate returns the current error-budget burn rate over the long window, as a
// point-in-time snapshot. Surfaced by Policy.Metrics as a gauge: 1 means the
// budget is being spent at the sustainable pace, above 1 faster; 0 when no call
// has been recorded in the window. It is the steady long-window figure, not the
// (more reactive) short window the shed probability scales with.
func (g *SLOGovernor) BurnRate() float64 {
	now := g.clock.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	rate, _ := g.burnRateLocked(&g.long, now)

	return rate
}

// ShedProbability returns the current probability that a [SheddabilityDefault]
// call would be shed locally, as a point-in-time snapshot. Surfaced by
// Policy.Metrics as a gauge; zero whenever the governor is admitting all
// traffic (the multiwindow burn rule is not met).
func (g *SLOGovernor) ShedProbability() float64 {
	now := g.clock.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	return g.shedProbabilityLocked(now)
}

// Shedding reports whether the governor is currently shedding any load (its shed
// probability is above zero). Surfaced as a degraded health condition.
func (g *SLOGovernor) Shedding() bool {
	return g.ShedProbability() > 0
}

// Reconfigure retunes the governor at runtime. The target is replaced
// positionally (like [RateLimiter.Reconfigure]'s rate); options are applied on
// top of the current parameters, so a partial option update leaves the others
// unchanged. Changing a window resets that window's history, since counts
// recorded under the old bucket size cannot be reinterpreted under the new one;
// the window refills within one window span. Safe for concurrent use with the
// admission path.
func (g *SLOGovernor) Reconfigure(target float64, opts ...SLOOption) {
	g.mu.Lock()
	defer g.mu.Unlock()

	cfg := g.currentConfig()
	cfg.target = target

	for _, opt := range opts {
		opt(&cfg)
	}

	cfg.clamp()
	g.applyConfig(cfg)
}
