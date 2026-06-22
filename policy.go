package r8e

import (
	"context"
	"fmt"
	"sync"
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
		chain             Middleware[T]
		circuitBreaker    *CircuitBreaker
		rateLimiter       *RateLimiter
		bulkhead          *Bulkhead
		adaptive          *AdaptiveLimiter
		throttler         *Throttler
		retryBudget       *RetryBudget
		concurrencyBudget *ConcurrencyBudget
		coalescer         *Coalescer[T]
		registry          *Registry
		metrics           *policyMetrics
		// clock drives the latency window (and is the same clock injected into
		// every pattern); held so Do can time each call deterministically.
		clock Clock
		// latency records each Do() duration into a sliding-window DDSketch for
		// the p50/p95/p99 figures in Metrics. Always present (zero-config).
		latency *latencyWindow
		// adaptiveTimeout, when non-nil, sizes the timeout from observed
		// success-latency percentiles (see WithTimeout + AdaptiveTimeout); the
		// timeout cell below then holds the ceiling rather than the fixed timeout.
		adaptiveTimeout *adaptiveTimeout
		// Reloadable cells for the stateless patterns; nil when the pattern is
		// absent. The middleware reads them per call so Reconfigure takes
		// effect without rebuilding the chain.
		timeout *atomic.Int64 // timeout in nanoseconds
		// timeBudget carries the budget duration and the deadline-propagation
		// flag as one atomically-swapped value (see timeBudgetState).
		timeBudget *atomic.Pointer[timeBudgetState]
		hedge      *atomic.Int64                 // hedge delay in nanoseconds
		retry      *atomic.Pointer[retryRuntime] // retry attempts/strategy/opts
		name       string
		deps       []HealthReporter
		// reconfigureMu serializes Reconfigure so two concurrent callers cannot
		// lose a load-modify-store update to a hot-swapped cell (e.g. timeBudget,
		// whose budget and propagate-deadline flag share one atomic pointer).
		reconfigureMu sync.Mutex
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

	// retryBudgets bundles the optional storm-control budgets a retry consults —
	// the rate-limiting [RetryBudget] and the concurrency-limiting
	// [ConcurrencyBudget] — so newRetryEntry stays within the argument limit.
	retryBudgets struct {
		retry       *RetryBudget
		concurrency *ConcurrencyBudget
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

		timeout           *time.Duration
		timeoutAdaptive   *adaptiveTimeoutConfig
		timeBudget        *time.Duration
		retry             *retryDesc
		circuitBreaker    *circuitBreakerDesc
		rateLimit         *rateLimitDesc
		bulkhead          *bulkheadDesc
		adaptive          *adaptiveDesc
		throttle          *throttleDesc
		hedge             *time.Duration
		fallbackValue     *staticFallback
		fallbackFunc      *funcFallback
		retryBudget       *RetryBudget
		concurrencyBudget *ConcurrencyBudget
		coalesce          *coalesceDesc
		cache             *cacheDesc
		deps              []HealthReporter

		affectsReadiness bool
		// propagateDeadline requests a hard clock-driven deadline derived from
		// the time budget (see PropagateDeadline); ignored without timeBudget.
		propagateDeadline bool
		// panicRecover, when true, adds the innermost recover middleware that
		// catches panics and converts them to *PanicError (see WithRecover).
		panicRecover bool
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

	// bulkheadDesc holds deferred bulkhead configuration.
	bulkheadDesc struct {
		opts          []BulkheadOption
		maxConcurrent int
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

	// throttleDesc holds deferred adaptive-throttler configuration.
	throttleDesc struct {
		opts []ThrottleOption
	}

	// cacheDesc holds deferred read-through-cache configuration. The cache is
	// carried as any (a Cache[string, CacheEntry[T]] erased like WithFallback's
	// value) and asserted back to the policy's T in NewPolicy[T]; keyFn nil, a nil
	// cache, or a non-positive ttl are the misconfigurations NewPolicy rejects.
	cacheDesc struct {
		cache any
		keyFn func(context.Context) string
		opts  []CacheOption
		ttl   time.Duration
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
	start := p.clock.Now()
	wrapped := p.chain(fn)

	result, err := wrapped(ctx)

	// Record the end-to-end latency of every call — success or failure, including
	// fast-fail rejections — so the percentiles describe the policy's real
	// outward latency.
	p.latency.observe(p.clock.Since(start))

	//nolint:wrapcheck // middleware chain error returned as-is
	return result, err
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

// WithTimeout adds a timeout that cancels slow calls after the given duration.
// Pass [AdaptiveTimeout] to instead tune the timeout from observed latency
// percentiles, using the duration as the hard ceiling and warmup fallback.
func WithTimeout(timeout time.Duration, opts ...TimeoutOption) Option {
	var cfg timeoutConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return optionFunc(func(s *policySetup) {
		s.timeout = &timeout
		s.timeoutAdaptive = cfg.adaptive
	})
}

// WithTimeBudget adds a single total time budget shared across the whole call,
// so retry and hedge stop starting new work once the budget is spent. Before
// each retry, if the backoff alone would exhaust the remaining budget the retry
// stops early with [ErrTimeBudgetExceeded] (observable via the
// OnTimeBudgetExceeded hook and the TimeBudgetExceeded metric) instead of
// sleeping and launching an attempt that cannot finish in time; a hedge is not
// fired once the budget is spent.
//
// The budget is cooperative and measured against the policy's [Clock]: it gates
// whether more work starts but does not cancel an in-flight attempt — pair it
// with [WithTimeout] (a hard deadline) or PerAttemptTimeout to bound a single
// attempt. It is tighter than a per-attempt timeout because it caps the total
// time across all attempts, not each one.
//
// The budget gates only retry and hedge, so it requires [WithRetry] or
// [WithHedge]; configured with neither it would do nothing, and [NewPolicy]
// panics with [ErrTimeBudgetWithoutConsumer] instead.
//
// Pass [PropagateDeadline] to additionally expose the budget as a hard,
// clock-driven [context.Context] deadline that downstream callees observe and
// that cancels an in-flight attempt when the budget expires.
func WithTimeBudget(budget time.Duration, opts ...TimeBudgetOption) Option {
	var cfg timeBudgetConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return optionFunc(func(s *policySetup) {
		s.timeBudget = &budget
		s.propagateDeadline = cfg.propagateDeadline
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

// WithConcurrencyBudget adds a concurrency budget that caps how many retries and
// hedges may be in flight at once, as a fraction of live traffic with a floor
// (see [ConcurrencyBudget] and the [MaxRate] and [MinConcurrency] options). It is
// the concurrency-dimension complement of [WithRetryBudget] (which throttles the
// retry RATE over time): the two compose. It requires [WithRetry] or [WithHedge];
// a policy configured with a budget but neither panics in NewPolicy. To bound
// retries across several policies, share one budget via [NewConcurrencyBudget]
// and [WithSharedConcurrencyBudget] instead.
func WithConcurrencyBudget(opts ...ConcurrencyBudgetOption) Option {
	return optionFunc(func(s *policySetup) {
		s.concurrencyBudget = NewConcurrencyBudget(opts...)
	})
}

// WithSharedConcurrencyBudget attaches an existing ConcurrencyBudget, letting
// several policies share one ceiling so their retries and hedges are bounded
// process-wide. Like WithConcurrencyBudget it requires WithRetry or WithHedge. A
// nil budget is ignored.
func WithSharedConcurrencyBudget(budget *ConcurrencyBudget) Option {
	return optionFunc(func(s *policySetup) {
		s.concurrencyBudget = budget
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
// in use. By default rejection is immediate ([ErrBulkheadFull]); pass
// [BulkheadMaxWait] (and optionally [BulkheadQueueDepth]) to make a full bulkhead
// queue callers for a bounded time instead.
func WithBulkhead(maxConcurrent int, opts ...BulkheadOption) Option {
	return optionFunc(func(s *policySetup) {
		s.bulkhead = &bulkheadDesc{maxConcurrent: maxConcurrent, opts: opts}
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

// WithAdaptiveThrottle adds a Google-SRE client-side adaptive throttler: a
// probabilistic load shedder that rejects calls locally, with [ErrThrottled],
// in proportion to how heavily the backend is already rejecting them (see
// [Throttler]). It keeps a sliding window of requests vs. backend-accepted
// requests and, once requests exceed [OverloadRatio] times accepts, sheds new
// calls with a probability capped by [MaxRejectionRate]. See also [MinRequests],
// [ThrottleWindow], and [ThrottleClassifier].
//
// It sits just outside the circuit breaker in the chain, so it dampens load
// gradually and proportionally before the breaker's binary trip — ideally
// easing a recovering backend back to health without the breaker opening at all.
// A shed call never reaches the inner patterns, so it does not count against the
// circuit breaker.
//
// By default every error returned by the inner chain counts as a backend
// rejection — including the resilience stack's OWN admission errors
// ([ErrCircuitOpen], [ErrRateLimited], [ErrBulkheadFull], [ErrConcurrencyLimited]),
// since the throttler sits outside those stages and only sees their returned
// error. Two consequences to be aware of when combining the throttler with a
// breaker or limiter: a saturated rate limiter or bulkhead inflates the apparent
// backend-distress signal even when the backend is healthy, and after a breaker
// opens the window holds those rejections, so the throttler can keep shedding
// briefly into the breaker's half-open recovery. Both are self-correcting — the
// window decays within one [ThrottleWindow] (default 10s) and [MaxRejectionRate]
// keeps a fraction of traffic probing — but to react only to genuine backend
// outcomes, pass a [ThrottleClassifier] that excludes those sentinels (and any
// non-overload application error such as a 404).
//
// The shedding probability is observable via the OnThrottled hook, the Throttled
// counter, and the ThrottleProbability gauge, and surfaces as the degraded
// health condition [ConditionThrottling] while it sheds.
func WithAdaptiveThrottle(opts ...ThrottleOption) Option {
	return optionFunc(func(s *policySetup) {
		s.throttle = &throttleDesc{opts: opts}
	})
}

// WithHedge adds a hedged request that fires a second concurrent call after
// delay.
func WithHedge(delay time.Duration) Option {
	return optionFunc(func(s *policySetup) {
		s.hedge = &delay
	})
}

// WithRecover adds panic recovery: if the user function (or any inner pattern)
// panics, the panic is caught and returned as a *[PanicError] instead of
// crashing the process. The recovered value, a goroutine stack trace, and the
// [Hooks.OnPanic] hook are all available to callers. Use errors.Is(err,
// [ErrPanic]) to detect it and errors.As to inspect the full *[PanicError].
//
// Recovery sits innermost in the chain (priority > hedge), so each goroutine
// spawned by hedge also gets its own recovery wrapper. When paired with
// [WithRetry], a recovered panic becomes an error that retry can retry — useful
// for intermittent panics caused by race conditions or nil-pointer bugs that a
// retry might avoid. Pair it with [WithFallback] to return a safe default on
// unrecoverable panics.
func WithRecover() Option {
	return optionFunc(func(s *policySetup) {
		s.panicRecover = true
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

// WithCache adds a read-through result cache (see [ReadThroughCache]): keyFn
// derives a cache key from the call's context — stamp request identity into ctx
// upstream and read it back here, exactly as [WithCoalesce] does, so one keyFn
// can drive both. A fresh hit returns the cached value and skips the rest of the
// chain entirely; a miss executes the chain and caches a successful result for
// ttl. Returning an empty key opts a call out of caching.
//
// The cache sits just inside the fallback layer and outside every other pattern,
// so a hit avoids even coalescing, and a fallback value is never cached (only a
// genuine downstream success is). Pair it with [WithCoalesce] to collapse the
// burst of concurrent misses on a hot key into one downstream call.
//
// The underlying [Cache] is parameterised by [CacheEntry], e.g.
// otter.MustNew[string, r8e.CacheEntry[T]](cfg). Configure stale-if-error and
// negative caching with [StaleIfError] and [NegativeCache], and bust a single
// call's cache read with [ForceRefresh]. Because the cache and keyFn are code,
// caching is code-only — it is deliberately absent from [PolicyConfig],
// [BuildOptions], and [Policy.Reconfigure], like [WithCoalesce].
//
// A nil keyFn, a nil cache, or a non-positive ttl are misconfigurations:
// [NewPolicy] panics with [ErrCacheNilKeyFunc], [ErrCacheNilCache], or
// [ErrCacheNonPositiveTTL] respectively.
func WithCache[T any](
	cache Cache[string, CacheEntry[T]],
	keyFn func(context.Context) string,
	ttl time.Duration,
	opts ...CacheOption,
) Option {
	return optionFunc(func(s *policySetup) {
		s.cache = &cacheDesc{cache: cache, keyFn: keyFn, ttl: ttl, opts: opts}
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

	validateSetup(&setup)

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
		throttler      *Throttler
		coalescer       *Coalescer[T]
		timeoutCell     *atomic.Int64
		adaptiveTimeout *adaptiveTimeout
		timeBudgetCell  *atomic.Pointer[timeBudgetState]
		hedgeCell      *atomic.Int64
		retryCell      *atomic.Pointer[retryRuntime]
	)

	if setup.timeout != nil {
		timeoutCell = new(atomic.Int64)
		timeoutCell.Store(int64(*setup.timeout))

		if setup.timeoutAdaptive != nil {
			adaptiveTimeout = newAdaptiveTimeout(setup.timeoutAdaptive, clock)
			entries = append(
				entries,
				newAdaptiveTimeoutEntry[T](timeoutCell, adaptiveTimeout, &hooks),
			)
		} else {
			entries = append(entries, newTimeoutEntry[T](timeoutCell, &hooks))
		}
	}

	if setup.timeBudget != nil {
		timeBudgetCell = new(atomic.Pointer[timeBudgetState])
		timeBudgetCell.Store(&timeBudgetState{
			budget:            *setup.timeBudget,
			propagateDeadline: setup.propagateDeadline,
		})
		entries = append(
			entries,
			newTimeBudgetEntry[T](timeBudgetCell, clock, &hooks),
		)
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
			newRetryEntry[T](retryCell, &hooks, clock, retryBudgets{
				retry:       setup.retryBudget,
				concurrency: setup.concurrencyBudget,
			}),
		)
	}

	if setup.concurrencyBudget != nil {
		entries = append(
			entries,
			newConcurrencyBudgetEntry[T](setup.concurrencyBudget),
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
		bulkhead = NewBulkhead(setup.bulkhead.maxConcurrent, clock, &hooks, setup.bulkhead.opts...)
		entries = append(entries, newBulkheadEntry[T](bulkhead))
	}

	if setup.adaptive != nil {
		adaptive = NewAdaptiveLimiter(clock, &hooks, setup.adaptive.opts...)
		entries = append(entries, newAdaptiveEntry[T](adaptive))
	}

	if setup.throttle != nil {
		throttler = NewThrottler(clock, &hooks, setup.throttle.opts...)
		entries = append(entries, newThrottleEntry[T](throttler))
	}

	if setup.hedge != nil {
		hedgeCell = new(atomic.Int64)
		hedgeCell.Store(int64(*setup.hedge))
		entries = append(
			entries,
			newHedgeEntry[T](hedgeCell, &hooks, clock, setup.concurrencyBudget),
		)
	}

	if setup.panicRecover {
		entries = append(entries, newRecoverEntry[T](&hooks))
	}

	if setup.cache != nil {
		entries = append(entries, newCacheEntry[T](setup.cache, clock, &hooks))
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
		name:              name,
		chain:             chain,
		circuitBreaker:    circuitBreaker,
		rateLimiter:       rateLimiter,
		bulkhead:          bulkhead,
		adaptive:          adaptive,
		throttler:         throttler,
		retryBudget:       setup.retryBudget,
		concurrencyBudget: setup.concurrencyBudget,
		coalescer:         coalescer,
		metrics:           metrics,
		clock:             clock,
		latency:           newLatencyWindow(clock),
		adaptiveTimeout:   adaptiveTimeout,
		timeout:           timeoutCell,
		timeBudget:        timeBudgetCell,
		hedge:             hedgeCell,
		retry:             retryCell,
		deps:              setup.deps,
		affectsReadiness:  setup.affectsReadiness,
		registry:          reg,
	}

	if reg != nil {
		reg.Register(policy)
	}

	return policy
}

// validateSetup panics on a self-contradictory policy configuration — the same
// misconfigurations [BuildOptions] rejects with an error for the config-driven
// path. It runs once before any pattern is constructed.
func validateSetup(setup *policySetup) {
	if err := checkSetupInvariants(setup); err != nil {
		panic(err)
	}
}

// checkSetupInvariants returns the first cross-pattern misconfiguration in
// setup, or nil if it is self-consistent. It is the single source of truth for
// these rules: [validateSetup] panics with it (the options path) and
// [BuildOptions] returns it (the config path), so an invariant added here is
// enforced by both paths and the config path can never panic on one the options
// path catches.
func checkSetupInvariants(setup *policySetup) error {
	// A retry budget gates retries; without a retry pattern it has nothing to do.
	if setup.retryBudget != nil && setup.retry == nil {
		return ErrRetryBudgetWithoutRetry
	}

	// A concurrency budget gates retries and hedges; with neither it would do
	// nothing.
	if setup.concurrencyBudget != nil &&
		setup.retry == nil && setup.hedge == nil {
		return ErrConcurrencyBudgetWithoutConsumer
	}

	if setup.coalesce != nil {
		// Coalescing cannot group calls without a key function, and its detached
		// shared call needs a timeout to bound it (see WithCoalesce).
		if setup.coalesce.keyFn == nil {
			return ErrCoalesceNilKeyFunc
		}

		if setup.timeout == nil {
			return ErrCoalesceWithoutTimeout
		}
	}

	if setup.cache != nil {
		// The cache cannot key calls without a key function, has nothing to back
		// it without a cache, and could never serve a hit with a non-positive TTL.
		if setup.cache.keyFn == nil {
			return ErrCacheNilKeyFunc
		}

		if setup.cache.cache == nil {
			return ErrCacheNilCache
		}

		if setup.cache.ttl <= 0 {
			return ErrCacheNonPositiveTTL
		}
	}

	// The bulkhead and the adaptive limiter both drive the concurrency slot;
	// configuring both is contradictory.
	if setup.bulkhead != nil && setup.adaptive != nil {
		return ErrConcurrencyLimiterConflict
	}

	// A time budget only gates retry and hedge; with neither it would do nothing.
	if setup.timeBudget != nil && setup.retry == nil && setup.hedge == nil {
		return ErrTimeBudgetWithoutConsumer
	}

	return nil
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

// newAdaptiveTimeoutEntry builds the timeout middleware in adaptive mode: the
// per-call timeout is at.compute(ceiling) where ceiling is the reloadable cell,
// and a successful call's elapsed time (measured on the policy clock) is recorded
// back into the controller's percentile window.
func newAdaptiveTimeoutEntry[T any](
	cell *atomic.Int64,
	at *adaptiveTimeout,
	hooks *Hooks,
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityTimeout,
		Name:     "timeout",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				ceiling := time.Duration(cell.Load())
				start := at.clock.Now()
				result, err := DoTimeout[T](ctx, at.compute(ceiling), next, hooks)
				at.record(at.clock.Since(start), err)

				return result, err
			}
		},
	}
}

func newTimeBudgetEntry[T any](
	cell *atomic.Pointer[timeBudgetState],
	clock Clock,
	hooks *Hooks,
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityTimeBudget,
		Name:     "time_budget",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				state := cell.Load()

				// Cooperative-only: attach the budget value retry/hedge consult.
				if !state.propagateDeadline {
					return next(attachTimeBudget(ctx, clock.Now().Add(state.budget)))
				}

				// Hard mode: additionally derive a clock-driven context deadline
				// that downstream callees observe (see PropagateDeadline).
				return applyHardDeadline[T](ctx, next, hardDeadlineParams{
					clock:  clock,
					hooks:  hooks,
					budget: state.budget,
				})
			}
		},
	}
}

func newRetryEntry[T any](
	cell *atomic.Pointer[retryRuntime],
	hooks *Hooks,
	clock Clock,
	budgets retryBudgets,
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
					Budget:      budgets.retry,
					Concurrency: budgets.concurrency,
					Opts:        rt.opts,
				})
			}
		},
	}
}

// newConcurrencyBudgetEntry builds the middleware that scopes the in-flight
// execution count the concurrency budget uses as its denominator. It performs no
// admission of its own — every call that reaches it is counted in, and out on
// return — while the actual gating happens inside retry and hedge (see
// [ConcurrencyBudget]). It sits just outside retry so its scope spans both retry
// and hedge.
func newConcurrencyBudgetEntry[T any](budget *ConcurrencyBudget) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityConcurrencyBudget,
		Name:     "concurrency_budget",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				budget.enter()
				defer budget.exit()

				return next(ctx)
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

				// Measure latency so the breaker can evaluate its slow-call rate
				// (a no-op cost when slow-call detection is off). The span covers
				// the work the breaker wraps — including inner retry/hedge — the
				// same granularity at which it records success and failure.
				start := cb.clock.Now()
				val, err := next(ctx)
				cb.Record(cb.clock.Since(start), err)

				return val, err //nolint:wrapcheck // caller's error returned as-is
			}
		},
	}
}

// admitRecordEntry builds an admit → next → record middleware: a guard that may
// reject before the inner work runs, and a record callback fed the outcome
// afterwards. The rate limiter (AIMD adaptation) and the adaptive throttler share
// this shape; keeping it in one place avoids two near-identical entry builders.
func admitRecordEntry[T any](
	priority int,
	name string,
	admit func(context.Context) error,
	record func(error),
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priority,
		Name:     name,
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if err := admit(ctx); err != nil {
					var zero T

					return zero, err //nolint:wrapcheck // admission error returned as-is
				}

				val, err := next(ctx)
				record(err)

				return val, err //nolint:wrapcheck // caller's error returned as-is
			}
		},
	}
}

func newRateLimiterEntry[T any](rl *RateLimiter) PatternEntry[T] {
	return admitRecordEntry[T](
		priorityRateLimiter, "rate_limiter", rl.Allow, rl.RecordOutcome,
	)
}

func newBulkheadEntry[T any](bh *Bulkhead) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityBulkhead,
		Name:     "bulkhead",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				if err := bh.Acquire(ctx); err != nil {
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

func newThrottleEntry[T any](throttler *Throttler) PatternEntry[T] {
	return admitRecordEntry[T](
		priorityThrottle, "adaptive_throttle", throttler.Allow, throttler.Record,
	)
}

func newHedgeEntry[T any](
	cell *atomic.Int64,
	hooks *Hooks,
	clock Clock,
	budget *ConcurrencyBudget,
) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityHedge,
		Name:     "hedge",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoHedge[T](ctx, next, HedgeParams{
					Delay:  time.Duration(cell.Load()),
					Hooks:  hooks,
					Clock:  clock,
					Budget: budget,
				})
			}
		},
	}
}

// newCacheEntry builds the read-through-cache middleware. It asserts the erased
// cache back to the policy's concrete Cache[string, CacheEntry[T]]; a mismatch
// is a programmer error (a cache typed for a different T than the policy), so it
// panics with a clear message, mirroring the fallback entries.
func newCacheEntry[T any](
	desc *cacheDesc,
	clock Clock,
	hooks *Hooks,
) PatternEntry[T] {
	cache, ok := desc.cache.(Cache[string, CacheEntry[T]])
	if !ok {
		var zero T

		panic(fmt.Sprintf(
			"r8e: WithCache value has type %T, which does not match policy "+
				"result type Cache[string, CacheEntry[%T]]",
			desc.cache, zero,
		))
	}

	opts := append([]CacheOption{CacheClock(clock), CacheHooks(hooks)}, desc.opts...)
	rc := NewReadThroughCache[T](cache, desc.ttl, opts...)

	return PatternEntry[T]{
		Priority: priorityCache,
		Name:     "cache",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return rc.Do(ctx, desc.keyFn(ctx), next)
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
