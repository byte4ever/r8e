package r8e

import (
	"errors"
	"fmt"
	"time"
)

// Reconfigurer is implemented by every [Policy]. [Registry.Reconfigure] uses it
// to apply configuration changes to registered policies regardless of their
// type parameter.
type Reconfigurer interface {
	// Name returns the policy's name.
	Name() string
	// Reconfigure applies cfg to the policy's live patterns.
	Reconfigure(cfg PolicyConfig) error
}

var (
	// ErrPatternAbsent is returned (wrapped) by [Policy.Reconfigure] when the
	// new configuration tries to tune a pattern the policy was not built with.
	// Hot-reload retunes existing patterns; it cannot add or remove them —
	// rebuild the policy for structural changes.
	ErrPatternAbsent = errors.New(
		"r8e: cannot reconfigure a pattern the policy does not have",
	)

	// ErrPolicyNotRegistered is returned (wrapped) by [Registry.Reconfigure]
	// when no registered policy matches the given name.
	ErrPolicyNotRegistered = errors.New(
		"r8e: no policy with that name is registered",
	)
)

// Reconfigure applies cfg to the registered policy named name, retuning its
// live patterns (see [Policy.Reconfigure]). It returns an error wrapping
// [ErrPolicyNotRegistered] if no policy with that name is registered.
func (r *Registry) Reconfigure(name string, cfg PolicyConfig) error {
	for _, reporter := range *r.reporters.Load() {
		rc, ok := reporter.(Reconfigurer)
		if ok && rc.Name() == name {
			//nolint:wrapcheck // Reconfigure already returns r8e-prefixed errors
			return rc.Reconfigure(cfg)
		}
	}

	return fmt.Errorf("%w: %q", ErrPolicyNotRegistered, name)
}

// Reconfigure retunes the patterns already present in the policy from cfg at
// runtime, without rebuilding the middleware chain. Every non-nil field of cfg
// is applied to its corresponding live pattern; nil fields leave that pattern
// unchanged. In-flight calls are unaffected.
//
// It returns an error wrapping [ErrPatternAbsent] if cfg specifies a pattern
// the policy does not have, or a parse error for an invalid duration or backoff
// strategy. Patterns are applied in a fixed order; on error, changes already
// applied are kept.
func (p *Policy[T]) Reconfigure(cfg PolicyConfig) error {
	if err := p.reconfigureDurations(cfg); err != nil {
		return err
	}

	if cfg.RateLimit != nil {
		if p.rateLimiter == nil {
			return absentPatternError("rate_limit")
		}

		p.rateLimiter.Reconfigure(*cfg.RateLimit)
	}

	if cfg.Bulkhead != nil {
		if p.bulkhead == nil {
			return absentPatternError("bulkhead")
		}

		p.bulkhead.Reconfigure(*cfg.Bulkhead)
	}

	if cfg.CircuitBreaker != nil {
		if p.circuitBreaker == nil {
			return absentPatternError("circuit_breaker")
		}

		opts, err := cbOptionsFromConfig(cfg.CircuitBreaker)
		if err != nil {
			return fmt.Errorf("r8e: reconfigure: %w", err)
		}

		p.circuitBreaker.Reconfigure(opts...)
	}

	if cfg.Retry != nil {
		if p.retry == nil {
			return absentPatternError("retry")
		}

		runtime, err := retryRuntimeFromConfig(cfg.Retry)
		if err != nil {
			return fmt.Errorf("r8e: reconfigure: %w", err)
		}

		p.retry.Store(runtime)
	}

	return nil
}

// reconfigureDurations applies the timeout and hedge duration cells.
func (p *Policy[T]) reconfigureDurations(cfg PolicyConfig) error {
	if cfg.Timeout != nil {
		if p.timeout == nil {
			return absentPatternError("timeout")
		}

		d, err := time.ParseDuration(*cfg.Timeout)
		if err != nil {
			return fmt.Errorf("r8e: reconfigure timeout: %w", err)
		}

		p.timeout.Store(int64(d))
	}

	if cfg.Hedge != nil {
		if p.hedge == nil {
			return absentPatternError("hedge")
		}

		d, err := time.ParseDuration(*cfg.Hedge)
		if err != nil {
			return fmt.Errorf("r8e: reconfigure hedge: %w", err)
		}

		p.hedge.Store(int64(d))
	}

	return nil
}

func absentPatternError(pattern string) error {
	return fmt.Errorf("%w: %q", ErrPatternAbsent, pattern)
}
