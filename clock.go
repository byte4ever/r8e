package r8e

import "time"

// Clock abstracts time operations so that resilience patterns can be tested
// deterministically. Production code uses [RealClock]; tests may substitute a
// fake implementation to control the passage of time.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Since returns the duration elapsed since t.
	Since(t time.Time) time.Duration
	// NewTimer creates a new [Timer] that will fire after duration d.
	NewTimer(d time.Duration) Timer
}

// Timer abstracts [time.Timer] so that fake clocks can provide controllable
// timers for deterministic testing of timeout and backoff logic.
type Timer interface {
	// C returns the channel on which the timer's firing time is delivered.
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it was stopped
	// before it fired.
	Stop() bool
	// Reset changes the timer to fire after duration d and reports whether the
	// timer had been active before the reset.
	Reset(d time.Duration) bool
}

// RealClock is a zero-value [Clock] backed by the real [time] package.
// It is safe for concurrent use because it holds no mutable state.
type RealClock struct{}

// Now returns the current wall-clock time via [time.Now].
func (RealClock) Now() time.Time { return time.Now() }

// Since returns the time elapsed since t via [time.Since].
func (RealClock) Since(t time.Time) time.Duration { return time.Since(t) }

// NewTimer creates a real [Timer] that fires after d via [time.NewTimer].
func (RealClock) NewTimer(d time.Duration) Timer {
	return &realTimer{inner: time.NewTimer(d)}
}

// realTimer wraps [time.Timer] to satisfy the [Timer] interface.
type realTimer struct {
	inner *time.Timer
}

func (t *realTimer) C() <-chan time.Time        { return t.inner.C }
func (t *realTimer) Stop() bool                 { return t.inner.Stop() }
func (t *realTimer) Reset(d time.Duration) bool { return t.inner.Reset(d) }
