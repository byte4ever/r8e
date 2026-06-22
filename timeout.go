package r8e

import (
	"context"
	"sync/atomic"
	"time"
)

// Pattern: Timeout — wraps a call with a context deadline, returning
// ErrTimeout if the operation does not complete in time. Distinguishes
// between timeout-caused cancellation and parent context cancellation.

type (
	// TimeoutOption configures the timeout pattern built by [WithTimeout].
	//
	// Pattern: Functional Options — composable optional settings applied to a
	// private config, keeping WithTimeout's signature stable as adaptive behavior
	// is layered onto the fixed timeout.
	TimeoutOption func(*timeoutConfig)

	// timeoutConfig collects the optional [WithTimeout] settings before the policy
	// builds the timeout middleware. adaptive is non-nil once [AdaptiveTimeout] was
	// passed.
	timeoutConfig struct {
		adaptive *adaptiveTimeoutConfig
	}

	// AdaptiveTimeoutOption configures percentile-driven adaptive timeout (see
	// [AdaptiveTimeout]).
	AdaptiveTimeoutOption func(*adaptiveTimeoutConfig)

	// adaptiveTimeoutConfig holds the adaptive-timeout tunables. It is both the
	// option target (before resolve fills the defaults) and the resolved value
	// stored atomically on the live [adaptiveTimeout]; a reconfigure copies the
	// current resolved value, overlays the new options, and re-resolves.
	adaptiveTimeoutConfig struct {
		percentile float64
		multiplier float64
		floor      time.Duration
		minSamples int64
	}

	// adaptiveTimeout is the live percentile-driven timeout controller: a dedicated
	// sliding-window DDSketch of the bounded call's SUCCESSFUL latencies (the
	// embedded [windowedPercentile]) plus the hot-swappable tunables. Each call's
	// timeout is clamp(percentile-latency × multiplier, floor, ceiling), where
	// ceiling is the [WithTimeout] duration (a hard maximum the adaptive value never
	// exceeds). The percentile read is memoized per window epoch by the embedded
	// estimator, and both window and refresh cadence are driven by the injected
	// [Clock], so the adaptive timeout is deterministic under a fake clock in tests.
	// Safe for concurrent use.
	//
	// Pattern: Adaptive Timeout — a controller sizes the call deadline from live
	// latency feedback (its own success-latency percentile) instead of a fixed
	// duration, the latency→timeout analogue of the [AdaptiveLimiter]'s
	// latency→concurrency.
	adaptiveTimeout struct {
		*windowedPercentile
		cfg atomic.Pointer[adaptiveTimeoutConfig]
	}
)

const (
	// defaultAdaptiveTimeoutPercentile is the latency percentile the adaptive
	// timeout tracks by default — p99, the standard tail figure timeouts are sized
	// from.
	defaultAdaptiveTimeoutPercentile = 0.99

	// defaultAdaptiveTimeoutMultiplier is the headroom applied to the percentile by
	// default: timeout = p99 × 2, the common "percentile plus a 2× buffer" rule.
	defaultAdaptiveTimeoutMultiplier = 2.0

	// defaultAdaptiveTimeoutMinSamples is how many successful calls must be in the
	// window before the adaptive value is trusted; below it the policy falls back
	// to the configured ceiling.
	defaultAdaptiveTimeoutMinSamples = 20
)

// DoTimeout executes fn with a timeout. If fn does not complete within d,
// the context is cancelled and ErrTimeout is returned.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoTimeout[T any](
	ctx context.Context,
	timeout time.Duration,
	fn func(context.Context) (T, error),
	hooks *Hooks,
) (T, error) {
	var zero T

	// If the parent context is already done, return its error immediately.
	if ctx.Err() != nil {
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}

	// Create derived context with timeout.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Run fn in a goroutine and collect result via channel.
	type result struct {
		val T
		err error
	}

	ch := make(chan result, 1)

	go func() {
		v, err := fn(timeoutCtx)
		ch <- result{val: v, err: err}
	}()

	// Wait for fn to complete or context to expire.
	select {
	case r := <-ch:
		return r.val, r.err
	case <-timeoutCtx.Done():
		// Distinguish between timeout and parent cancellation.
		// If the parent context is done, the parent was cancelled externally.
		if ctx.Err() != nil {
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}
		// Otherwise, the derived context's deadline was exceeded.
		hooks.emitTimeout()

		return zero, ErrTimeout
	}
}

// AdaptiveTimeout enables percentile-driven adaptive timeout on a [WithTimeout]
// pattern. Instead of always bounding a call at the fixed duration d, the policy
// bounds it at clamp(percentile-latency × multiplier, floor, d), recomputed from a
// sliding window of recent SUCCESSFUL call latencies. The duration d passed to
// WithTimeout becomes the hard ceiling — the adaptive value can only tighten the
// timeout below it, never raise it — and the fallback used until
// [AdaptiveTimeoutMinSamples] successes have accumulated (a cold or low-traffic
// policy therefore uses the operator's full timeout).
//
// Defaults are p99 × 2.0 with no floor and a 20-sample warmup; tune them with
// [AdaptiveTimeoutPercentile], [AdaptiveTimeoutMultiplier], [AdaptiveTimeoutFloor]
// and [AdaptiveTimeoutMinSamples]. Only successful calls feed the window, so a
// timeout never inflates the percentile that set it. The window measures the
// bounded call — every inner pattern (retry, hedge, breaker) the timeout wraps.
func AdaptiveTimeout(opts ...AdaptiveTimeoutOption) TimeoutOption {
	return func(cfg *timeoutConfig) {
		ac := &adaptiveTimeoutConfig{}
		for _, o := range opts {
			o(ac)
		}

		cfg.adaptive = ac
	}
}

// AdaptiveTimeoutPercentile sets the latency percentile (in (0, 1]) the adaptive
// timeout is derived from; a higher percentile tolerates more of the latency tail
// before timing out. An out-of-range value resets to the default 0.99.
func AdaptiveTimeoutPercentile(q float64) AdaptiveTimeoutOption {
	return func(cfg *adaptiveTimeoutConfig) {
		cfg.percentile = q
	}
}

// AdaptiveTimeoutMultiplier sets the headroom multiplier applied to the percentile
// latency (timeout = percentile × multiplier). It must be at least 1 — a value
// below the driving percentile would time out a large fraction of healthy calls —
// and a value less than 1 resets to the default 2.0.
func AdaptiveTimeoutMultiplier(m float64) AdaptiveTimeoutOption {
	return func(cfg *adaptiveTimeoutConfig) {
		cfg.multiplier = m
	}
}

// AdaptiveTimeoutFloor sets the floor the adaptive timeout is never reduced below,
// guarding against an over-tight deadline when the service is briefly very fast. A
// non-positive value disables the floor (the default); the ceiling always wins
// over the floor, so a floor above the [WithTimeout] duration has no effect.
func AdaptiveTimeoutFloor(d time.Duration) AdaptiveTimeoutOption {
	return func(cfg *adaptiveTimeoutConfig) {
		cfg.floor = d
	}
}

// AdaptiveTimeoutMinSamples sets how many successful calls must be in the window
// before the adaptive value is used; below it the policy bounds calls at the
// configured ceiling. A non-positive value resets to the default 20.
func AdaptiveTimeoutMinSamples(n int) AdaptiveTimeoutOption {
	return func(cfg *adaptiveTimeoutConfig) {
		cfg.minSamples = int64(n)
	}
}

// resolve fills the adaptive-timeout defaults and clamps each tunable into its
// valid range. An out-of-range value is reset rather than rejected, matching the
// tolerant clamp-on-invalid contract of the other adaptive patterns.
func (c *adaptiveTimeoutConfig) resolve() {
	if c.percentile <= 0 || c.percentile > 1 {
		c.percentile = defaultAdaptiveTimeoutPercentile
	}

	if c.multiplier < 1 {
		c.multiplier = defaultAdaptiveTimeoutMultiplier
	}

	if c.minSamples <= 0 {
		c.minSamples = defaultAdaptiveTimeoutMinSamples
	}

	if c.floor < 0 {
		c.floor = 0
	}
}

// newAdaptiveTimeout builds the live controller from a resolved config, driven by
// clock (the policy's clock, shared with every pattern). The embedded estimator
// seeds its cached epoch to a sentinel so the first compute always refreshes.
func newAdaptiveTimeout(cfg *adaptiveTimeoutConfig, clock Clock) *adaptiveTimeout {
	cfg.resolve()

	adaptive := &adaptiveTimeout{
		windowedPercentile: newWindowedPercentile(clock),
	}
	adaptive.cfg.Store(cfg)

	return adaptive
}

// compute returns the timeout to apply to the next call: the driving percentile of
// recent successful latencies times the multiplier, clamped to [floor, ceiling].
// Until minSamples successes have been observed it returns ceiling. The ceiling is
// applied last so the operator's hard maximum always wins and the float→Duration
// cast cannot overflow (the clamped value is at most float64(ceiling)).
func (at *adaptiveTimeout) compute(ceiling time.Duration) time.Duration {
	cfg := at.cfg.Load()

	value, samples := at.estimate(cfg.percentile)
	if samples < cfg.minSamples {
		return ceiling
	}

	target := cfg.multiplier * float64(value)

	if floorF := float64(cfg.floor); target < floorF {
		target = floorF
	}

	if target >= float64(ceiling) {
		return ceiling
	}

	return time.Duration(target)
}

// record feeds a completed call's latency into the percentile window, but only on
// success: a failed or timed-out call is not representative of healthy service
// time, and a timeout's latency (≈ the timeout itself) would feed back into the
// percentile and inflate the very value that produced it.
func (at *adaptiveTimeout) record(elapsed time.Duration, err error) {
	if err != nil {
		return
	}

	at.observe(elapsed)
}

// reconfigure overlays new adaptive tunables at runtime: an option left unset keeps
// its current value, while one that passes an out-of-range value resets that field
// to its default. It runs under the policy's reconfigure mutex, so the
// copy-overlay-store cannot race another reconfigure; a concurrent compute reads
// the config pointer atomically and sees either the old or the new value whole.
//
// The embedded estimator's per-epoch cache is invalidated so a percentile change
// takes effect on the next call rather than lagging until the current epoch rolls
// over — the cache is keyed on the epoch alone, not on the driving percentile.
func (at *adaptiveTimeout) reconfigure(opts ...AdaptiveTimeoutOption) {
	cfg := *at.cfg.Load()
	for _, o := range opts {
		o(&cfg)
	}

	cfg.resolve()
	at.cfg.Store(&cfg)
	at.invalidate()
}
