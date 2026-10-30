package r8e

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outcomeHookCounts counts the neutral and stale outcome hooks, and records
// whether any of them fired while the breaker's lock was held.
type outcomeHookCounts struct {
	cb        *CircuitBreaker
	neutral   atomic.Int32
	stale     atomic.Int32
	underLock atomic.Bool
}

func (c *outcomeHookCounts) hooks() *Hooks {
	return &Hooks{
		OnCircuitNeutral:      func() { c.fired(&c.neutral) },
		OnCircuitStaleOutcome: func() { c.fired(&c.stale) },
	}
}

func (c *outcomeHookCounts) fired(counter *atomic.Int32) {
	counter.Add(1)

	if !c.cb.mu.TryLock() {
		c.underLock.Store(true)

		return
	}

	c.cb.mu.Unlock()
}

func TestCircuitOutcomeHooksFireOncePerAffectedDone(t *testing.T) {
	t.Parallel()

	// Each case leaves the breaker in a state, then reports exactly one
	// outcome; the hooks must count that one report and nothing else.
	tests := []struct {
		report      func(t *testing.T, cb *CircuitBreaker)
		name        string
		wantNeutral int32
		wantStale   int32
	}{
		{
			name: "current success",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				acquire(t, cb).Done(0, OutcomeSuccess)
			},
		},
		{
			name: "current failure",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				acquire(t, cb).Done(0, OutcomeFailure)
			},
		},
		{
			name: "current neutral, closed",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				acquire(t, cb).Done(0, OutcomeNeutral)
			},
			wantNeutral: 1,
		},
		{
			name: "current neutral, half-open probe",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				tripAndWait(t, cb)

				probe := acquire(t, cb)
				require.Equal(t, CircuitHalfOpen, cb.State())
				probe.Done(0, OutcomeNeutral)
			},
			wantNeutral: 1,
		},
		{
			name: "stale success",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				slow := acquire(t, cb)
				tripAndWait(t, cb)
				slow.Done(0, OutcomeSuccess)
			},
			wantStale: 1,
		},
		{
			name: "stale failure",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				slow := acquire(t, cb)
				tripAndWait(t, cb)
				slow.Done(0, OutcomeFailure)
			},
			wantStale: 1,
		},
		{
			name: "stale neutral counts as stale only",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				slow := acquire(t, cb)
				tripAndWait(t, cb)
				slow.Done(0, OutcomeNeutral)
			},
			wantStale: 1,
		},
		{
			name: "a second Done on the same permit",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				permit := acquire(t, cb)
				permit.Done(0, OutcomeNeutral)
				permit.Done(0, OutcomeNeutral)
			},
			wantNeutral: 1,
		},
		{
			name: "the zero permit of a rejection",
			report: func(t *testing.T, cb *CircuitBreaker) {
				t.Helper()
				tripBreaker(t, cb)

				permit, err := cb.Acquire()
				require.ErrorIs(t, err, ErrCircuitOpen)
				permit.Done(0, OutcomeNeutral)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				counts := &outcomeHookCounts{}
				counts.cb = NewCircuitBreaker(RealClock{}, counts.hooks(),
					FailureThreshold(1),
					RecoveryTimeout(time.Second),
				)

				tt.report(t, counts.cb)

				assert.Equal(t, tt.wantNeutral, counts.neutral.Load(), "OnCircuitNeutral")
				assert.Equal(t, tt.wantStale, counts.stale.Load(), "OnCircuitStaleOutcome")
				assert.False(t, counts.underLock.Load(), "a hook fired under the breaker's lock")
			})
		})
	}
}

// acquire admits one call and fails the test on a rejection.
func acquire(t *testing.T, cb *CircuitBreaker) *CircuitPermit {
	t.Helper()

	permit, err := cb.Acquire()
	require.NoError(t, err)

	return &permit
}

// tripAndWait opens a FailureThreshold(1), RecoveryTimeout(1s) breaker and
// waits out its recovery timeout, so the next admission enters half-open.
func tripAndWait(t *testing.T, cb *CircuitBreaker) {
	t.Helper()

	tripBreaker(t, cb)
	time.Sleep(time.Second + time.Millisecond)
}

func TestPolicyMetricsCountNeutralAndStaleOutcomes(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var userNeutral, userStale atomic.Int32

		policy := NewPolicy[string]("",
			WithRegistry(NewRegistry()),
			WithCircuitBreaker(
				FailureThreshold(1),
				RecoveryTimeout(time.Minute),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
			WithHooks(&Hooks{
				OnCircuitNeutral:      func() { userNeutral.Add(1) },
				OnCircuitStaleOutcome: func() { userStale.Add(1) },
			}),
		)

		// A call its caller cancels is neutral.
		ctx, cancel := context.WithCancel(t.Context())
		cancelled := make(chan error, 1)

		go func() {
			_, err := policy.Do(ctx, blockUntilDone)
			cancelled <- err
		}()

		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-cancelled, context.Canceled)

		metrics := policy.Metrics()
		assert.EqualValues(t, 1, metrics.CircuitNeutralOutcomes)
		assert.Zero(t, metrics.CircuitStaleOutcomes)

		// A call admitted before the breaker opens reports a stale outcome.
		release := make(chan struct{})
		slow := make(chan error, 1)

		go func() {
			_, err := policy.Do(t.Context(), func(context.Context) (string, error) {
				<-release

				return "late", nil
			})
			slow <- err
		}()

		synctest.Wait()

		_, err := policy.Do(t.Context(), func(context.Context) (string, error) {
			return "", errDownstream
		})
		require.ErrorIs(t, err, errDownstream)
		require.Equal(t, CircuitOpen, policy.circuitBreaker.State())

		close(release)
		require.NoError(t, <-slow)

		metrics = policy.Metrics()
		assert.EqualValues(t, 1, metrics.CircuitNeutralOutcomes)
		assert.EqualValues(t, 1, metrics.CircuitStaleOutcomes)
		assert.Equal(t, "open", metrics.CircuitState, "the stale success did not close the breaker")

		assert.EqualValues(t, 1, userNeutral.Load(), "the caller's OnCircuitNeutral still fires")
		assert.EqualValues(t, 1, userStale.Load(), "the caller's OnCircuitStaleOutcome still fires")
	})
}

func TestCircuitAllowAndRecordFireNoOutcomeHook(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		counts := &outcomeHookCounts{}
		counts.cb = NewCircuitBreaker(RealClock{}, counts.hooks(), FailureThreshold(1))

		// Record is not tied to an admission: it is never neutral nor stale,
		// not even while the breaker is open.
		require.NoError(t, counts.cb.Allow())
		counts.cb.Record(0, errDownstream)
		require.Equal(t, CircuitOpen, counts.cb.State())

		counts.cb.Record(0, nil)
		counts.cb.Record(0, errDownstream)
		counts.cb.RecordSuccess()
		counts.cb.RecordFailure()

		assert.Zero(t, counts.neutral.Load(), "OnCircuitNeutral")
		assert.Zero(t, counts.stale.Load(), "OnCircuitStaleOutcome")
	})
}
