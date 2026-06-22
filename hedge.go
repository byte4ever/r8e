package r8e

import (
	"context"
	"sync/atomic"
	"time"
)

// Pattern: Hedged Request — after a delay, fire a second concurrent attempt.
// The first response wins; the other is cancelled. This reduces tail latency
// by racing redundant requests.

type (
	// hedgeResult holds the outcome of a hedged call attempt.
	hedgeResult[T any] struct {
		val       T
		err       error
		isPrimary bool
	}

	// HedgeParams holds the configuration for a hedged request. A nil Clock
	// defaults to [RealClock] and a nil Hooks is treated as a no-op, so the
	// zero value beyond Delay is usable. A nil Budget disables concurrency-budget
	// gating (the hedge always fires).
	HedgeParams struct {
		Clock Clock
		Hooks *Hooks
		// RecordPrimary, when non-nil, is called with the primary attempt's elapsed
		// time and error once it completes (used by the adaptive hedge delay to feed
		// its percentile window). It records the PRIMARY only — never the hedge — so
		// a winning hedge cannot bias the percentile that sized its own delay; the
		// primary's error is passed through so the recorder can drop non-successes
		// (a hedge that wins cancels the primary, whose error then filters it out).
		RecordPrimary func(elapsed time.Duration, err error)
		Budget        *ConcurrencyBudget
		Delay         time.Duration
	}

	// HedgeOption configures the hedge pattern built by [WithHedge].
	//
	// Pattern: Functional Options — composable optional settings applied to a
	// private config, keeping WithHedge's signature stable as adaptive behavior is
	// layered onto the fixed hedge delay.
	HedgeOption func(*hedgeConfig)

	// hedgeConfig collects the optional [WithHedge] settings before the policy
	// builds the hedge middleware. adaptive is non-nil once [AdaptiveHedge] was
	// passed.
	hedgeConfig struct {
		adaptive *adaptiveHedgeConfig
	}

	// AdaptiveHedgeOption configures percentile-driven adaptive hedge delay (see
	// [AdaptiveHedge]).
	AdaptiveHedgeOption func(*adaptiveHedgeConfig)

	// adaptiveHedgeConfig holds the adaptive-hedge tunables. Like its timeout
	// sibling it is both the option target (before resolve fills the defaults) and
	// the resolved value stored atomically on the live [adaptiveHedge]; a
	// reconfigure copies the current resolved value, overlays the new options, and
	// re-resolves.
	adaptiveHedgeConfig struct {
		percentile float64
		multiplier float64
		floor      time.Duration
		minSamples int64
	}

	// adaptiveHedge is the live percentile-driven hedge-delay controller: a
	// dedicated sliding-window DDSketch of the PRIMARY attempt's SUCCESSFUL
	// latencies (the embedded [windowedPercentile]) plus the hot-swappable
	// tunables. Each call's hedge fires after clamp(percentile-latency × multiplier,
	// floor, ceiling), where ceiling is the [WithHedge] delay (a hard maximum the
	// adaptive value never exceeds, so the operator's delay bounds the latest a
	// hedge can fire). Firing at, by default, the p95 means only genuine stragglers
	// — the slowest ~5% — are hedged, the standard "defer the redundant request
	// past the 95th percentile" rule that keeps the extra load small.
	//
	// Only the primary's own completion latency feeds the window: a winning hedge
	// cancels the primary, whose latency is then censored (not recorded), so the
	// percentile reflects the natural un-accelerated distribution and a hedge cannot
	// bias down the very value that sized its delay. The percentile read is memoized
	// per window epoch by the embedded estimator and driven by the injected [Clock],
	// so the adaptive delay is deterministic under a fake clock in tests. Safe for
	// concurrent use.
	//
	// Pattern: Adaptive Hedge — a controller sizes the hedge delay from live latency
	// feedback (its own primary-latency percentile) instead of a fixed duration, the
	// latency→hedge-delay analogue of the [adaptiveTimeout]'s latency→timeout.
	adaptiveHedge struct {
		*windowedPercentile
		cfg atomic.Pointer[adaptiveHedgeConfig]
	}
)

const (
	// defaultAdaptiveHedgePercentile is the latency percentile the adaptive hedge
	// fires at by default — p95, the classic "hedge the slowest 5%" threshold from
	// Google's tail-at-scale hedging, which keeps the added redundant load small.
	defaultAdaptiveHedgePercentile = 0.95

	// defaultAdaptiveHedgeMultiplier is the headroom applied to the percentile by
	// default: 1.0, so the hedge fires exactly at the observed percentile latency.
	defaultAdaptiveHedgeMultiplier = 1.0

	// defaultAdaptiveHedgeMinSamples is how many successful primaries must be in the
	// window before the adaptive delay is trusted; below it the policy falls back to
	// the configured ceiling delay.
	defaultAdaptiveHedgeMinSamples = 20
)

// DoHedge executes fn and, if it hasn't completed after delay, fires a second
// concurrent attempt. The first response wins; the other is cancelled. A nil
// params.Clock defaults to [RealClock]; a nil params.Hooks is a no-op.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoHedge[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	params HedgeParams,
) (T, error) {
	var zero T

	if params.Clock == nil {
		params.Clock = RealClock{}
	}

	// If the parent context is already done, return its error immediately.
	if ctx.Err() != nil {
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}

	// Buffered channel of size 2 to receive results from both goroutines.
	results := make(chan hedgeResult[T], 2)

	// Start primary call with a cancellable context.
	primaryCtx, primaryCancel := context.WithCancel(ctx)
	defer primaryCancel()

	primaryStart := params.Clock.Now()

	go func() {
		val, err := fn(primaryCtx)

		// Record the primary's latency BEFORE sending the result. The channel
		// send/receive then establishes happens-before, so a caller that receives
		// this primary result is guaranteed to see the recorded sample — the
		// adaptive hedge window is deterministically up to date once Do returns. A
		// cancelled (hedge lost the race) or failed primary carries a non-nil err
		// the recorder drops, so only genuine primary completions feed the window.
		if params.RecordPrimary != nil {
			params.RecordPrimary(params.Clock.Since(primaryStart), err)
		}

		results <- hedgeResult[T]{val: val, err: err, isPrimary: true}
	}()

	// Start a timer for the hedge delay.
	timer := params.Clock.NewTimer(params.Delay)

	// Wait for primary completion, timer, or context cancellation.
	select {
	case result := <-results:
		// Primary completed before delay elapsed.
		timer.Stop()

		if result.err != nil {
			return result.val, result.err
		}

		return result.val, nil

	case <-timer.C():
		// Delay elapsed; primary is still running. Skip the hedge if the total
		// time budget is spent — a second request cannot help within it — and
		// just wait for the primary.
		if remaining, ok := timeBudgetRemaining(ctx, params.Clock); ok && remaining <= 0 {
			//nolint:wrapcheck // primary/context error returned as-is
			return waitForPrimary(ctx, results)
		}

		// The hedge is a second concurrent attempt: gate it on the concurrency
		// budget. If the budget is exhausted, skip the hedge and just wait for
		// the primary — unlike a suppressed retry this is not an error, the
		// primary still runs. The permit is released when the hedge goroutine's
		// fn completes (even if it loses the race).
		if !params.Budget.tryAcquire() {
			params.Hooks.emitConcurrencyBudgetExceeded()

			//nolint:wrapcheck // primary/context error returned as-is
			return waitForPrimary(ctx, results)
		}

		// Fire hedge.
		params.Hooks.emitHedgeTriggered()

		hedgeCtx, hedgeCancel := context.WithCancel(ctx)
		defer hedgeCancel()

		go func() {
			defer params.Budget.release()

			v, err := fn(hedgeCtx)
			results <- hedgeResult[T]{val: v, err: err, isPrimary: false}
		}()

		// Now wait for first completion from either goroutine.
		//nolint:wrapcheck // internal delegation
		return waitForResults(
			ctx,
			results,
			primaryCancel,
			hedgeCancel,
			params.Hooks,
		)

	case <-ctx.Done():
		timer.Stop()

		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}

// waitForPrimary waits for the primary attempt to complete, or for ctx to be
// cancelled — used when the time budget is spent so no hedge is launched.
//
//nolint:ireturn // generic type parameter T, not an interface
func waitForPrimary[T any](
	ctx context.Context,
	results <-chan hedgeResult[T],
) (T, error) {
	select {
	case result := <-results:
		return result.val, result.err //nolint:wrapcheck // caller's error as-is
	case <-ctx.Done():
		var zero T

		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}

// waitForResults waits for results from both the primary and hedge goroutines
// after the hedge has been triggered. It returns the first successful result,
// or an error if both fail.
//
// for internal use.
//
//nolint:ireturn,revive // generic type parameter T; argument count justified
func waitForResults[T any](
	ctx context.Context,
	results chan hedgeResult[T],
	primaryCancel, hedgeCancel context.CancelFunc,
	hooks *Hooks,
) (T, error) {
	var zero T

	// Wait for the first result.
	select {
	case result := <-results:
		if result.err == nil {
			// Success: cancel the loser.
			if result.isPrimary {
				hedgeCancel()
			} else {
				primaryCancel()
				hooks.emitHedgeWon()
			}

			return result.val, nil
		}

		// First result was an error. Wait for the second.
		select {
		case r2 := <-results:
			if r2.err == nil {
				// Second attempt succeeded.
				if r2.isPrimary {
					hedgeCancel()
				} else {
					primaryCancel()
					hooks.emitHedgeWon()
				}

				return r2.val, nil
			}
			// Both failed. Return the first error received.
			return zero, result.err

		case <-ctx.Done():
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}

	case <-ctx.Done():
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}

// ---------------------------------------------------------------------------
// Adaptive hedge delay — fire the hedge at an observed latency percentile
// ---------------------------------------------------------------------------.

// AdaptiveHedge enables percentile-driven adaptive hedge delay on a [WithHedge]
// pattern. Instead of always firing the second attempt after the fixed delay, the
// policy fires it after clamp(percentile-latency × multiplier, floor, delay),
// recomputed from a sliding window of recent SUCCESSFUL primary latencies. The
// delay passed to WithHedge becomes the hard ceiling — the adaptive value can only
// pull the hedge earlier below it, never later — and the fallback used until
// [AdaptiveHedgeMinSamples] successes have accumulated (a cold or low-traffic
// policy therefore hedges at the operator's full delay).
//
// Defaults are p95 × 1.0 with no floor and a 20-sample warmup; tune them with
// [AdaptiveHedgePercentile], [AdaptiveHedgeMultiplier], [AdaptiveHedgeFloor] and
// [AdaptiveHedgeMinSamples]. Only the primary attempt's own completion feeds the
// window, so a winning hedge — which cancels the primary — never biases the
// percentile that set its delay. Pairs with a [WithConcurrencyBudget] to cap how
// much extra load the hedges add.
func AdaptiveHedge(opts ...AdaptiveHedgeOption) HedgeOption {
	return func(cfg *hedgeConfig) {
		ac := &adaptiveHedgeConfig{}
		for _, o := range opts {
			o(ac)
		}

		cfg.adaptive = ac
	}
}

// AdaptiveHedgePercentile sets the latency percentile (in (0, 1]) the hedge delay
// is derived from; a higher percentile hedges fewer, slower calls. An out-of-range
// value resets to the default 0.95.
func AdaptiveHedgePercentile(q float64) AdaptiveHedgeOption {
	return func(cfg *adaptiveHedgeConfig) {
		cfg.percentile = q
	}
}

// AdaptiveHedgeMultiplier sets the headroom multiplier applied to the percentile
// latency (delay = percentile × multiplier). It must be positive — a value at or
// below the percentile hedges more aggressively, above it more conservatively —
// and a non-positive value resets to the default 1.0.
func AdaptiveHedgeMultiplier(m float64) AdaptiveHedgeOption {
	return func(cfg *adaptiveHedgeConfig) {
		cfg.multiplier = m
	}
}

// AdaptiveHedgeFloor sets the floor the adaptive hedge delay is never reduced
// below, guarding against firing a redundant request too eagerly when the service
// is briefly very fast. A non-positive value disables the floor (the default); the
// ceiling always wins over the floor, so a floor above the [WithHedge] delay has no
// effect.
func AdaptiveHedgeFloor(d time.Duration) AdaptiveHedgeOption {
	return func(cfg *adaptiveHedgeConfig) {
		cfg.floor = d
	}
}

// AdaptiveHedgeMinSamples sets how many successful primaries must be in the window
// before the adaptive delay is used; below it the policy fires the hedge at the
// configured ceiling delay. A non-positive value resets to the default 20.
func AdaptiveHedgeMinSamples(n int) AdaptiveHedgeOption {
	return func(cfg *adaptiveHedgeConfig) {
		cfg.minSamples = int64(n)
	}
}

// resolve fills the adaptive-hedge defaults and clamps each tunable into its valid
// range. An out-of-range value is reset rather than rejected, matching the tolerant
// clamp-on-invalid contract of the other adaptive patterns.
func (c *adaptiveHedgeConfig) resolve() {
	if c.percentile <= 0 || c.percentile > 1 {
		c.percentile = defaultAdaptiveHedgePercentile
	}

	if c.multiplier <= 0 {
		c.multiplier = defaultAdaptiveHedgeMultiplier
	}

	if c.minSamples <= 0 {
		c.minSamples = defaultAdaptiveHedgeMinSamples
	}

	if c.floor < 0 {
		c.floor = 0
	}
}

// newAdaptiveHedge builds the live controller from a resolved config, driven by
// clock (the policy's clock, shared with every pattern). The embedded estimator
// seeds its cached epoch to a sentinel so the first compute always refreshes.
func newAdaptiveHedge(cfg *adaptiveHedgeConfig, clock Clock) *adaptiveHedge {
	cfg.resolve()

	adaptive := &adaptiveHedge{
		windowedPercentile: newWindowedPercentile(clock),
	}
	adaptive.cfg.Store(cfg)

	return adaptive
}

// compute returns the delay before the next call's hedge fires: the driving
// percentile of recent successful primary latencies times the multiplier, clamped
// to [floor, ceiling]. Until minSamples successes have been observed it returns
// ceiling. The ceiling is applied last so the operator's hard maximum always wins
// and the float→Duration cast cannot overflow (the clamped value is at most
// float64(ceiling)).
func (ah *adaptiveHedge) compute(ceiling time.Duration) time.Duration {
	cfg := ah.cfg.Load()

	value, samples := ah.estimate(cfg.percentile)
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

// record feeds a completed primary's latency into the percentile window, but only
// on success: a failed primary, or one cancelled because the hedge won the race, is
// not representative of healthy service time, and recording a hedge-shortened
// outcome would feed back into the percentile and pull down the very delay that
// produced it.
func (ah *adaptiveHedge) record(elapsed time.Duration, err error) {
	if err != nil {
		return
	}

	ah.observe(elapsed)
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
func (ah *adaptiveHedge) reconfigure(opts ...AdaptiveHedgeOption) {
	cfg := *ah.cfg.Load()
	for _, o := range opts {
		o(&cfg)
	}

	cfg.resolve()
	ah.cfg.Store(&cfg)
	ah.invalidate()
}
