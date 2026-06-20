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
		// Hedge is the delay before launching a hedged request.
		// Optional. Parsed via time.ParseDuration. Example: "200ms".
		Hedge *string `json:"hedge,omitempty" yaml:"hedge,omitempty"`
		// RateLimit is the maximum requests per second.
		// Optional. Example: 100.
		RateLimit *float64 `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
		// Bulkhead is the maximum concurrent requests.
		// Optional. Example: 10.
		Bulkhead *int `json:"bulkhead,omitempty" yaml:"bulkhead,omitempty"`
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

	if pc.Bulkhead != nil {
		opts = append(opts, WithBulkhead(*pc.Bulkhead))
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

