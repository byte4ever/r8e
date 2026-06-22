package r8e

import "sync"

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------.

type (
	// concurrencyBudgetConfig holds the parameters for a ConcurrencyBudget
	// before they are validated and stored on the budget in
	// NewConcurrencyBudget.
	concurrencyBudgetConfig struct {
		maxRatio       float64
		minConcurrency int
	}

	// ConcurrencyBudgetOption configures a ConcurrencyBudget.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewConcurrencyBudget's signature stable.
	ConcurrencyBudgetOption func(*concurrencyBudgetConfig)

	// ConcurrencyBudget bounds the number of CONCURRENT retries and hedges
	// in flight at once, so a burst of simultaneous retries cannot pile load
	// onto a struggling downstream (a "retry storm" in the concurrency
	// dimension). It is the complement of [RetryBudget]: that throttles the
	// RATE of retries over time (a token bucket), this caps their instantaneous
	// PARALLELISM. The two compose — a policy may use both.
	//
	// A retry or hedge is permitted only while
	//
	//	concurrent < max(MinConcurrency, MaxRatio × in-flight executions)
	//
	// where "in-flight executions" is the number of calls currently inside the
	// retry/hedge region of the chain and "concurrent" is the number of those
	// that are presently executing a retry attempt or a hedge. The MaxRatio term
	// scales the cap with live traffic (so a busy service tolerates more
	// concurrent retries than an idle one) and the MinConcurrency floor keeps a
	// low-traffic service from being unable to retry at all. This mirrors
	// failsafe-go's execution budget and resilience4j's bulkhead-on-retries.
	//
	// The first attempt of every call is the baseline and is never gated; only
	// retries (second and later attempts) and the second concurrent hedge
	// attempt claim a permit. When the budget is exhausted a retry is suppressed
	// and the call fails with [ErrConcurrencyBudgetExceeded] (wrapping the last
	// downstream error); an over-budget hedge is simply not launched (the primary
	// still runs). Both outcomes fire [Hooks.OnConcurrencyBudgetExceeded] and
	// increment the matching metric.
	//
	// Construct a ConcurrencyBudget with [NewConcurrencyBudget]; the zero value
	// is not usable (its MaxRatio of 0 would reject every retry). A single budget
	// may be shared across several policies to bound retries process-wide (see
	// [WithSharedConcurrencyBudget]); it is safe for concurrent use. All counters
	// and tunables move together under one mutex so a concurrent [Reconfigure]
	// cannot tear an admission decision.
	//
	// Pattern: Concurrency Budget — caps the share of in-flight executions that
	// may be retries/hedges (ratio with a floor); mutex-guarded so the
	// (executions, in-use, MaxRatio, MinConcurrency) tuple stays coherent.
	ConcurrencyBudget struct {
		mu sync.Mutex
		// executions is the number of calls currently in the retry/hedge region
		// (the denominator). inUse is how many of them hold a retry/hedge permit
		// (the numerator).
		executions     int
		inUse          int
		maxRatio       float64
		minConcurrency int
	}
)

// Default budget parameters, matching failsafe-go's execution budget defaults
// (up to 25% of in-flight executions may be retries/hedges, at least 5).
const (
	defaultConcurrencyMaxRatio       = 0.25
	defaultConcurrencyMinConcurrency = 5
)

// MaxRatio sets the maximum fraction of in-flight executions that may be retries
// or hedges at once. A value at or below 0 is clamped to the default and a value
// above 1 is clamped to 1. Default: 0.25.
func MaxRatio(r float64) ConcurrencyBudgetOption {
	return func(cfg *concurrencyBudgetConfig) {
		cfg.maxRatio = r
	}
}

// MinConcurrency sets the floor on concurrent retries/hedges the budget always
// permits, regardless of the [MaxRatio] fraction — without it a low-traffic
// service could be unable to retry at all. A negative value is clamped to the
// default; 0 disables the floor (pure ratio). Default: 5.
func MinConcurrency(n int) ConcurrencyBudgetOption {
	return func(cfg *concurrencyBudgetConfig) {
		cfg.minConcurrency = n
	}
}

// NewConcurrencyBudget creates a budget that bounds concurrent retries and
// hedges. Invalid parameters are clamped to the defaults rather than panicking,
// matching [NewRetryBudget] and [NewRateLimiter].
func NewConcurrencyBudget(opts ...ConcurrencyBudgetOption) *ConcurrencyBudget {
	cfg := concurrencyBudgetConfig{
		maxRatio:       defaultConcurrencyMaxRatio,
		minConcurrency: defaultConcurrencyMinConcurrency,
	}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	return &ConcurrencyBudget{
		maxRatio:       cfg.maxRatio,
		minConcurrency: cfg.minConcurrency,
	}
}

// clamp replaces out-of-range parameters with valid values so the admission
// arithmetic always has a positive rate and a non-negative floor.
func (c *concurrencyBudgetConfig) clamp() {
	if c.maxRatio <= 0 {
		c.maxRatio = defaultConcurrencyMaxRatio
	}

	if c.maxRatio > 1 {
		c.maxRatio = 1
	}

	if c.minConcurrency < 0 {
		c.minConcurrency = defaultConcurrencyMinConcurrency
	}
}

// Reconfigure retunes the budget at runtime. Options are applied on top of the
// current parameters, so a partial update leaves the unspecified one unchanged
// (matching [RetryBudget.Reconfigure]). The live execution and in-use counts are
// untouched — only the tunables change — so resizing takes effect on the next
// admission decision without disturbing calls already in flight. Safe for
// concurrent use with the retry/hedge path.
func (b *ConcurrencyBudget) Reconfigure(opts ...ConcurrencyBudgetOption) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Seed from the current values so unspecified options are preserved.
	cfg := concurrencyBudgetConfig{
		maxRatio:       b.maxRatio,
		minConcurrency: b.minConcurrency,
	}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	b.maxRatio = cfg.maxRatio
	b.minConcurrency = cfg.minConcurrency
}

// limitLocked returns the current ceiling on concurrent retries/hedges. The
// caller must hold b.mu.
//
// The int() conversion truncates toward zero deliberately: it is the admission
// semantics, not a rounding shortcut. A small ratio term (e.g. maxRatio 0.25 ×
// 1 execution = 0.25) floors to 0 so the MinConcurrency term governs at low
// traffic; do not "fix" it to round.
func (b *ConcurrencyBudget) limitLocked() int {
	return max(b.minConcurrency, int(b.maxRatio*float64(b.executions)))
}

// enter records that a call has entered the retry/hedge region, growing the
// denominator. It is a no-op on a nil budget so the chain can call it
// unconditionally. Pair every enter with an exit.
func (b *ConcurrencyBudget) enter() {
	if b == nil {
		return
	}

	b.mu.Lock()
	b.executions++
	b.mu.Unlock()
}

// exit records that a call has left the retry/hedge region. It is a no-op on a
// nil budget and floored at zero so an unmatched exit cannot drive the
// denominator negative.
func (b *ConcurrencyBudget) exit() {
	if b == nil {
		return
	}

	b.mu.Lock()

	if b.executions > 0 {
		b.executions--
	}

	b.mu.Unlock()
}

// tryAcquire claims a retry/hedge permit if the budget has room, reporting
// whether it was granted. A granted permit must be returned with release. A nil
// budget always grants, so the retry/hedge path can call it unconditionally.
func (b *ConcurrencyBudget) tryAcquire() bool {
	if b == nil {
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.inUse >= b.limitLocked() {
		return false
	}

	b.inUse++

	return true
}

// release returns a permit claimed by tryAcquire. It is a no-op on a nil budget
// and floored at zero so an unmatched release cannot drive the count negative.
func (b *ConcurrencyBudget) release() {
	if b == nil {
		return
	}

	b.mu.Lock()

	if b.inUse > 0 {
		b.inUse--
	}

	b.mu.Unlock()
}

// InUse returns the number of retries and hedges currently holding a permit, as
// a point-in-time snapshot. A nil budget reports 0. Surfaced by [Policy.Metrics]
// as a gauge; when a budget is shared across policies every sharing policy
// reports the same value.
func (b *ConcurrencyBudget) InUse() int {
	if b == nil {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.inUse
}

// Exhausted reports whether the budget is currently at its ceiling and would
// reject the next retry/hedge. A nil budget is never exhausted. Surfaced as a
// degraded health condition.
func (b *ConcurrencyBudget) Exhausted() bool {
	if b == nil {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.inUse >= b.limitLocked()
}
