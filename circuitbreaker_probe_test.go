package r8e

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Half-open probe lease
// ---------------------------------------------------------------------------

func TestCircuitProbeHoldsItsSlotWithoutLease(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)

		time.Sleep(time.Hour)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitHalfOpen, cb.State(), "without a lease the probe keeps its slot")

		probe.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State())
	})
}

func TestCircuitExpiredProbeReopensBreaker(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			opens     atomic.Int32
			underLock atomic.Bool
			cb        *CircuitBreaker
		)

		cb = NewCircuitBreaker(RealClock{}, &Hooks{OnCircuitOpen: func() {
			opens.Add(1)

			// The hook must run after the breaker released its lock.
			if !cb.mu.TryLock() {
				underLock.Store(true)

				return
			}

			cb.mu.Unlock()
		}},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			HalfOpenProbeLease(5*time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		stuck, err := cb.Acquire()
		require.NoError(t, err)

		time.Sleep(5 * time.Second)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitHalfOpen, cb.State(), "the probe holds its slot for the whole lease")

		time.Sleep(time.Millisecond)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State(), "the expired probe counts as a failed probe")
		require.EqualValues(t, 2, opens.Load())
		require.False(t, underLock.Load(), "OnCircuitOpen fired under the breaker's lock")

		time.Sleep(10*time.Second + time.Millisecond)
		probe, err := cb.Acquire()
		require.NoError(t, err, "recovery resumes after the reopen")

		stuck.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitHalfOpen, cb.State(), "the expired probe's late outcome is stale")

		probe.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State())
	})
}

func TestCircuitExpiredProbeGrowsRecoveryBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			RecoveryBackoffMultiplier(2),
			HalfOpenProbeLease(5*time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		_, err := cb.Acquire()
		require.NoError(t, err)

		time.Sleep(5*time.Second + time.Millisecond)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State())

		// One failed probe since the trip: the wait is now 10s × 2^1.
		time.Sleep(20 * time.Second)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)

		time.Sleep(time.Millisecond)
		_, err = cb.Acquire()
		require.NoError(t, err)
		require.Equal(t, CircuitHalfOpen, cb.State())

		// The backoff grew, the lease did not: this probe expires after 5s too.
		time.Sleep(5*time.Second + time.Millisecond)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State())
	})
}

func TestCircuitProbeLeaseWaitsForTheNewestProbe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			HalfOpenMaxAttempts(2),
			HalfOpenProbeLease(10*time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		_, err := cb.Acquire()
		require.NoError(t, err)

		time.Sleep(5 * time.Second)
		_, err = cb.Acquire()
		require.NoError(t, err)

		time.Sleep(5*time.Second + time.Millisecond)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitHalfOpen, cb.State(), "the second probe is still within its lease")

		time.Sleep(5 * time.Second)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State(), "both probes have expired")
	})
}

func TestCircuitProbeLeaseLeavesAFreeSlotToTheNextCall(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			HalfOpenMaxAttempts(2),
			HalfOpenProbeLease(5*time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		_, err := cb.Acquire()
		require.NoError(t, err)

		// The first probe's lease has passed, but a slot is still free: the
		// lease only applies once every slot is taken.
		time.Sleep(5*time.Second + time.Millisecond)
		_, err = cb.Acquire()
		require.NoError(t, err)
		require.Equal(t, CircuitHalfOpen, cb.State())

		// Both slots are now taken and the newest probe is within its lease.
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitHalfOpen, cb.State())
	})
}

func TestCircuitNonPositiveProbeLeaseDisablesIt(t *testing.T) {
	t.Parallel()

	for _, lease := range []time.Duration{0, -time.Second} {
		t.Run(lease.String(), func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				cb := NewCircuitBreaker(RealClock{}, &Hooks{},
					FailureThreshold(1),
					RecoveryTimeout(10*time.Second),
					HalfOpenProbeLease(5*time.Second),
				)
				cb.Reconfigure(HalfOpenProbeLease(lease))
				tripBreaker(t, cb)
				time.Sleep(10*time.Second + time.Millisecond)

				_, err := cb.Acquire()
				require.NoError(t, err)

				time.Sleep(time.Hour)
				_, err = cb.Acquire()
				require.ErrorIs(t, err, ErrCircuitOpen)
				require.Equal(t, CircuitHalfOpen, cb.State(),
					"Reconfigure to a lease <= 0 switches the lease off")
			})
		})
	}
}

func TestCircuitProbeLeaseThroughAllow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var opens atomic.Int32

		cb := NewCircuitBreaker(RealClock{}, &Hooks{OnCircuitOpen: func() { opens.Add(1) }},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			HalfOpenProbeLease(5*time.Second),
		)
		cb.RecordFailure()
		time.Sleep(10*time.Second + time.Millisecond)

		require.NoError(t, cb.Allow())

		time.Sleep(5*time.Second + time.Millisecond)
		require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State())
		require.EqualValues(t, 2, opens.Load(), "the rejected Allow reopened the breaker")

		// Allow does not tie the probe to its admission: its late Record
		// applies to the state current when it arrives, here the next
		// half-open phase.
		time.Sleep(10*time.Second + time.Millisecond)
		require.NoError(t, cb.Allow())

		cb.RecordSuccess()
		require.Equal(t, CircuitClosed, cb.State())
	})
}

func TestPolicyHungProbeDoesNotKeepBreakerHalfOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("", WithRegistry(NewRegistry()),
			WithCircuitBreaker(
				FailureThreshold(1),
				RecoveryTimeout(10*time.Second),
				HalfOpenProbeLease(5*time.Second),
			),
		)

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			return "", errDownstream
		})
		require.ErrorIs(t, err, errDownstream)
		time.Sleep(10*time.Second + time.Millisecond)

		// The probe ignores its context: it only returns once the test
		// releases it, on every exit path of the test, whatever timeout an
		// enclosing policy would set.
		release := make(chan struct{})
		probeDone := make(chan error, 1)

		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()

		go func() {
			_, err := p.Do(context.Background(), func(context.Context) (string, error) {
				<-release

				return "late", nil
			})
			probeDone <- err
		}()

		synctest.Wait()
		time.Sleep(5*time.Second + time.Millisecond)

		_, err = p.Do(t.Context(), func(context.Context) (string, error) { return "unreached", nil })
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, p.circuitBreaker.State())

		time.Sleep(10*time.Second + time.Millisecond)
		val, err := p.Do(t.Context(), func(context.Context) (string, error) { return "fresh", nil })
		require.NoError(t, err)
		assert.Equal(t, "fresh", val)
		require.Equal(t, CircuitClosed, p.circuitBreaker.State())

		close(release)
		released = true

		require.NoError(t, <-probeDone)
		require.Equal(t, CircuitClosed, p.circuitBreaker.State(), "the late success was stale and changed nothing")
	})
}

func TestPolicySlowHealthyProbeClosesBreaker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lease time.Duration
	}{
		{"without a lease", 0},
		{"with a lease above the call latency", 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var opens, closes atomic.Int32

				p := NewPolicy[string]("", WithRegistry(NewRegistry()),
					WithCircuitBreaker(
						FailureThreshold(1),
						RecoveryTimeout(time.Second),
						HalfOpenProbeLease(tt.lease),
					),
					WithHooks(&Hooks{
						OnCircuitOpen:  func() { opens.Add(1) },
						OnCircuitClose: func() { closes.Add(1) },
					}),
				)

				_, err := p.Do(t.Context(), func(context.Context) (string, error) {
					return "", errDownstream
				})
				require.ErrorIs(t, err, errDownstream)

				// A healthy downstream that answers in 1.5s, slower than the
				// recovery wait, under one call every 100ms.
				var calls sync.WaitGroup

				for range 100 {
					calls.Go(func() {
						// The test only reads the breaker's state; calls the
						// open breaker rejects return at once, so their results
						// are dropped.
						_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
							time.Sleep(1500 * time.Millisecond)

							return "ok", nil
						})
					})

					time.Sleep(100 * time.Millisecond)
				}

				assert.Equal(t, CircuitClosed, p.circuitBreaker.State(), "the breaker closed while traffic ran")
				assert.EqualValues(t, 1, opens.Load(), "no reopening after the trip")
				assert.EqualValues(t, 1, closes.Load())

				calls.Wait()
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Probe lease in the config
// ---------------------------------------------------------------------------

func TestHalfOpenProbeLeaseConfig(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		lease := "5s"
		opts, err := cbOptionsFromConfig(&CircuitBreakerConfig{HalfOpenProbeLease: &lease})
		require.NoError(t, err)

		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			append(opts, FailureThreshold(1), RecoveryTimeout(10*time.Second))...,
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		_, err = cb.Acquire()
		require.NoError(t, err)

		time.Sleep(5 * time.Second)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitHalfOpen, cb.State(), "the 5s lease has not passed yet")

		time.Sleep(time.Millisecond)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State())
	})
}

func TestHalfOpenProbeLeaseInvalidDuration(t *testing.T) {
	t.Parallel()

	invalid := "not-a-duration"
	_, err := cbOptionsFromConfig(&CircuitBreakerConfig{HalfOpenProbeLease: &invalid})
	require.ErrorContains(t, err, "circuit_breaker.half_open_probe_lease")
	assert.ErrorContains(t, err, invalid)
}

func TestPolicyReconfigureEnablesProbeLease(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("", WithRegistry(NewRegistry()),
			WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(10*time.Second)),
		)

		lease := "5s"
		require.NoError(t, p.Reconfigure(PolicyConfig{
			CircuitBreaker: &CircuitBreakerConfig{HalfOpenProbeLease: &lease},
		}))

		tripBreaker(t, p.circuitBreaker)
		time.Sleep(10*time.Second + time.Millisecond)

		_, err := p.circuitBreaker.Acquire()
		require.NoError(t, err)

		time.Sleep(5*time.Second + time.Millisecond)
		_, err = p.circuitBreaker.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		require.Equal(t, CircuitOpen, p.circuitBreaker.State())
	})
}

// ---------------------------------------------------------------------------
// Half-open hook panic
// ---------------------------------------------------------------------------

func TestCircuitHalfOpenHookPanicFreesProbeSlot(t *testing.T) {
	t.Parallel()

	admits := []struct {
		admit func(*CircuitBreaker) error
		name  string
	}{
		{name: "Acquire", admit: func(cb *CircuitBreaker) error {
			_, err := cb.Acquire()

			return err
		}},
		{name: "Allow", admit: func(cb *CircuitBreaker) error { return cb.Allow() }},
	}

	for _, tc := range admits {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var halfOpens atomic.Int32

				cb := NewCircuitBreaker(RealClock{}, &Hooks{OnCircuitHalfOpen: func() {
					if halfOpens.Add(1) == 1 {
						panic("hook")
					}
				}},
					FailureThreshold(1),
					RecoveryTimeout(time.Second),
				)
				cb.RecordFailure()
				time.Sleep(time.Second + time.Millisecond)

				// The panic is the outcome under test, so the admission's
				// error is never returned.
				require.PanicsWithValue(t, "hook", func() { _ = tc.admit(cb) })
				require.Equal(t, CircuitHalfOpen, cb.State())

				require.NoError(t, tc.admit(cb), "the panicking admission freed its probe slot")
			})
		})
	}
}

func TestCircuitHalfOpenHookPanicLeavesALaterPhaseAlone(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var halfOpens atomic.Int32

		unpark := make(chan struct{})

		// The parked hook returns on every exit path of the test, so a failed
		// assertion cannot leave it blocked in the bubble.
		unparked := false
		defer func() {
			if !unparked {
				close(unpark)
			}
		}()

		cb := NewCircuitBreaker(RealClock{}, &Hooks{OnCircuitHalfOpen: func() {
			if halfOpens.Add(1) == 1 {
				<-unpark
				panic("hook")
			}
		}},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			HalfOpenProbeLease(5*time.Second),
		)
		cb.RecordFailure()
		time.Sleep(10*time.Second + time.Millisecond)

		recovered := make(chan any, 1)

		go func() {
			defer func() { recovered <- recover() }()

			// The hook's panic is the outcome under test, so the admission's
			// error is never returned.
			_ = cb.Allow()
		}()

		synctest.Wait()

		// The first probe's admission is parked in its hook. Its lease
		// expires, the breaker reopens, and the next phase's probe takes the
		// only slot.
		time.Sleep(5*time.Second + time.Millisecond)
		require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
		require.Equal(t, CircuitOpen, cb.State())

		time.Sleep(10*time.Second + time.Millisecond)
		require.NoError(t, cb.Allow())

		close(unpark)
		unparked = true

		require.Equal(t, "hook", <-recovered)

		require.ErrorIs(t, cb.Allow(), ErrCircuitOpen, "the stale panic did not free the new phase's slot")
		require.Equal(t, CircuitHalfOpen, cb.State())
	})
}

func TestPolicyHalfOpenHookPanicDoesNotKeepBreakerHalfOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var halfOpens atomic.Int32

		p := NewPolicy[string]("", WithRegistry(NewRegistry()),
			WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(time.Second)),
			WithHooks(&Hooks{OnCircuitHalfOpen: func() {
				if halfOpens.Add(1) == 1 {
					panic("hook")
				}
			}}),
		)

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			return "", errDownstream
		})
		require.ErrorIs(t, err, errDownstream)
		time.Sleep(time.Second + time.Millisecond)

		// The panic is the outcome under test, so the call's result is never
		// returned.
		require.PanicsWithValue(t, "hook", func() {
			_, _ = p.Do(t.Context(), func(context.Context) (string, error) { return "unreached", nil })
		})

		val, err := p.Do(t.Context(), func(context.Context) (string, error) { return "probe", nil })
		require.NoError(t, err, "the next call is admitted as the probe")
		assert.Equal(t, "probe", val)
		require.Equal(t, CircuitClosed, p.circuitBreaker.State())
	})
}
