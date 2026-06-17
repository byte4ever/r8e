package r8e

import (
	"context"
	"fmt"
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
		registry       *Registry
		metrics        *policyMetrics
		name           string
		deps           []HealthReporter
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
		hedge          *time.Duration
		fallbackValue  *staticFallback
		fallbackFunc   *funcFallback
		deps           []HealthReporter
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

// WithHedge adds a hedged request that fires a second concurrent call after
// delay.
func WithHedge(delay time.Duration) Option {
	return optionFunc(func(s *policySetup) {
		s.hedge = &delay
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
	)

	if setup.timeout != nil {
		entries = append(entries, newTimeoutEntry[T](*setup.timeout, &hooks))
	}

	if setup.retry != nil {
		entries = append(entries, newRetryEntry[T](*setup.retry, &hooks, clock))
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

	if setup.hedge != nil {
		entries = append(entries, newHedgeEntry[T](*setup.hedge, &hooks, clock))
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
		name:           name,
		chain:          chain,
		circuitBreaker: circuitBreaker,
		rateLimiter:    rateLimiter,
		bulkhead:       bulkhead,
		metrics:        metrics,
		deps:           setup.deps,
		registry:       reg,
	}

	if reg != nil {
		reg.Register(policy)
	}

	return policy
}

// ---------------------------------------------------------------------------
// Per-pattern middleware entry builders
// ---------------------------------------------------------------------------.

func newTimeoutEntry[T any](d time.Duration, hooks *Hooks) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityTimeout,
		Name:     "timeout",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoTimeout[T](ctx, d, next, hooks)
			}
		},
	}
}

func newRetryEntry[T any](desc retryDesc, hooks *Hooks, clock Clock) PatternEntry[T] {
	params := RetryParams{
		MaxAttempts: desc.maxAttempts,
		Strategy:    desc.strategy,
		Hooks:       hooks,
		Clock:       clock,
		Opts:        desc.opts,
	}

	return PatternEntry[T]{
		Priority: priorityRetry,
		Name:     "retry",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoRetry[T](ctx, next, params)
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

func newHedgeEntry[T any](delay time.Duration, hooks *Hooks, clock Clock) PatternEntry[T] {
	params := HedgeParams{
		Delay: delay,
		Hooks: hooks,
		Clock: clock,
	}

	return PatternEntry[T]{
		Priority: priorityHedge,
		Name:     "hedge",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoHedge[T](ctx, next, params)
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
