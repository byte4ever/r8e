package r8e

import (
	"fmt"
	"time"
)

type (
	// PolicyConfig holds the decoded configuration for a single
	// resilience policy. Export it to embed in your own app config
	// structs for JSON or YAML unmarshaling, then call [BuildOptions]
	// to obtain functional options for [NewPolicy].
	PolicyConfig struct {
		// CircuitBreaker configures the circuit breaker pattern.
		// Optional. Example: {"failure_threshold": 5}.
		CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty" yaml:"circuit_breaker,omitempty"`
		// Retry configures the retry pattern.
		// Optional. Example: {"max_attempts": 3, "backoff": "exponential"}.
		Retry *RetryConfig `json:"retry,omitempty" yaml:"retry,omitempty"`
		// Timeout is the maximum duration for a single call.
		// Optional. Parsed via time.ParseDuration. Example: "2s".
		Timeout *string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
		// AdaptiveTimeout configures percentile-driven adaptive timeout (see
		// [AdaptiveTimeout]). Requires Timeout, which becomes the ceiling and warmup
		// fallback. Optional. Example: {"percentile": 0.99, "multiplier": 2.0}.
		AdaptiveTimeout *AdaptiveTimeoutConfig `json:"adaptive_timeout,omitempty" yaml:"adaptive_timeout,omitempty"`
		// TimeBudget is the total time budget shared across retry and hedge.
		// Optional. Parsed via time.ParseDuration. Example: "5s".
		TimeBudget *string `json:"time_budget,omitempty" yaml:"time_budget,omitempty"`
		// PropagateDeadline, when true, exposes the time budget as a hard,
		// clock-driven context deadline that downstream callees observe and that
		// cancels an in-flight attempt once the budget expires (see
		// [PropagateDeadline]). Optional; requires TimeBudget.
		PropagateDeadline *bool `json:"propagate_deadline,omitempty" yaml:"propagate_deadline,omitempty"`
		// Hedge is the delay before launching a hedged request.
		// Optional. Parsed via time.ParseDuration. Example: "200ms".
		Hedge *string `json:"hedge,omitempty" yaml:"hedge,omitempty"`
		// AdaptiveHedge configures percentile-driven adaptive hedge delay (see
		// [AdaptiveHedge]). Requires Hedge, which becomes the ceiling and warmup
		// fallback. Optional.
		AdaptiveHedge *AdaptiveHedgeConfig `json:"adaptive_hedge,omitempty" yaml:"adaptive_hedge,omitempty"`
		// RateLimit is the maximum requests per second.
		// Optional. Example: 100.
		RateLimit *float64 `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
		// AIMD configures additive-increase / multiplicative-decrease adaptation
		// of the rate limiter (see [AIMD]). Requires RateLimit (the starting and
		// ceiling rate). Optional. Example: {"backoff": 0.9, "interval": "1s"}.
		AIMD *AIMDConfig `json:"aimd,omitempty" yaml:"aimd,omitempty"`
		// Bulkhead is the maximum concurrent requests.
		// Optional. Example: 10.
		Bulkhead *int `json:"bulkhead,omitempty" yaml:"bulkhead,omitempty"`
		// BulkheadMaxWait enables the bounded FIFO wait: a full bulkhead queues
		// callers for up to this duration instead of rejecting immediately.
		// Optional; requires Bulkhead. Parsed via time.ParseDuration. Example:
		// "50ms".
		BulkheadMaxWait *string `json:"bulkhead_max_wait,omitempty" yaml:"bulkhead_max_wait,omitempty"`
		// BulkheadQueueDepth caps how many callers may wait at once.
		// Optional; requires Bulkhead and BulkheadMaxWait. Default: the bulkhead
		// max-concurrency. Example: 20.
		BulkheadQueueDepth *int `json:"bulkhead_queue_depth,omitempty" yaml:"bulkhead_queue_depth,omitempty"`
		// AdaptiveConcurrency configures the adaptive concurrency limiter.
		// Mutually exclusive with Bulkhead.
		// Optional. Example: {"initial_limit": 20, "max_limit": 200}.
		AdaptiveConcurrency *AdaptiveConfig `json:"adaptive_concurrency,omitempty" yaml:"adaptive_concurrency,omitempty"`
		// AdaptiveThrottle configures the Google-SRE adaptive throttler.
		// Optional. Example: {"overload_ratio": 2.0, "window": "10s"}.
		AdaptiveThrottle *AdaptiveThrottleConfig `json:"adaptive_throttle,omitempty" yaml:"adaptive_throttle,omitempty"`
		// RetryBudget configures the adaptive retry budget. Requires Retry.
		// Optional. Example: {"max_tokens": 10, "token_ratio": 0.1}.
		RetryBudget *RetryBudgetConfig `json:"retry_budget,omitempty" yaml:"retry_budget,omitempty"`
		// ConcurrencyBudget configures the retry/hedge concurrency budget.
		// Requires Retry or Hedge. Optional. Example:
		// {"max_rate": 0.25, "min_concurrency": 5}.
		ConcurrencyBudget *ConcurrencyBudgetConfig `json:"concurrency_budget,omitempty" yaml:"concurrency_budget,omitempty"`
	}

	// CircuitBreakerConfig holds circuit breaker configuration
	// values. Embed it (via [PolicyConfig]) in your own config
	// struct for JSON or YAML unmarshaling.
	CircuitBreakerConfig struct {
		// RecoveryTimeout is the duration the breaker stays open.
		// Optional. Parsed via time.ParseDuration. Example: "30s".
		RecoveryTimeout *string `json:"recovery_timeout,omitempty" yaml:"recovery_timeout,omitempty"`
		// FailureThreshold is the number of failures before opening.
		// Optional. Example: 5.
		FailureThreshold *int `json:"failure_threshold,omitempty" yaml:"failure_threshold,omitempty"`
		// HalfOpenMaxAttempts is the max probes in half-open state.
		// Optional. Example: 2.
		HalfOpenMaxAttempts *int `json:"half_open_max_attempts,omitempty" yaml:"half_open_max_attempts,omitempty"`
		// SlowCallDuration is the latency above which a call counts as slow,
		// enabling slow-call-rate tripping. Optional, but must be paired with
		// SlowCallRateThreshold. Parsed via time.ParseDuration. Example: "2s".
		SlowCallDuration *string `json:"slow_call_duration,omitempty" yaml:"slow_call_duration,omitempty"`
		// SlowCallRateThreshold is the fraction of slow calls (in [0,1]) that
		// opens the breaker. Optional, but must be paired with SlowCallDuration.
		// Example: 0.5.
		SlowCallRateThreshold *float64 `json:"slow_call_rate_threshold,omitempty" yaml:"slow_call_rate_threshold,omitempty"`
		// SlowCallWindow is the count-based slow-call window size.
		// Optional. Default 100. Example: 200.
		SlowCallWindow *int `json:"slow_call_window,omitempty" yaml:"slow_call_window,omitempty"`
		// SlowCallMinCalls is the minimum observed calls before the slow-call
		// rate is evaluated. Optional. Default 10. Example: 20.
		SlowCallMinCalls *int `json:"slow_call_min_calls,omitempty" yaml:"slow_call_min_calls,omitempty"`
		// RecoveryBackoffMultiplier enables exponential backoff on the recovery
		// timeout after consecutive failed half-open probes (opt-in, default 0 =
		// disabled). A factor > 1 is the typical use case. Example: 2.0.
		RecoveryBackoffMultiplier *float64 `json:"recovery_backoff_multiplier,omitempty" yaml:"recovery_backoff_multiplier,omitempty"` //nolint:lll // struct tag cannot be split across lines
		// RecoveryMaxBackoff caps the exponential backoff duration. Optional.
		// Only meaningful when RecoveryBackoffMultiplier is set.
		// Parsed via time.ParseDuration. Example: "60s".
		RecoveryMaxBackoff *string `json:"recovery_max_backoff,omitempty" yaml:"recovery_max_backoff,omitempty"`
	}

	// RetryConfig holds retry configuration values. Embed it
	// (via [PolicyConfig]) in your own config struct for JSON or
	// YAML unmarshaling.
	RetryConfig struct {
		// Backoff is the backoff strategy name.
		// Required. One of: "constant", "exponential",
		// "linear", "exponential_jitter".
		Backoff *string `json:"backoff,omitempty" yaml:"backoff,omitempty"`
		// BaseDelay is the base delay for backoff calculation.
		// Required. Parsed via time.ParseDuration. Example: "100ms".
		BaseDelay *string `json:"base_delay,omitempty" yaml:"base_delay,omitempty"`
		// MaxDelay caps the backoff delay.
		// Optional. Parsed via time.ParseDuration. Example: "30s".
		MaxDelay *string `json:"max_delay,omitempty" yaml:"max_delay,omitempty"`
		// MaxAttempts is the maximum number of retry attempts.
		// Required. Example: 3.
		MaxAttempts *int `json:"max_attempts,omitempty" yaml:"max_attempts,omitempty"`
	}

	// AdaptiveConfig holds adaptive-concurrency configuration values.
	// Embed it (via [PolicyConfig]) in your own config struct for JSON or YAML
	// unmarshaling.
	AdaptiveConfig struct {
		// InitialLimit is the concurrency limit to start from.
		// Optional. Example: 20.
		InitialLimit *int `json:"initial_limit,omitempty" yaml:"initial_limit,omitempty"`
		// MinLimit is the floor the adaptive limit cannot drop below.
		// Optional. Example: 1.
		MinLimit *int `json:"min_limit,omitempty" yaml:"min_limit,omitempty"`
		// MaxLimit is the ceiling the adaptive limit cannot rise above.
		// Optional. Example: 200.
		MaxLimit *int `json:"max_limit,omitempty" yaml:"max_limit,omitempty"`
		// RTTTolerance is the tolerated RTT increase before reducing the limit.
		// Optional. Example: 1.5.
		RTTTolerance *float64 `json:"rtt_tolerance,omitempty" yaml:"rtt_tolerance,omitempty"`
	}

	// AdaptiveThrottleConfig holds adaptive-throttler configuration values.
	// Embed it (via [PolicyConfig]) in your own config struct for JSON or YAML
	// unmarshaling. The error classifier (see [ThrottleClassifier]) is code, so
	// it is not configurable here.
	AdaptiveThrottleConfig struct {
		// OverloadRatio is K, the request/accept gap tolerated before shedding.
		// Optional. Example: 2.0.
		OverloadRatio *float64 `json:"overload_ratio,omitempty" yaml:"overload_ratio,omitempty"`
		// MaxRejectionRate caps the local rejection probability in (0, 1].
		// Optional. Example: 0.9.
		MaxRejectionRate *float64 `json:"max_rejection_rate,omitempty" yaml:"max_rejection_rate,omitempty"`
		// Window is the sliding window over which requests and accepts are summed.
		// Optional. Parsed via time.ParseDuration. Example: "10s".
		Window *string `json:"window,omitempty" yaml:"window,omitempty"`
		// MinRequests is the minimum windowed requests before any call is shed.
		// Optional. Example: 10.
		MinRequests *int `json:"min_requests,omitempty" yaml:"min_requests,omitempty"`
	}

	// AIMDConfig holds the AIMD adaptive-rate-limiter tunables. Embed it (via
	// [PolicyConfig.AIMD]) in your own config struct for JSON or YAML
	// unmarshaling. The overload classifier is code-only (it cannot be expressed
	// declaratively); the default server-overload signal is used unless one is
	// set through the [AIMDClassifier] option.
	AIMDConfig struct {
		// MinRate is the rate floor the adaptation never reduces below.
		// Optional. Default: a tenth of the ceiling rate. Example: 10.
		MinRate *float64 `json:"min_rate,omitempty" yaml:"min_rate,omitempty"`
		// MaxRate is the rate ceiling the adaptation never raises above.
		// Optional. Default: the rate_limit value. Example: 100.
		MaxRate *float64 `json:"max_rate,omitempty" yaml:"max_rate,omitempty"`
		// Backoff is the multiplicative factor applied on overload, in (0, 1).
		// Optional. Default: 0.9. Example: 0.8.
		Backoff *float64 `json:"backoff,omitempty" yaml:"backoff,omitempty"`
		// Increase is the additive step (tokens/sec) added back on a clean
		// interval. Optional. Default: a twentieth of the ceiling rate. Example: 5.
		Increase *float64 `json:"increase,omitempty" yaml:"increase,omitempty"`
		// Interval is the minimum time between two rate adjustments.
		// Optional. Parsed via time.ParseDuration. Default: "1s". Example: "500ms".
		Interval *string `json:"interval,omitempty" yaml:"interval,omitempty"`
	}

	// AdaptiveTimeoutConfig holds the percentile-driven adaptive-timeout tunables.
	// Embed it (via [PolicyConfig.AdaptiveTimeout]) in your own config struct for
	// JSON or YAML unmarshaling. Every field is optional; an omitted or
	// out-of-range value falls back to its default.
	AdaptiveTimeoutConfig struct {
		// Percentile is the latency percentile (in (0, 1]) the timeout is derived
		// from. Optional. Default: 0.99. Example: 0.95.
		Percentile *float64 `json:"percentile,omitempty" yaml:"percentile,omitempty"`
		// Multiplier is the headroom applied to the percentile latency; must be at
		// least 1. Optional. Default: 2.0. Example: 3.0.
		Multiplier *float64 `json:"multiplier,omitempty" yaml:"multiplier,omitempty"`
		// Floor is the lower bound the adaptive timeout is never reduced below.
		// Optional. Parsed via time.ParseDuration. Default: none. Example: "5ms".
		Floor *string `json:"floor,omitempty" yaml:"floor,omitempty"`
		// MinSamples is how many successful calls must be in the window before the
		// adaptive value is used. Optional. Default: 20. Example: 50.
		MinSamples *int `json:"min_samples,omitempty" yaml:"min_samples,omitempty"`
	}

	// AdaptiveHedgeConfig holds the percentile-driven adaptive-hedge tunables. Embed
	// it (via [PolicyConfig.AdaptiveHedge]) in your own config struct for JSON or
	// YAML unmarshaling. Every field is optional; an omitted or out-of-range value
	// falls back to its default.
	AdaptiveHedgeConfig struct {
		// Percentile is the latency percentile (in (0, 1]) the hedge delay is
		// derived from. Optional. Default: 0.95. Example: 0.99.
		Percentile *float64 `json:"percentile,omitempty" yaml:"percentile,omitempty"`
		// Multiplier is the headroom applied to the percentile latency; must be
		// positive. Optional. Default: 1.0. Example: 1.5.
		Multiplier *float64 `json:"multiplier,omitempty" yaml:"multiplier,omitempty"`
		// Floor is the lower bound the adaptive hedge delay is never reduced below.
		// Optional. Parsed via time.ParseDuration. Default: none. Example: "5ms".
		Floor *string `json:"floor,omitempty" yaml:"floor,omitempty"`
		// MinSamples is how many successful primaries must be in the window before
		// the adaptive value is used. Optional. Default: 20. Example: 50.
		MinSamples *int `json:"min_samples,omitempty" yaml:"min_samples,omitempty"`
	}

	// RetryBudgetConfig holds retry-budget configuration values. Embed it
	// (via [PolicyConfig]) in your own config struct for JSON or YAML
	// unmarshaling.
	RetryBudgetConfig struct {
		// MaxTokens is the budget capacity.
		// Optional. Example: 10.
		MaxTokens *int `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
		// TokenRatio is the tokens returned per success.
		// Optional. Example: 0.1.
		TokenRatio *float64 `json:"token_ratio,omitempty" yaml:"token_ratio,omitempty"`
	}

	// ConcurrencyBudgetConfig holds retry/hedge concurrency-budget configuration
	// values. Embed it (via [PolicyConfig]) in your own config struct for JSON or
	// YAML unmarshaling.
	ConcurrencyBudgetConfig struct {
		// MaxRatio is the maximum fraction of in-flight executions that may be
		// retries/hedges. Optional. Example: 0.25.
		MaxRatio *float64 `json:"max_ratio,omitempty" yaml:"max_ratio,omitempty"`
		// MinConcurrency is the floor on concurrent retries/hedges always
		// permitted. Optional. Example: 5.
		MinConcurrency *int `json:"min_concurrency,omitempty" yaml:"min_concurrency,omitempty"`
	}
)

// BuildOptions converts a [PolicyConfig] into a slice of functional
// option values suitable for [NewPolicy]. Use this when you embed
// [PolicyConfig] in your own config struct and want to build a
// policy without going through [LoadConfig].
func BuildOptions(pc *PolicyConfig) ([]Option, error) {
	var opts []Option

	if pc.Timeout != nil {
		timeout, err := time.ParseDuration(*pc.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}

		toOpts, err := timeoutOptionsFromConfig(pc.AdaptiveTimeout)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithTimeout(timeout, toOpts...))
	} else if pc.AdaptiveTimeout != nil {
		// Adaptive timeout has no fixed timeout to adapt — reject the same input
		// cold and hot so BuildOptions and Reconfigure agree.
		return nil, ErrAdaptiveTimeoutWithoutTimeout
	}

	if pc.TimeBudget != nil {
		// The budget gates only retry and hedge; without one it would panic in
		// NewPolicy. Surface the misconfiguration as an error here instead.
		if pc.Retry == nil && pc.Hedge == nil {
			return nil, fmt.Errorf("time_budget: %w", ErrTimeBudgetWithoutConsumer)
		}

		budget, err := time.ParseDuration(*pc.TimeBudget)
		if err != nil {
			return nil, fmt.Errorf("time_budget: %w", err)
		}

		var tbOpts []TimeBudgetOption
		if pc.PropagateDeadline != nil && *pc.PropagateDeadline {
			tbOpts = append(tbOpts, PropagateDeadline())
		}

		opts = append(opts, WithTimeBudget(budget, tbOpts...))
	} else if pc.PropagateDeadline != nil && *pc.PropagateDeadline {
		// Propagation has no budget to derive a deadline from — reject the same
		// input cold and hot so BuildOptions and Reconfigure agree.
		return nil, fmt.Errorf(
			"propagate_deadline: %w",
			ErrDeadlinePropagationWithoutBudget,
		)
	}

	if pc.CircuitBreaker != nil {
		cbOpts, err := cbOptionsFromConfig(pc.CircuitBreaker)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithCircuitBreaker(cbOpts...))
	}

	if pc.Retry != nil {
		rt, err := retryRuntimeFromConfig(pc.Retry)
		if err != nil {
			return nil, err
		}

		opts = append(
			opts,
			WithRetry(rt.maxAttempts, rt.strategy, rt.opts...),
		)
	}

	if pc.RateLimit != nil {
		rlOpts, err := rateLimitOptionsFromConfig(pc)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithRateLimit(*pc.RateLimit, rlOpts...))
	} else if pc.AIMD != nil {
		return nil, ErrAIMDWithoutRateLimit
	}

	if pc.Bulkhead != nil && pc.AdaptiveConcurrency != nil {
		// Both drive the concurrency slot; configuring both would panic in
		// NewPolicy. Reject it here so config-driven misconfiguration surfaces as
		// an error, not a panic.
		return nil, fmt.Errorf(
			"adaptive_concurrency: %w",
			ErrConcurrencyLimiterConflict,
		)
	}

	if pc.Bulkhead != nil {
		bhOpts, err := bulkheadOptionsFromConfig(pc)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithBulkhead(*pc.Bulkhead, bhOpts...))
	} else if pc.BulkheadMaxWait != nil || pc.BulkheadQueueDepth != nil {
		return nil, ErrBulkheadWaitWithoutBulkhead
	}

	if pc.AdaptiveConcurrency != nil {
		opts = append(
			opts,
			WithAdaptiveConcurrency(adaptiveOptionsFromConfig(pc.AdaptiveConcurrency)...),
		)
	}

	if pc.AdaptiveThrottle != nil {
		throttleOpts, err := throttleOptionsFromConfig(pc.AdaptiveThrottle)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithAdaptiveThrottle(throttleOpts...))
	}

	if pc.Hedge != nil {
		delay, err := time.ParseDuration(*pc.Hedge)
		if err != nil {
			return nil, fmt.Errorf("hedge: %w", err)
		}

		hOpts, err := hedgeOptionsFromConfig(pc.AdaptiveHedge)
		if err != nil {
			return nil, err
		}

		opts = append(opts, WithHedge(delay, hOpts...))
	} else if pc.AdaptiveHedge != nil {
		// Adaptive hedge has no fixed hedge to adapt — reject the same input cold
		// and hot so BuildOptions and Reconfigure agree.
		return nil, ErrAdaptiveHedgeWithoutHedge
	}

	if pc.RetryBudget != nil {
		// A retry budget gates retries; a config that asks for one without a
		// retry block would panic in NewPolicy. Reject it here so config-driven
		// misconfiguration surfaces as an error, not a panic.
		if pc.Retry == nil {
			return nil, fmt.Errorf(
				"retry_budget: %w",
				ErrRetryBudgetWithoutRetry,
			)
		}

		opts = append(
			opts,
			WithRetryBudget(retryBudgetOptionsFromConfig(pc.RetryBudget)...),
		)
	}

	if pc.ConcurrencyBudget != nil {
		// The budget gates only retry and hedge; without one it would panic in
		// NewPolicy. Surface the misconfiguration as an error here instead.
		if pc.Retry == nil && pc.Hedge == nil {
			return nil, fmt.Errorf(
				"concurrency_budget: %w",
				ErrConcurrencyBudgetWithoutConsumer,
			)
		}

		opts = append(
			opts,
			WithConcurrencyBudget(
				concurrencyBudgetOptionsFromConfig(pc.ConcurrencyBudget)...,
			),
		)
	}

	// Safety net: apply the assembled options to a probe setup and run the same
	// cross-pattern checks NewPolicy enforces, so the config path returns an
	// error where the options path would panic — including any future invariant
	// added to checkSetupInvariants but not mirrored by an explicit check above.
	var probe policySetup
	for _, opt := range opts {
		opt.apply(&probe)
	}

	if err := checkSetupInvariants(&probe); err != nil {
		return nil, err
	}

	return opts, nil
}

// adaptiveOptionsFromConfig converts an [AdaptiveConfig] into
// adaptive-concurrency options. Shared by [BuildOptions] and
// [Policy.Reconfigure].
func adaptiveOptionsFromConfig(cfg *AdaptiveConfig) []AdaptiveOption {
	var opts []AdaptiveOption

	if cfg.InitialLimit != nil {
		opts = append(opts, InitialLimit(*cfg.InitialLimit))
	}

	if cfg.MinLimit != nil {
		opts = append(opts, MinLimit(*cfg.MinLimit))
	}

	if cfg.MaxLimit != nil {
		opts = append(opts, MaxLimit(*cfg.MaxLimit))
	}

	if cfg.RTTTolerance != nil {
		opts = append(opts, RTTTolerance(*cfg.RTTTolerance))
	}

	return opts
}

// throttleOptionsFromConfig converts an [AdaptiveThrottleConfig] into
// adaptive-throttler options. Shared by [BuildOptions] and [Policy.Reconfigure].
// It returns an error only when the window string fails to parse.
//
//nolint:dupl // structurally mirrors adaptiveTimeoutOptionsFromConfig but maps a
// distinct config type to a distinct option type; no shared abstraction without generics.
func throttleOptionsFromConfig(
	cfg *AdaptiveThrottleConfig,
) ([]ThrottleOption, error) {
	var opts []ThrottleOption

	if cfg.OverloadRatio != nil {
		opts = append(opts, OverloadRatio(*cfg.OverloadRatio))
	}

	if cfg.MaxRejectionRate != nil {
		opts = append(opts, MaxRejectionRate(*cfg.MaxRejectionRate))
	}

	if cfg.Window != nil {
		window, err := time.ParseDuration(*cfg.Window)
		if err != nil {
			return nil, fmt.Errorf("adaptive_throttle.window: %w", err)
		}

		opts = append(opts, ThrottleWindow(window))
	}

	if cfg.MinRequests != nil {
		opts = append(opts, MinRequests(*cfg.MinRequests))
	}

	return opts, nil
}

// timeoutOptionsFromConfig converts an [AdaptiveTimeoutConfig] into [WithTimeout]
// options, wrapping the tunables in an [AdaptiveTimeout] option when a block is
// present. Shared by [BuildOptions]; the caller guarantees pc.Timeout is set
// (adaptive timeout without it is rejected there).
func timeoutOptionsFromConfig(cfg *AdaptiveTimeoutConfig) ([]TimeoutOption, error) {
	if cfg == nil {
		return nil, nil
	}

	atOpts, err := adaptiveTimeoutOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	return []TimeoutOption{AdaptiveTimeout(atOpts...)}, nil
}

// adaptiveTimeoutOptionsFromConfig converts an [AdaptiveTimeoutConfig] into
// [AdaptiveTimeout] options. Shared by [BuildOptions] and [Policy.Reconfigure]. It
// returns an error only when the floor string fails to parse.
//
//nolint:dupl // structurally mirrors throttleOptionsFromConfig but maps a distinct
// config type to a distinct option type; no shared abstraction without generics.
func adaptiveTimeoutOptionsFromConfig(cfg *AdaptiveTimeoutConfig) ([]AdaptiveTimeoutOption, error) {
	var opts []AdaptiveTimeoutOption

	if cfg.Percentile != nil {
		opts = append(opts, AdaptiveTimeoutPercentile(*cfg.Percentile))
	}

	if cfg.Multiplier != nil {
		opts = append(opts, AdaptiveTimeoutMultiplier(*cfg.Multiplier))
	}

	if cfg.Floor != nil {
		floor, err := time.ParseDuration(*cfg.Floor)
		if err != nil {
			return nil, fmt.Errorf("adaptive_timeout.floor: %w", err)
		}

		opts = append(opts, AdaptiveTimeoutFloor(floor))
	}

	if cfg.MinSamples != nil {
		opts = append(opts, AdaptiveTimeoutMinSamples(*cfg.MinSamples))
	}

	return opts, nil
}

// hedgeOptionsFromConfig converts an [AdaptiveHedgeConfig] into [WithHedge]
// options, wrapping the tunables in an [AdaptiveHedge] option when a block is
// present. Shared by [BuildOptions]; the caller guarantees pc.Hedge is set
// (adaptive hedge without it is rejected there).
func hedgeOptionsFromConfig(cfg *AdaptiveHedgeConfig) ([]HedgeOption, error) {
	if cfg == nil {
		return nil, nil
	}

	ahOpts, err := adaptiveHedgeOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	return []HedgeOption{AdaptiveHedge(ahOpts...)}, nil
}

// adaptiveHedgeOptionsFromConfig converts an [AdaptiveHedgeConfig] into
// [AdaptiveHedge] options. Shared by [BuildOptions] and [Policy.Reconfigure]. It
// returns an error only when the floor string fails to parse.
//
//nolint:dupl // structurally mirrors adaptiveTimeoutOptionsFromConfig but maps a
// distinct config type to a distinct option type; no shared abstraction without generics.
func adaptiveHedgeOptionsFromConfig(cfg *AdaptiveHedgeConfig) ([]AdaptiveHedgeOption, error) {
	var opts []AdaptiveHedgeOption

	if cfg.Percentile != nil {
		opts = append(opts, AdaptiveHedgePercentile(*cfg.Percentile))
	}

	if cfg.Multiplier != nil {
		opts = append(opts, AdaptiveHedgeMultiplier(*cfg.Multiplier))
	}

	if cfg.Floor != nil {
		floor, err := time.ParseDuration(*cfg.Floor)
		if err != nil {
			return nil, fmt.Errorf("adaptive_hedge.floor: %w", err)
		}

		opts = append(opts, AdaptiveHedgeFloor(floor))
	}

	if cfg.MinSamples != nil {
		opts = append(opts, AdaptiveHedgeMinSamples(*cfg.MinSamples))
	}

	return opts, nil
}

// rateLimitOptionsFromConfig converts the rate-limiter fields of a
// [PolicyConfig] into rate-limiter options, wrapping the AIMD tunables in an
// [AIMD] option when an aimd block is present. Shared by [BuildOptions]; the
// caller guarantees pc.RateLimit is set (AIMD without it is rejected there).
func rateLimitOptionsFromConfig(pc *PolicyConfig) ([]RateLimitOption, error) {
	if pc.AIMD == nil {
		return nil, nil
	}

	aimdOpts, err := aimdOptionsFromConfig(pc.AIMD)
	if err != nil {
		return nil, err
	}

	return []RateLimitOption{AIMD(aimdOpts...)}, nil
}

// aimdOptionsFromConfig converts an [AIMDConfig] into AIMD options. Shared by
// [BuildOptions] and [Policy.Reconfigure]. It returns an error only when the
// interval string fails to parse.
func aimdOptionsFromConfig(cfg *AIMDConfig) ([]AIMDOption, error) {
	var opts []AIMDOption

	if cfg.MinRate != nil {
		opts = append(opts, AIMDMinRate(*cfg.MinRate))
	}

	if cfg.MaxRate != nil {
		opts = append(opts, AIMDMaxRate(*cfg.MaxRate))
	}

	if cfg.Backoff != nil {
		opts = append(opts, AIMDBackoff(*cfg.Backoff))
	}

	if cfg.Increase != nil {
		opts = append(opts, AIMDIncrease(*cfg.Increase))
	}

	if cfg.Interval != nil {
		interval, err := time.ParseDuration(*cfg.Interval)
		if err != nil {
			return nil, fmt.Errorf("aimd.interval: %w", err)
		}

		opts = append(opts, AIMDInterval(interval))
	}

	return opts, nil
}

// retryBudgetOptionsFromConfig converts a [RetryBudgetConfig] into retry-budget
// options. Shared by [BuildOptions] and [Policy.Reconfigure].
func retryBudgetOptionsFromConfig(cfg *RetryBudgetConfig) []RetryBudgetOption {
	var opts []RetryBudgetOption

	if cfg.MaxTokens != nil {
		opts = append(opts, MaxTokens(*cfg.MaxTokens))
	}

	if cfg.TokenRatio != nil {
		opts = append(opts, TokenRatio(*cfg.TokenRatio))
	}

	return opts
}

// concurrencyBudgetOptionsFromConfig converts a [ConcurrencyBudgetConfig] into
// concurrency-budget options. Shared by [BuildOptions] and [Policy.Reconfigure].
func concurrencyBudgetOptionsFromConfig(
	cfg *ConcurrencyBudgetConfig,
) []ConcurrencyBudgetOption {
	var opts []ConcurrencyBudgetOption

	if cfg.MaxRatio != nil {
		opts = append(opts, MaxRatio(*cfg.MaxRatio))
	}

	if cfg.MinConcurrency != nil {
		opts = append(opts, MinConcurrency(*cfg.MinConcurrency))
	}

	return opts
}

// bulkheadOptionsFromConfig converts the bulkhead wait fields of a
// [PolicyConfig] into bulkhead options. Shared by [BuildOptions] and
// [Policy.Reconfigure]. Returns an error if a queue depth is given without a
// max-wait (the queue is only used while waiting).
func bulkheadOptionsFromConfig(pc *PolicyConfig) ([]BulkheadOption, error) {
	var opts []BulkheadOption

	if pc.BulkheadMaxWait != nil {
		d, err := time.ParseDuration(*pc.BulkheadMaxWait)
		if err != nil {
			return nil, fmt.Errorf("bulkhead_max_wait: %w", err)
		}

		opts = append(opts, BulkheadMaxWait(d))
	}

	if pc.BulkheadQueueDepth != nil {
		if pc.BulkheadMaxWait == nil {
			return nil, ErrBulkheadQueueWithoutWait
		}

		opts = append(opts, BulkheadQueueDepth(*pc.BulkheadQueueDepth))
	}

	return opts, nil
}

// cbOptionsFromConfig converts a [CircuitBreakerConfig] into circuit-breaker
// options. Shared by [BuildOptions] and [Policy.Reconfigure].
func cbOptionsFromConfig(cfg *CircuitBreakerConfig) ([]CircuitBreakerOption, error) {
	var opts []CircuitBreakerOption

	if cfg.FailureThreshold != nil {
		opts = append(opts, FailureThreshold(*cfg.FailureThreshold))
	}

	if cfg.RecoveryTimeout != nil {
		recovery, err := time.ParseDuration(*cfg.RecoveryTimeout)
		if err != nil {
			return nil, fmt.Errorf("circuit_breaker.recovery_timeout: %w", err)
		}

		opts = append(opts, RecoveryTimeout(recovery))
	}

	if cfg.HalfOpenMaxAttempts != nil {
		opts = append(opts, HalfOpenMaxAttempts(*cfg.HalfOpenMaxAttempts))
	}

	slowOpts, err := slowCallOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	opts = append(opts, slowOpts...)

	if cfg.RecoveryBackoffMultiplier != nil {
		opts = append(opts, RecoveryBackoffMultiplier(*cfg.RecoveryBackoffMultiplier))
	}

	if cfg.RecoveryMaxBackoff != nil {
		maxBackoffDur, parseErr := time.ParseDuration(*cfg.RecoveryMaxBackoff)
		if parseErr != nil {
			return nil, fmt.Errorf("circuit_breaker.recovery_max_backoff: %w", parseErr)
		}

		opts = append(opts, RecoveryMaxBackoff(maxBackoffDur))
	}

	return opts, nil
}

// slowCallOptionsFromConfig maps the slow-call fields of a [CircuitBreakerConfig]
// to circuit-breaker options. SlowCallDuration and SlowCallRateThreshold enable
// the detector and must be supplied together; the window tuners are independent.
func slowCallOptionsFromConfig(cfg *CircuitBreakerConfig) ([]CircuitBreakerOption, error) {
	var opts []CircuitBreakerOption

	switch {
	case cfg.SlowCallDuration != nil && cfg.SlowCallRateThreshold != nil:
		d, err := time.ParseDuration(*cfg.SlowCallDuration)
		if err != nil {
			return nil, fmt.Errorf("circuit_breaker.slow_call_duration: %w", err)
		}

		opts = append(opts, SlowCallRate(d, *cfg.SlowCallRateThreshold))
	case cfg.SlowCallDuration != nil || cfg.SlowCallRateThreshold != nil:
		return nil, ErrSlowCallConfigIncomplete
	default:
		// Neither paired field set: slow-call detection stays off.
	}

	if cfg.SlowCallWindow != nil {
		opts = append(opts, SlowCallWindow(*cfg.SlowCallWindow))
	}

	if cfg.SlowCallMinCalls != nil {
		opts = append(opts, SlowCallMinCalls(*cfg.SlowCallMinCalls))
	}

	return opts, nil
}

// retryRuntimeFromConfig converts a [RetryConfig] into the runtime retry
// configuration. Shared by [BuildOptions] and [Policy.Reconfigure].
func retryRuntimeFromConfig(cfg *RetryConfig) (*retryRuntime, error) {
	strategy, err := parseBackoffStrategy(cfg.Backoff, cfg.BaseDelay)
	if err != nil {
		return nil, fmt.Errorf("retry: %w", err)
	}

	var opts []RetryOption

	if cfg.MaxDelay != nil {
		maxDelay, parseErr := time.ParseDuration(*cfg.MaxDelay)
		if parseErr != nil {
			return nil, fmt.Errorf("retry.max_delay: %w", parseErr)
		}

		opts = append(opts, MaxDelay(maxDelay))
	}

	// max_attempts is required: a nil value would silently collapse the retry to
	// a single attempt. Checked after parsing so duration/strategy errors win.
	if cfg.MaxAttempts == nil {
		return nil, fmt.Errorf("retry: %w", ErrRetryMaxAttemptsRequired)
	}

	return &retryRuntime{
		strategy:    strategy,
		opts:        opts,
		maxAttempts: *cfg.MaxAttempts,
	}, nil
}

// parseBackoffStrategy maps a backoff name + base delay to a
// BackoffStrategy. Both fields are required pointers; nil values
// produce an error.
//
// Pattern: Factory — selects and constructs the concrete BackoffStrategy
// implementation from a configuration name, hiding the concrete type behind
// the BackoffStrategy interface.
//
//nolint:ireturn // returns interface by design for strategy pattern
func parseBackoffStrategy(
	name, baseDelayStr *string,
) (BackoffStrategy, error) {
	const errCtx = "parsing backoff strategy"

	if name == nil {
		return nil, fmt.Errorf("%s: backoff is required", errCtx)
	}

	if baseDelayStr == nil {
		return nil, fmt.Errorf(
			"%s: base_delay is required",
			errCtx,
		)
	}

	base, err := time.ParseDuration(*baseDelayStr)
	if err != nil {
		return nil, fmt.Errorf("base_delay: %w", err)
	}

	switch *name {
	case "constant":
		return ConstantBackoff(base), nil
	case "exponential":
		return ExponentialBackoff(base), nil
	case "linear":
		return LinearBackoff(base), nil
	case "exponential_jitter":
		return ExponentialJitterBackoff(base), nil
	default:
		return nil, fmt.Errorf(
			"unknown backoff strategy: %q",
			*name,
		)
	}
}
