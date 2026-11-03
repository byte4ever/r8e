package r8e

import (
	"context"
	"errors"
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

	// policyDeadlineKey is the context key under which r8e records the instant of
	// a deadline it set and that binds, that is, earlier than any deadline the
	// context already had. withPolicyTimeout sets it for DoTimeout and the
	// per-attempt retry timeout, and budgetDeadlineCtx answers it for a hard time
	// budget. IsCallerCancellation counts a deadline as r8e's only when
	// ctx.Deadline() equals it, two facts fixed when each context is created, so
	// a writer must store the instant its context's Deadline reports.
	policyDeadlineKey struct{}
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

// DoTimeout executes fn with a timeout. If fn does not complete within
// timeout, the context is cancelled and ErrTimeout is returned.
//
// The timeout is r8e's only when it is earlier than any deadline ctx already
// has. When ctx's own deadline falls before it or on the same instant,
// DoTimeout arms no timer of its own: the call ends when ctx does, without
// firing OnTimeout. That is the rule [IsCallerCancellation] applies too.
//
// Who ended the call is decided when the binding timeout fires, as for a time
// budget: a caller that cancels, or reaches its own later deadline, after
// that does not take the call back, and a caller that cancels before it keeps
// the call. fn may stop on the propagated deadline by itself and return its
// own error (a socket or client deadline) at the instant the timeout fires:
// an error fn returns once the binding timeout has passed is ErrTimeout too,
// with one OnTimeout, whichever of the two DoTimeout sees first. A failed call
// the timeout did not cut, once ctx reports its end, returns ctx's error
// rather than fn's, again whichever DoTimeout sees first, as a time budget
// does. An error fn returns before ctx reports its end stays fn's, even on
// ctx's own deadline: only r8e's deadline is decided on the clock. The check
// applies to errors only: a success DoTimeout receives before it sees the
// timeout stays a success, even on the deadline.
//
// DoTimeout returns as soon as the timeout fires or ctx ends, without waiting
// for fn: fn keeps running on its own goroutine until it returns, so it must
// synchronise whatever it shares with the caller.
//
// The context passed to fn records this deadline as r8e's own when it binds,
// which is how IsCallerCancellation tells it from the caller's. Its Err and
// [context.Cause] are those of any context.WithTimeout context.
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

	// Derive the timeout's context. A timeout that does not bind arms no
	// timer: the derived context then only follows ctx.
	timeoutCtx, cancel := withPolicyTimeout(ctx, timeout)
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

	// Wait for fn to complete or context to expire. Both arms give the same
	// answer for the same history: the timeout cut the call (see
	// policyDeadlineCut), or the caller ended it and its error comes back, or
	// fn's own result does.
	select {
	case res := <-ch:
		switch {
		case res.err == nil:
			return res.val, nil
		case policyDeadlineCut(ctx, timeoutCtx):
			// Cut by the timeout: answered below.
		case ctx.Err() != nil:
			// The caller ended the call, as the other arm would report it.
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		default:
			return res.val, res.err
		}
	case <-timeoutCtx.Done():
		if !policyDeadlineCut(ctx, timeoutCtx) {
			// The parent ended the call: it was cancelled before the timeout
			// fired, or its own deadline came no later than the timeout,
			// which then armed no timer.
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}
	}

	hooks.emitTimeout()

	return zero, ErrTimeout
}

// policyDeadlineCut reports whether the deadline r8e set on derived (see
// withPolicyTimeout) cut the call running under it: the timeout of DoTimeout,
// or the per-attempt deadline of PerAttemptTimeout. The deadline must bind
// (see deadlineBinds): one that does not bind leaves derived with parent's
// deadline. The decision is then the first end derived records: its own
// deadline means a cut, a parent cancelled before it means none, whatever
// happens to parent afterwards. A binding deadline comes strictly before any
// deadline of parent, so DeadlineExceeded as the first end is r8e's own.
// When derived has not reported an end yet, the wall clock decides through
// cutByOwnDeadline: a call that returns on or after the deadline, before the
// context's timer ran, is cut unless parent was cancelled.
func policyDeadlineCut(parent, derived context.Context) bool {
	deadline, ok := derived.Deadline()
	if !ok || !deadlineBinds(parent, deadline) {
		return false
	}

	if err := derived.Err(); err != nil {
		return errors.Is(err, context.DeadlineExceeded)
	}

	return cutByOwnDeadline(parent, deadline, time.Now())
}

// cutByOwnDeadline is the one rule for who ended a call run under deadline,
// a deadline r8e set on a context derived from parent: the timeout of
// DoTimeout, the per-attempt deadline of PerAttemptTimeout, or a hard time
// budget (see budgetDeadlineCtx.settle). now is the instant the caller
// decides at, read from the wall clock for the timeouts and from the policy
// Clock for the budget.
//
// A deadline that does not bind (see deadlineBinds) is the parent's, so it
// never cuts. A parent already cancelled keeps the call. A parent found past
// its own deadline reached it after r8e's, which binds and so comes strictly
// first: the call is r8e's. A live parent leaves the decision to the clock:
// the call is cut once now reaches deadline. The parent's side is decided
// from parent.Err() alone, never on the clock: an error the call returns
// before the parent reports its end stays the call's.
func cutByOwnDeadline(parent context.Context, deadline, now time.Time) bool {
	if !deadlineBinds(parent, deadline) {
		return false
	}

	if err := parent.Err(); err != nil {
		return errors.Is(err, context.DeadlineExceeded)
	}

	return !now.Before(deadline)
}

// withPolicyTimeout derives the context for a timeout r8e sets. When the
// timeout binds (see deadlineBinds), the derived context is a
// context.WithDeadline context that also records its deadline under
// policyDeadlineKey: the same instant its Deadline reports, as
// IsCallerCancellation requires. A timeout that does not bind arms no timer of
// its own: the derived context only follows ctx, which ends first or on the
// same instant, so the call ends with ctx's error and the recorded deadline
// stays with whoever set the earlier one.
func withPolicyTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(timeout)
	if !deadlineBinds(ctx, deadline) {
		return context.WithCancel(ctx)
	}

	timeoutCtx, cancel := context.WithDeadline(ctx, deadline)

	return context.WithValue(timeoutCtx, policyDeadlineKey{}, deadline), cancel
}

// deadlineBinds reports whether a deadline r8e sets at the instant deadline is
// its own: strictly earlier than any deadline parent already has. A deadline on
// the same instant as the parent's, or later, is the parent's. The policy
// timeouts and the time budget share this rule.
func deadlineBinds(parent context.Context, deadline time.Time) bool {
	parentDeadline, ok := parent.Deadline()

	return !ok || deadline.Before(parentDeadline)
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
