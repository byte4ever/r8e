package r8e

import (
	"sync"
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
	// auto-recovers via half-open probe after timeout. State transitions are
	// guarded by a mutex so the (state, counters) tuple mutates atomically as a
	// unit — the cheap, linearizable choice the Go concurrency guidance
	// prescribes for a multi-field state machine.
	CircuitBreaker struct {
		clock       Clock
		hooks       *Hooks
		lastFailure time.Time
		cfg         circuitBreakerConfig

		failureCount      int
		halfOpenSuccesses int
		halfOpenInFlight  int // probes currently admitted in half-open
		mu                sync.Mutex
		state             uint32 // stateClosed | stateOpen | stateHalfOpen
	}
)

// Circuit breaker states.
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
// closed, or half-open with a probe slot available. Returns ErrCircuitOpen if
// the breaker is open and the recovery timeout hasn't elapsed, or if half-open
// already has halfOpenMaxAttempts probes in flight.
// The state-transition methods capture the lifecycle hook to fire in a local
// and invoke it AFTER releasing cb.mu, so a user-supplied callback can never
// run inside the critical section (which would deadlock on re-entry or stall
// every caller behind a slow hook).

func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()

	var (
		emit func()
		err  error
	)

	switch cb.state {
	case stateOpen:
		if cb.clock.Since(cb.lastFailure) <= cb.cfg.recoveryTimeout {
			err = ErrCircuitOpen

			break
		}

		// Recovery timeout elapsed: transition to half-open and admit this
		// call as the first probe.
		cb.state = stateHalfOpen
		cb.halfOpenSuccesses = 0
		cb.halfOpenInFlight = 1
		emit = cb.hooks.emitCircuitHalfOpen

	case stateHalfOpen:
		// Admit at most halfOpenMaxAttempts concurrent probes; reject the rest
		// so a recovering downstream is not hit by a thundering herd.
		if cb.halfOpenInFlight >= cb.cfg.halfOpenMaxAttempts {
			err = ErrCircuitOpen

			break
		}

		cb.halfOpenInFlight++

	default:
		// stateClosed: allow the call.
	}

	cb.mu.Unlock()

	if emit != nil {
		emit()
	}

	return err
}

// RecordSuccess records a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()

	var emit func()

	switch cb.state {
	case stateClosed:
		cb.failureCount = 0

	case stateHalfOpen:
		cb.releaseProbe()

		cb.halfOpenSuccesses++
		if cb.halfOpenSuccesses < cb.cfg.halfOpenMaxAttempts {
			break
		}

		cb.state = stateClosed
		cb.failureCount = 0
		cb.halfOpenSuccesses = 0
		cb.halfOpenInFlight = 0
		emit = cb.hooks.emitCircuitClose

	default:
		// stateOpen — no action on success.
	}

	cb.mu.Unlock()

	if emit != nil {
		emit()
	}
}

// RecordFailure records a failed call.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()

	var emit func()

	cb.lastFailure = cb.clock.Now()

	switch cb.state {
	case stateClosed:
		cb.failureCount++
		if cb.failureCount < cb.cfg.failureThreshold {
			break
		}

		cb.state = stateOpen
		emit = cb.hooks.emitCircuitOpen

	case stateHalfOpen:
		// Any failure in half-open reopens the breaker.
		cb.releaseProbe()
		cb.state = stateOpen
		cb.halfOpenSuccesses = 0
		cb.halfOpenInFlight = 0
		emit = cb.hooks.emitCircuitOpen

	default:
		// stateOpen — already open, no state change needed.
	}

	cb.mu.Unlock()

	if emit != nil {
		emit()
	}
}

// releaseProbe decrements the in-flight half-open probe counter, flooring at
// zero so RecordSuccess/RecordFailure calls without a matching Allow (or more
// results than admitted probes) cannot drive it negative. Caller must hold mu.
func (cb *CircuitBreaker) releaseProbe() {
	if cb.halfOpenInFlight > 0 {
		cb.halfOpenInFlight--
	}
}

// State returns the current state as a string: "closed", "open", or
// "half_open".
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
