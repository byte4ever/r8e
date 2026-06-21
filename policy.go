package r8e

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Policy[T] — the central integration type
// ---------------------------------------------------------------------------.

type (
	// Policy composes multiple resilience patterns (timeout, retry, circuit
	// breaker, rate limiter, bulkhead, hedge, fallback) behind a single [Do]
	// method. Use [NewPolicy] with functional options to configure it.
	//
	// Pattern: Functional Options — configures Policy[T] via composable
	// [Option] values. Every With* constructor returns an Option, so passing a
	// non-option to [NewPolicy] is a compile error and a misconfigured policy
	// cannot be built silently.
	Policy[T any] struct {
		chain          Middleware[T]
		circuitBreaker *CircuitBreaker
		rateLimiter    *RateLimiter
		bulkhead       *Bulkhead
		adaptive       *AdaptiveLimiter
		retryBudget    *RetryBudget
		coalescer      *Coalescer[T]
		registry       *Registry
		metrics        *policyMetrics
		// Reloadable cells for the stateless patterns; nil when the pattern is
		// absent. The middleware reads them per call so Reconfigure takes
		// effect without rebuilding the chain.
		timeout *atomic.Int64                 // timeout in nanoseconds
		hedge   *atomic.Int64                 // hedge delay in nanoseconds
		retry   *atomic.Pointer[retryRuntime] // retry attempts/strategy/opts
		name    string
		deps    []HealthReporter
		// affectsReadiness gates Kubernetes readiness when this policy is
		// critically unhealthy (see WithReadinessImpact). False by default.
		affectsReadiness bool
	}

	// retryRuntime is the hot-swappable retry configuration read per call.
	retryRuntime struct {
		strategy    BackoffStrategy
		opts        []RetryOption
		maxAttempts int
	}

	// Option configures a [Policy] during [NewPolicy]. Construct options with
	// the With* functions and [DependsOn]; the interface is closed to the
	// package so the set of valid options is fixed and type-checked.
	Option interface {
		apply(*policySetup)
	}

	// optionFunc adapts a plain function to the Option interface.
	optionFunc func(*policySetup)

	// policySetup holds the configuration collected from all options before
	// the typed middleware chain is built in NewPolicy.
	policySetup struct {
		clock    Clock
		hooks    Hooks
		registry *Registry

		timeout        *time.Duration
		retry          *retryDesc
		circuitBreaker *circuitBreakerDesc
		rateLimit      *rateLimitDesc
		bulkhead       *int
		adaptive       *adaptiveDesc
		hedge          *time.Duration
		fallbackValue  *staticFallback
		fallbackFunc   *funcFallback
		retryBudget    *RetryBudget
		coalesce       *coalesceDesc
		deps           []HealthReporter

		affectsReadiness bool
	}

	// retryDesc holds deferred retry configuration.
	retryDesc struct {
		strategy    BackoffStrategy
		opts        []RetryOption
		maxAttempts int
	}

	// circuitBreakerDesc holds deferred circuit breaker configuration.
	circuitBreakerDesc struct {
		opts []CircuitBreakerOption
	}

	// rateLimitDesc holds deferred rate limiter configuration.
	rateLimitDesc struct {
		opts []RateLimitOption
		rate float64
	}

	// coalesceDesc holds deferred request-coalescing configuration. A non-nil
	// pointer marks coalescing as requested; keyFn nil within it is the
	// misconfiguration NewPolicy rejects with ErrCoalesceNilKeyFunc.
	coalesceDesc struct {
		keyFn func(context.Context) string
	}

	// adaptiveDesc holds deferred adaptive-concurrency configuration.
	adaptiveDesc struct {
		opts []AdaptiveOption
	}

	// staticFallback carries a WithFallback value (typed T, erased to any).
	// NewPolicy[T] asserts it back to T and panics on a mismatch, since a
	// fallback typed for a different T than the policy is a programmer error.
	staticFallback struct {
		value any
	}

	// funcFallback carries a WithFallbackFunc value (func(error) (T, error),
	// erased to any), asserted back to T in NewPolicy[T].
	funcFallback struct {
		fn any
	}
)

func (f optionFunc) apply(s *policySetup) { f(s) }

// Name returns the policy's name.
func (p *Policy[T]) Name() string { return p.name }

// Do executes fn through the composed middleware chain.
//
//nolint:ireturn // generic type parameter T, not an interface
func (p *Policy[T]) Do(
	ctx context.Context,
	fn func(context.Context) (T, error),
) (T, error) {
	wrapped := p.chain(fn)

	//nolint:wrapcheck // middleware chain error returned as-is
	return wrapped(ctx)
}

// ---------------------------------------------------------------------------
// With* functions — all return Option
// ---------------------------------------------------------------------------.

// WithClock sets the clock used by all resilience patterns within this policy.
func WithClock(c Clock) Option {
	return optionFunc(func(s *policySetup) {
		s.clock = c
	})
}

// WithHooks sets the lifecycle hooks for all resilience patterns within this
// policy. A nil argument is ignored, leaving the default (no-op) hooks.
func WithHooks(h *Hooks) Option {
	return optionFunc(func(s *policySetup) {
		if h != nil {
			s.hooks = *h
		}
	})
}

// WithRegistry sets an explicit registry for the policy to register with.
// If not provided, named policies auto-register with DefaultRegistry.
func WithRegistry(reg *Registry) Option {
	return optionFunc(func(s *policySetup) {
		s.registry = reg
	})
}

// WithTimeout adds a timeout that cancels slow calls after d.
func WithTimeout(d time.Duration) Option {
	return optionFunc(func(s *policySetup) {
		s.timeout = &d
	})
}

// WithRetry adds retry logic with the given maximum attempts, backoff strategy,
// and optional retry configuration.
func WithRetry(
	maxAttempts int,
	strategy BackoffStrategy,
	opts ...RetryOption,
) Option {
	return optionFunc(func(s *policySetup) {
		s.retry = &retryDesc{
			maxAttempts: maxAttempts,
			strategy:    strategy,
			opts:        opts,
		}
	})
}

// WithRetryBudget adds an adaptive retry budget that throttles retries when the
// downstream failure rate drains the budget, preventing retry storms. It
// requires WithRetry; a policy configured with a budget but no retry pattern
// panics in NewPolicy. To coordinate retries across several policies, share one
// budget via NewRetryBudget and WithSharedRetryBudget instead.
func WithRetryBudget(opts ...RetryBudgetOption) Option {
	return optionFunc(func(s *policySetup) {
		s.retryBudget = NewRetryBudget(opts...)
	})
}

// WithSharedRetryBudget attaches an existing RetryBudget, letting several
// policies share one bucket so their retries are throttled in concert. Like
// WithRetryBudget it requires WithRetry. A nil budget is ignored.
func WithSharedRetryBudget(budget *RetryBudget) Option {
	return optionFunc(func(s *policySetup) {
		s.retryBudget = budget
	})
}

// WithCircuitBreaker adds a circuit breaker that fast-fails when the downstream
// is unhealthy.
func WithCircuitBreaker(opts ...CircuitBreakerOption) Option {
	return optionFunc(func(s *policySetup) {
		s.circuitBreaker = &circuitBreakerDesc{opts: opts}
	})
}

// WithRateLimit adds a token-bucket rate limiter that allows rate tokens per
// second.
func WithRateLimit(rate float64, opts ...RateLimitOption) Option {
	return optionFunc(func(s *policySetup) {
		s.rateLimit = &rateLimitDesc{rate: rate, opts: opts}
	})
}

// WithBulkhead adds a concurrency limiter that rejects calls when all slots are
// in use.
func WithBulkhead(maxConcurrent int) Option {
	return optionFunc(func(s *policySetup) {
		s.bulkhead = &maxConcurrent
	})
}

// WithAdaptiveConcurrency adds an adaptive concurrency limiter that tunes its
// own limit from observed call latency (Netflix's Gradient2 algorithm), instead
// of the fixed ceiling of [WithBulkhead]. Calls arriving while in-flight is at
// the current limit are rejected with [ErrConcurrencyLimited]. See
// [AdaptiveLimiter] and the [InitialLimit], [MinLimit], [MaxLimit], and
// [RTTTolerance] options.
//
// It occupies the same chain slot as the bulkhead, so it is mutually exclusive
// with [WithBulkhead]: configuring both panics [NewPolicy] with
// [ErrConcurrencyLimiterConflict].
//
// The limiter sits outside retry and hedge in the chain, so the round-trip time
// it samples spans those inner stages — a call that retries with backoff reports
// the total elapsed time, not the raw downstream latency. That is intended: the
// limiter bounds the total concurrent work admitted into the downstream
// (retries included). Tune [RTTTolerance] up if retry backoff makes it react too
// eagerly.
func WithAdaptiveConcurrency(opts ...AdaptiveOption) Option {
	return optionFunc(func(s *policySetup) {
		s.adaptive = &adaptiveDesc{opts: opts}
	})
}

// WithHedge adds a hedged request that fires a second concurrent call after
// delay.
func WithHedge(delay time.Duration) Option {
	return optionFunc(func(s *policySetup) {
		s.hedge = &delay
	})
}

// WithCoalesce adds request coalescing (singleflight): concurrent calls for
// which keyFn returns the same non-empty key collapse into a single shared
// execution, and every caller receives that one result (see [Coalescer]). keyFn
// derives the coalescing key from the call's context — stamp request identity
// into ctx upstream and read it back here. Returning an empty string opts a call
// out of coalescing, so it runs on its own.
//
// Coalescing sits just inside the fallback layer and outside every other
// pattern, so the shared execution runs the timeout, circuit breaker, rate
// limiter, bulkhead, retry, and hedge once for the whole group while each caller
// still gets its own fallback.
//
// Because [Coalescer] runs the shared call under a context detached from its
// callers, coalescing requires a [WithTimeout] to bound that call — a leader
// whose fn never returns would otherwise park a goroutine and wedge its key.
// Both a nil keyFn and a missing timeout are misconfigurations: [NewPolicy]
// panics with [ErrCoalesceNilKeyFunc] or [ErrCoalesceWithoutTimeout]
// respectively.
func WithCoalesce(keyFn func(context.Context) string) Option {
	return optionFunc(func(s *policySetup) {
		s.coalesce = &coalesceDesc{keyFn: keyFn}
	})
}

// WithFallback adds a static fallback value returned when the call fails.
// The value's type must match the Policy's type parameter T; a mismatch panics
// in [NewPolicy].
func WithFallback[T any](val T) Option {
	return optionFunc(func(s *policySetup) {
		s.fallbackValue = &staticFallback{value: val}
	})
}

// WithFallbackFunc adds a fallback function called with the error when the call
// fails. The function signature must be func(error) (T, error) matching the
// Policy's type parameter; a mismatch panics in [NewPolicy].
func WithFallbackFunc[T any](fn func(error) (T, error)) Option {
	return optionFunc(func(s *policySetup) {
		s.fallbackFunc = &funcFallback{fn: fn}
	})
}

// DependsOn declares hierarchical health dependencies. If any dependency
// reports CriticalityCritical and is unhealthy, this policy's health
// status will be degraded.
func DependsOn(reporters ...HealthReporter) Option {
	return optionFunc(func(s *policySetup) {
		s.deps = append(s.deps, reporters...)
	})
}

// WithReadinessImpact makes this policy gate Kubernetes readiness: when it is
// critically unhealthy (e.g. its circuit breaker is open), [Registry.CheckReadiness]
// reports Ready=false and the readiness handler returns 503.
//
// Without this option (the default) a policy's health is reported but never
// removes the pod from rotation. This avoids correlated, fleet-wide readiness
// flips when a shared downstream dependency degrades and trips the breaker on
// every replica at once. Enable it only for a dependency the pod genuinely
// cannot serve without; rely on the probe's failureThreshold/periodSeconds for
// hysteresis.
func WithReadinessImpact() Option {
	return optionFunc(func(s *policySetup) {
		s.affectsReadiness = true
	})
}

// ---------------------------------------------------------------------------
// NewPolicy[T] — construct and wire up the policy
// ---------------------------------------------------------------------------.

// NewPolicy creates a new [Policy] with the given name and options. Each
// configured pattern contributes a middleware; patterns are auto-sorted by
// priority via [SortPatterns] before chaining. A named policy auto-registers
// with its registry (or [DefaultRegistry] if none is given).
func NewPolicy[T any](name string, opts ...Option) *Policy[T] {
	var setup policySetup
	for _, opt := range opts {
		opt.apply(&setup)
	}

	// A retry budget gates retries; without a retry pattern it has nothing to
	// do, so reject the misconfiguration loudly rather than silently ignore it.
	// BuildOptions catches the same case for config-driven construction and
	// returns ErrRetryBudgetWithoutRetry instead of reaching this panic.
	if setup.retryBudget != nil && setup.retry == nil {
		panic(ErrRetryBudgetWithoutRetry)
	}

	if setup.coalesce != nil {
		// Coalescing cannot group calls without a key function, and its detached
		// shared call needs a timeout to bound it (see WithCoalesce). Reject both
		// misconfigurations loudly rather than degrade silently or leak.
		if setup.coalesce.keyFn == nil {
			panic(ErrCoalesceNilKeyFunc)
		}

		if setup.timeout == nil {
			panic(ErrCoalesceWithoutTimeout)
		}
	}

	// The bulkhead and the adaptive limiter both drive the concurrency slot;
	// configuring both is contradictory. BuildOptions catches the same case for
	// config-driven construction and returns ErrConcurrencyLimiterConflict.
	if setup.bulkhead != nil && setup.adaptive != nil {
		panic(ErrConcurrencyLimiterConflict)
	}

	if setup.clock == nil {
		setup.clock = RealClock{}
	}

	// Wrap the caller's hooks so every lifecycle event also increments a
	// metrics counter (see policyMetrics.instrument).
	metrics := &policyMetrics{}
	hooks := metrics.instrument(&setup.hooks)
	clock := setup.clock

	var (
		entries        []PatternEntry[T]
		circuitBreaker *CircuitBreaker
		rateLimiter    *RateLimiter
		bulkhead       *Bulkhead
		adaptive       *AdaptiveLimiter
		coalescer      *Coalescer[T]
		timeoutCell    *atomic.Int64
		hedgeCell      *atomic.Int64
		retryCell      *atomic.Pointer[retryRuntime]
	)

	if setup.timeout != nil {
		timeoutCell = new(atomic.Int64)
		timeoutCell.Store(int64(*setup.timeout))
		entries = append(entries, newTimeoutEntry[T](timeoutCell, &hooks))
	}

	if setup.retry != nil {
		retryCell = new(atomic.Pointer[retryRuntime])
		retryCell.Store(&retryRuntime{
			strategy:    setup.retry.strategy,
			opts:        setup.retry.opts,
			maxAttempts: setup.retry.maxAttempts,
		})
		entries = append(
			entries,
			newRetryEntry[T](retryCell, &hooks, clock, setup.retryBudget),
		)
	}

	if setup.circuitBreaker != nil {
		circuitBreaker = NewCircuitBreaker(clock, &hooks, setup.circuitBreaker.opts...)
		entries = append(entries, newCircuitBreakerEntry[T](circuitBreaker))
	}

	if setup.rateLimit != nil {
		rateLimiter = NewRateLimiter(setup.rateLimit.rate, clock, &hooks, setup.rateLimit.opts...)
		entries = append(entries, newRateLimiterEntry[T](rateLimiter))
	}

	if setup.bulkhead != nil {
		bulkhead = NewBulkhead(*setup.bulkhead, &hooks)
		entries = append(entries, newBulkheadEntry[T](bulkhead))
	}

	if setup.adaptive != nil {
		adaptive = NewAdaptiveLimiter(clock, &hooks, setup.adaptive.opts...)
		entries = append(entries, newAdaptiveEntry[T](adaptive))
	}

	if setup.hedge != nil {
		hedgeCell = new(atomic.Int64)
		hedgeCell.Store(int64(*setup.hedge))
		entries = append(entries, newHedgeEntry[T](hedgeCell, &hooks, clock))
	}

	if setup.coalesce != nil {
		coalescer = NewCoalescer[T](&hooks)
		entries = append(
			entries,
			newCoalesceEntry[T](coalescer, setup.coalesce.keyFn),
		)
	}

	if setup.fallbackValue != nil {
		entries = append(entries, newStaticFallbackEntry[T](*setup.fallbackValue, &hooks))
	}

	if setup.fallbackFunc != nil {
		entries = append(entries, newFuncFallbackEntry[T](*setup.fallbackFunc, &hooks))
	}

	chain := Chain[T](SortPatterns[T](entries)...)

	var reg *Registry
	if name != "" {
		reg = setup.registry
		if reg == nil {
			reg = DefaultRegistry()
		}
	}

	policy := &Policy[T]{
		name:             name,
		chain:            chain,
		circuitBreaker:   circuitBreaker,
		rateLimiter:      rateLimiter,
		bulkhead:         bulkhead,
		adaptive:         adaptive,
		retryBudget:      setup.retryBudget,
		coalescer:        coalescer,
		metrics:          metrics,
		timeout:          timeoutCell,
		hedge:            hedgeCell,
		retry:            retryCell,
		deps:             setup.deps,
		affectsReadiness: setup.affectsReadiness,
		registry:         reg,
	}

	if reg != nil {
		reg.Register(policy)
	}

	return policy
}

// ---------------------------------------------------------------------------
// Per-pattern middleware entry builders
// ---------------------------------------------------------------------------.

func newTimeoutEntry[T any](cell *atomic.Int64, hooks *Hooks) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityTimeout,
		Name:     "timeout",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoTimeout[T](ctx, time.Duration(cell.Load()), next, hooks)
			}
		},
	}
}

func newRetryEntry[T any](
	cell *atomic.Pointer[retryRuntime],
	hooks *Hooks,
	clock Clock,
	budget *RetryBudget,
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityRetry,
		Name:     "retry",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				rt := cell.Load()

				return DoRetry[T](ctx, next, RetryParams{
					MaxAttempts: rt.maxAttempts,
					Strategy:    rt.strategy,
					Hooks:       hooks,
					Clock:       clock,
					Budget:      budget,
					Opts:        rt.opts,
				})
			}
		},
	}
}

func newCircuitBreakerEntry[T any](cb *CircuitBreaker) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityCircuitBreaker,
		Name:     "circuit_breaker",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if err := cb.Allow(); err != nil {
					var zero T

					return zero, err //nolint:wrapcheck // circuit breaker error returned as-is
				}

				val, err := next(ctx)
				if err != nil {
					cb.RecordFailure()
				} else {
					cb.RecordSuccess()
				}

				return val, err //nolint:wrapcheck // caller's error returned as-is
			}
		},
	}
}

func newRateLimiterEntry[T any](rl *RateLimiter) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityRateLimiter,
		Name:     "rate_limiter",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if err := rl.Allow(ctx); err != nil {
					var zero T

					return zero, err //nolint:wrapcheck // rate limiter error returned as-is
				}

				return next(ctx)
			}
		},
	}
}

func newBulkheadEntry[T any](bh *Bulkhead) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityBulkhead,
		Name:     "bulkhead",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if err := bh.Acquire(); err != nil {
					var zero T

					return zero, err //nolint:wrapcheck // bulkhead error returned as-is
				}

				defer bh.Release()

				return next(ctx)
			}
		},
	}
}

func newAdaptiveEntry[T any](limiter *AdaptiveLimiter) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityBulkhead,
		Name:     "adaptive_concurrency",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				done, err := limiter.Acquire()
				if err != nil {
					var zero T

					return zero, err //nolint:wrapcheck // limiter error returned as-is
				}

				defer done()

				return next(ctx)
			}
		},
	}
}

func newHedgeEntry[T any](cell *atomic.Int64, hooks *Hooks, clock Clock) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityHedge,
		Name:     "hedge",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoHedge[T](ctx, next, HedgeParams{
					Delay: time.Duration(cell.Load()),
					Hooks: hooks,
					Clock: clock,
				})
			}
		},
	}
}

func newCoalesceEntry[T any](
	coalescer *Coalescer[T],
	keyFn func(context.Context) string,
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityCoalesce,
		Name:     "coalesce",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				key := keyFn(ctx)
				if key == "" {
					// No key: opt this call out of coalescing entirely.
					return next(ctx)
				}

				return coalescer.Do(ctx, key, next)
			}
		},
	}
}

func newStaticFallbackEntry[T any](desc staticFallback, hooks *Hooks) PatternEntry[T] {
	val, ok := desc.value.(T)
	if !ok {
		var zero T

		panic(fmt.Sprintf(
			"r8e: WithFallback value has type %T, which does not match policy result type %T",
			desc.value, zero,
		))
	}

	return PatternEntry[T]{
		Priority: priorityFallback,
		Name:     "fallback",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoFallback[T](ctx, next, val, hooks)
			}
		},
	}
}

func newFuncFallbackEntry[T any](desc funcFallback, hooks *Hooks) PatternEntry[T] {
	fn, ok := desc.fn.(func(error) (T, error))
	if !ok {
		var zero T

		panic(fmt.Sprintf(
			"r8e: WithFallbackFunc has type %T, which does not match policy result type %T",
			desc.fn, zero,
		))
	}

	return PatternEntry[T]{
		Priority: priorityFallback,
		Name:     "fallback_func",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoFallbackFunc[T](ctx, next, fn, hooks)
			}
		},
	}
}
