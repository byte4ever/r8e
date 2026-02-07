package r8e

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// configFile is the top-level JSON structure.
type configFile struct {
	Policies map[string]policyConfig `json:"policies"`
}

// policyConfig holds the JSON-decoded configuration for a single policy.
type policyConfig struct {
	Timeout        string             `json:"timeout,omitempty"`
	CircuitBreaker *circuitBreakerCfg `json:"circuit_breaker,omitempty"`
	Retry          *retryCfg          `json:"retry,omitempty"`
	RateLimit      float64            `json:"rate_limit,omitempty"`
	Bulkhead       int                `json:"bulkhead,omitempty"`
	StaleCache     string             `json:"stale_cache,omitempty"`
	Hedge          string             `json:"hedge,omitempty"`
}

type circuitBreakerCfg struct {
	FailureThreshold    int    `json:"failure_threshold,omitempty"`
	RecoveryTimeout     string `json:"recovery_timeout,omitempty"`
	HalfOpenMaxAttempts int    `json:"half_open_max_attempts,omitempty"`
}

type retryCfg struct {
	MaxAttempts int    `json:"max_attempts"`
	Backoff     string `json:"backoff"`
	BaseDelay   string `json:"base_delay"`
	MaxDelay    string `json:"max_delay,omitempty"`
}

// LoadConfig reads a JSON configuration file and stores the policy
// configurations in a [Registry]. Actual [Policy] instances are not created
// until [GetPolicy] is called, allowing the caller to provide type
// parameters and additional code-level options.
//
// Duration values (timeout, recovery_timeout, base_delay, max_delay,
// stale_cache, hedge) are parsed using [time.ParseDuration].
//
// Supported backoff strategies: "constant", "exponential", "linear",
// "exponential_jitter".
func LoadConfig(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("r8e: read config: %w", err)
	}

	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("r8e: parse config: %w", err)
	}

	// Validate all policies eagerly so errors surface at load time.
	for name, pc := range cfg.Policies {
		if _, err := buildOptions(pc); err != nil {
			return nil, fmt.Errorf("r8e: policy %q: %w", name, err)
		}
	}

	reg := NewRegistry()
	reg.mu.Lock()
	reg.configs = cfg.Policies
	reg.mu.Unlock()

	return reg, nil
}

// buildOptions converts a policyConfig into a slice of option values.
func buildOptions(pc policyConfig) ([]any, error) {
	var opts []any

	if pc.Timeout != "" {
		d, err := time.ParseDuration(pc.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}
		opts = append(opts, WithTimeout(d))
	}

	if pc.CircuitBreaker != nil {
		var cbOpts []CircuitBreakerOption
		if pc.CircuitBreaker.FailureThreshold > 0 {
			cbOpts = append(cbOpts, FailureThreshold(pc.CircuitBreaker.FailureThreshold))
		}
		if pc.CircuitBreaker.RecoveryTimeout != "" {
			d, err := time.ParseDuration(pc.CircuitBreaker.RecoveryTimeout)
			if err != nil {
				return nil, fmt.Errorf("circuit_breaker.recovery_timeout: %w", err)
			}
			cbOpts = append(cbOpts, RecoveryTimeout(d))
		}
		if pc.CircuitBreaker.HalfOpenMaxAttempts > 0 {
			cbOpts = append(cbOpts, HalfOpenMaxAttempts(pc.CircuitBreaker.HalfOpenMaxAttempts))
		}
		opts = append(opts, WithCircuitBreaker(cbOpts...))
	}

	if pc.Retry != nil {
		strategy, err := parseBackoffStrategy(pc.Retry.Backoff, pc.Retry.BaseDelay)
		if err != nil {
			return nil, fmt.Errorf("retry: %w", err)
		}

		var retryOpts []RetryOption
		if pc.Retry.MaxDelay != "" {
			d, err := time.ParseDuration(pc.Retry.MaxDelay)
			if err != nil {
				return nil, fmt.Errorf("retry.max_delay: %w", err)
			}
			retryOpts = append(retryOpts, MaxDelay(d))
		}

		opts = append(opts, WithRetry(pc.Retry.MaxAttempts, strategy, retryOpts...))
	}

	if pc.RateLimit > 0 {
		opts = append(opts, WithRateLimit(pc.RateLimit))
	}

	if pc.Bulkhead > 0 {
		opts = append(opts, WithBulkhead(pc.Bulkhead))
	}

	if pc.StaleCache != "" {
		d, err := time.ParseDuration(pc.StaleCache)
		if err != nil {
			return nil, fmt.Errorf("stale_cache: %w", err)
		}
		opts = append(opts, WithStaleCache(d))
	}

	if pc.Hedge != "" {
		d, err := time.ParseDuration(pc.Hedge)
		if err != nil {
			return nil, fmt.Errorf("hedge: %w", err)
		}
		opts = append(opts, WithHedge(d))
	}

	return opts, nil
}

// parseBackoffStrategy maps a backoff name + base delay to a BackoffStrategy.
func parseBackoffStrategy(name string, baseDelayStr string) (BackoffStrategy, error) {
	base, err := time.ParseDuration(baseDelayStr)
	if err != nil {
		return nil, fmt.Errorf("base_delay: %w", err)
	}

	switch name {
	case "constant":
		return ConstantBackoff(base), nil
	case "exponential":
		return ExponentialBackoff(base), nil
	case "linear":
		return LinearBackoff(base), nil
	case "exponential_jitter":
		return ExponentialJitterBackoff(base), nil
	default:
		return nil, fmt.Errorf("unknown backoff strategy: %q", name)
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
		configOpts, _ := buildOptions(pc)
		allOpts = append(allOpts, configOpts...)
	}

	// User opts come last so they can override config values.
	allOpts = append(allOpts, opts...)
	return NewPolicy[T](name, allOpts...)
}

// writeFile is a minimal helper used by tests to create temporary JSON files.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
