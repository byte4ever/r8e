package r8e

import (
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------.

type (
	circuitBreakerConfig struct {
		failureThreshold    int
		recoveryTimeout     time.Duration
		halfOpenMaxAttempts int
	}

	// CircuitBreakerOption configures a circuit breaker.
	CircuitBreakerOption func(*circuitBreakerConfig)

	// CircuitBreaker tracks the health of a dependency and fails fast when it's
	// down.
	//
	// Pattern: Circuit Breaker — fast-fails calls to unhealthy downstream;
	// auto-recovers via half-open probe after timeout. Lock-free via atomic
	// CAS.
	CircuitBreaker struct {
		clock Clock
		hooks *Hooks
		cfg   circuitBreakerConfig

		state             atomic.Uint32 // stateClosed | stateOpen | stateHalfOpen
		failureCount      atomic.Int64
		lastFailureNano   atomic.Int64 // unix nano of last failure
		halfOpenSuccesses atomic.Int64
	}
)

// Circuit breaker states (stored in atomic.Uint32).
const (
	stateClosed   uint32 = 0
	stateOpen     uint32 = 1
	stateHalfOpen uint32 = 2
)

func defaultCircuitBreakerConfig() circuitBreakerConfig {
	return circuitBreakerConfig{
		failureThreshold:    5,
		recoveryTimeout:     30 * time.Second,
		halfOpenMaxAttempts: 1,
	}
}

// FailureThreshold sets the number of consecutive failures before opening.
func FailureThreshold(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.failureThreshold = n
	}
}

// RecoveryTimeout sets how long to wait in open state before transitioning to
// half-open.
func RecoveryTimeout(d time.Duration) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.recoveryTimeout = d
	}
}

// HalfOpenMaxAttempts sets the number of successful probes needed to close from
// half-open.
func HalfOpenMaxAttempts(n int) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.halfOpenMaxAttempts = n
	}
}

// NewCircuitBreaker creates a circuit breaker with the given options.
func NewCircuitBreaker(
	clock Clock,
	hooks *Hooks,
	opts ...CircuitBreakerOption,
) *CircuitBreaker {
	cfg := defaultCircuitBreakerConfig()
	for _, o := range opts {
		o(&cfg)
	}

	return &CircuitBreaker{
		clock: clock,
		hooks: hooks,
		cfg:   cfg,
	}
}

// Allow checks if a call should be allowed. Returns nil if the breaker is
// closed or half-open. Returns ErrCircuitOpen if the breaker is open and the
// recovery timeout hasn't elapsed.
func (cb *CircuitBreaker) Allow() error {
	s := cb.state.Load()

	if s == stateOpen {
		// Check if recovery timeout has elapsed.
		lastNano := cb.lastFailureNano.Load()

		lastTime := time.Unix(0, lastNano)
		if cb.clock.Since(lastTime) > cb.cfg.recoveryTimeout {
			// Attempt CAS from open to half-open.
			if cb.state.CompareAndSwap(stateOpen, stateHalfOpen) {
				cb.halfOpenSuccesses.Store(0)
				cb.hooks.emitCircuitHalfOpen()
			}
			// Even if CAS failed (another goroutine already transitioned),
			// the state is now half-open, so allow the call.
			return nil
		}

		return ErrCircuitOpen
	}

	// stateClosed or stateHalfOpen: allow the call.
	return nil
}

// RecordSuccess records a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	s := cb.state.Load()

	switch s {
	case stateClosed:
		// Reset failure count on success.
		cb.failureCount.Store(0)

	case stateHalfOpen:
		newCount := cb.halfOpenSuccesses.Add(1)
		if newCount < int64(cb.cfg.halfOpenMaxAttempts) {
			break
		}

		if !cb.state.CompareAndSwap(stateHalfOpen, stateClosed) {
			break
		}

		cb.failureCount.Store(0)
		cb.halfOpenSuccesses.Store(0)
		cb.hooks.emitCircuitClose()

	default:
		// stateOpen — no action on success
	}
}

// RecordFailure records a failed call.
func (cb *CircuitBreaker) RecordFailure() {
	// Record the failure time.
	cb.lastFailureNano.Store(cb.clock.Now().UnixNano())

	s := cb.state.Load()

	switch s {
	case stateClosed:
		newCount := cb.failureCount.Add(1)
		if newCount < int64(cb.cfg.failureThreshold) {
			break
		}

		if !cb.state.CompareAndSwap(stateClosed, stateOpen) {
			break
		}

		cb.hooks.emitCircuitOpen()

	case stateHalfOpen:
		// Any failure in half-open goes back to open.
		if cb.state.CompareAndSwap(stateHalfOpen, stateOpen) {
			cb.halfOpenSuccesses.Store(0)
			cb.hooks.emitCircuitOpen()
		}

	default:
		// stateOpen — already open, no state change needed
	}
}

// State returns the current state as a string: "closed", "open", or
// "half_open".
func (cb *CircuitBreaker) State() string {
	switch cb.state.Load() {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
