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
		// TimeBudget is the total time budget shared across retry and hedge.
		// Optional. Parsed via time.ParseDuration. Example: "5s".
		TimeBudget *string `json:"time_budget,omitempty" yaml:"time_budget,omitempty"`
		// Hedge is the delay before launching a hedged request.
		// Optional. Parsed via time.ParseDuration. Example: "200ms".
		Hedge *string `json:"hedge,omitempty" yaml:"hedge,omitempty"`
		// RateLimit is the maximum requests per second.
		// Optional. Example: 100.
		RateLimit *float64 `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
		// Bulkhead is the maximum concurrent requests.
		// Optional. Example: 10.
		Bulkhead *int `json:"bulkhead,omitempty" yaml:"bulkhead,omitempty"`
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
)

// BuildOptions converts a [PolicyConfig] into a slice of functional
// option values suitable for [NewPolicy]. Use this when you embed
// [PolicyConfig] in your own config struct and want to build a
// policy without going through [LoadConfig].
func BuildOptions(pc *PolicyConfig) ([]Option, error) {
	var opts []Option

	if pc.Timeout != nil {
		d, err := time.ParseDuration(*pc.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}

		opts = append(opts, WithTimeout(d))
	}

	if pc.TimeBudget != nil {
		// The budget gates only retry and hedge; without one it would panic in
		// NewPolicy. Surface the misconfiguration as an error here instead.
		if pc.Retry == nil && pc.Hedge == nil {
			return nil, fmt.Errorf("time_budget: %w", ErrTimeBudgetWithoutConsumer)
		}

		d, err := time.ParseDuration(*pc.TimeBudget)
		if err != nil {
			return nil, fmt.Errorf("time_budget: %w", err)
		}

		opts = append(opts, WithTimeBudget(d))
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
		opts = append(opts, WithRateLimit(*pc.RateLimit))
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
		opts = append(opts, WithBulkhead(*pc.Bulkhead))
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
		d, err := time.ParseDuration(*pc.Hedge)
		if err != nil {
			return nil, fmt.Errorf("hedge: %w", err)
		}

		opts = append(opts, WithHedge(d))
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

	maxAttempts := 0
	if cfg.MaxAttempts != nil {
		maxAttempts = *cfg.MaxAttempts
	}

	return &retryRuntime{
		strategy:    strategy,
		opts:        opts,
		maxAttempts: maxAttempts,
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
