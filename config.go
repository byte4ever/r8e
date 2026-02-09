package r8e

import (
	"fmt"
	"os"
	"time"

	json "github.com/goccy/go-json"
)

type (
	// configFile is the top-level JSON structure.
	configFile struct {
		Policies map[string]PolicyConfig `json:"policies"`
	}

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
)

// LoadConfig reads a JSON configuration file and stores the policy
// configurations in a [Registry]. Actual [Policy] instances are not created
// until [GetPolicy] is called, allowing the caller to provide type
// parameters and additional code-level options.
//
// Duration values (timeout, recovery_timeout, base_delay, max_delay,
// hedge) are parsed using [time.ParseDuration].
//
// Supported backoff strategies: "constant", "exponential", "linear",
// "exponential_jitter".
func LoadConfig(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("r8e: read config: %w", err)
	}

	var cfg configFile
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("r8e: parse config: %w", err)
	}

	// Validate all policies eagerly so errors surface at load time.
	for name, pc := range cfg.Policies {
		if _, buildErr := BuildOptions(&pc); buildErr != nil {
			return nil, fmt.Errorf("r8e: policy %q: %w", name, buildErr)
		}
	}

	reg := NewRegistry()
	reg.mu.Lock()
	reg.configs = cfg.Policies
	reg.mu.Unlock()

	return reg, nil
}

// BuildOptions converts a [PolicyConfig] into a slice of functional
// option values suitable for [NewPolicy]. Use this when you embed
// [PolicyConfig] in your own config struct and want to build a
// policy without going through [LoadConfig].
func BuildOptions(pc *PolicyConfig) ([]any, error) {
	var opts []any

	if pc.Timeout != nil {
		d, err := time.ParseDuration(*pc.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}

		opts = append(opts, WithTimeout(d))
	}

	if pc.CircuitBreaker != nil {
		var cbOpts []CircuitBreakerOption

		if pc.CircuitBreaker.FailureThreshold != nil {
			cbOpts = append(
				cbOpts,
				FailureThreshold(
					*pc.CircuitBreaker.FailureThreshold,
				),
			)
		}

		if pc.CircuitBreaker.RecoveryTimeout != nil {
			recoveryDur, err := time.ParseDuration(
				*pc.CircuitBreaker.RecoveryTimeout,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"circuit_breaker.recovery_timeout: %w",
					err,
				)
			}

			cbOpts = append(
				cbOpts,
				RecoveryTimeout(recoveryDur),
			)
		}

		if pc.CircuitBreaker.HalfOpenMaxAttempts != nil {
			cbOpts = append(
				cbOpts,
				HalfOpenMaxAttempts(
					*pc.CircuitBreaker.HalfOpenMaxAttempts,
				),
			)
		}

		opts = append(opts, WithCircuitBreaker(cbOpts...))
	}

	if pc.Retry != nil {
		strategy, err := parseBackoffStrategy(
			pc.Retry.Backoff,
			pc.Retry.BaseDelay,
		)
		if err != nil {
			return nil, fmt.Errorf("retry: %w", err)
		}

		var retryOpts []RetryOption

		if pc.Retry.MaxDelay != nil {
			maxDel, maxDelErr := time.ParseDuration(
				*pc.Retry.MaxDelay,
			)
			if maxDelErr != nil {
				return nil, fmt.Errorf(
					"retry.max_delay: %w",
					maxDelErr,
				)
			}

			retryOpts = append(retryOpts, MaxDelay(maxDel))
		}

		// MaxAttempts defaults to 0 if not set.
		maxAttempts := 0
		if pc.Retry.MaxAttempts != nil {
			maxAttempts = *pc.Retry.MaxAttempts
		}

		opts = append(
			opts,
			WithRetry(maxAttempts, strategy, retryOpts...),
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

	return opts, nil
}

// parseBackoffStrategy maps a backoff name + base delay to a
// BackoffStrategy. Both fields are required pointers; nil values
// produce an error.
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

// GetPolicy retrieves a named policy configuration from a config-loaded
// [Registry] and returns a typed [Policy] ready for use with [Policy.Do].
// If the name is not found in the stored configs, a bare policy is created
// with only the provided opts.
//
// Additional options can be provided to augment or override the
// config-loaded settings (e.g., adding hooks, a custom clock, or fallbacks).
// User-provided options are applied after config options, so they take
// precedence.
func GetPolicy[T any](reg *Registry, name string, opts ...any) *Policy[T] {
	reg.mu.Lock()
	pc, ok := reg.configs[name]
	reg.mu.Unlock()

	var allOpts []any

	allOpts = append(allOpts, WithRegistry(reg))

	if ok {
		configOpts, err := BuildOptions(&pc)
		if err == nil {
			allOpts = append(allOpts, configOpts...)
		}
	}

	// User opts come last so they can override config values.
	allOpts = append(allOpts, opts...)

	return NewPolicy[T](name, allOpts...)
}
