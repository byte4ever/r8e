package r8e

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localRejections are the sentinels a guard inside the breaker returns when it
// turns a call away.
var localRejections = []error{ //nolint:gochecknoglobals // read-only test table
	ErrRateLimited,
	ErrBulkheadFull,
	ErrBulkheadTimeout,
	ErrCoDelShed,
	ErrConcurrencyLimited,
}

// ---------------------------------------------------------------------------
// Local rejections
// ---------------------------------------------------------------------------

func TestIsLocalRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		name string
		want bool
	}{
		{name: "rate limited", err: ErrRateLimited, want: true},
		{name: "bulkhead full", err: ErrBulkheadFull, want: true},
		{name: "bulkhead wait timeout", err: ErrBulkheadTimeout, want: true},
		{name: "shed by the controlled-delay queue", err: ErrCoDelShed, want: true},
		{name: "concurrency limited", err: ErrConcurrencyLimited, want: true},
		{name: "wrapped by the caller", err: fmt.Errorf("call catalog: %w", ErrBulkheadFull)},
		{
			name: "nested policy's rejection after retries",
			err:  fmt.Errorf("%w: %w", ErrRetriesExhausted, ErrBulkheadFull),
		},
		{
			name: "concurrency budget around a downstream failure",
			err:  fmt.Errorf("%w: %w", ErrConcurrencyBudgetExceeded, errDownstream),
		},
		{
			name: "time budget around a downstream failure",
			err:  fmt.Errorf("%w: %w", ErrTimeBudgetExceeded, errDownstream),
		},
		{name: "nil"},
		{name: "downstream error", err: errDownstream},
		{name: "open breaker of an inner policy", err: ErrCircuitOpen},
		{name: "breaker ramping", err: ErrCircuitRamping},
		{name: "adaptive throttle", err: ErrThrottled},
		{name: "SLO shed", err: ErrSLOShed},
		{name: "policy timeout", err: ErrTimeout},
		{name: "caller cancellation", err: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, IsLocalRejection(tt.err))
		})
	}
}

func TestCallerCancellationIsNeutralCountsLocalRejections(t *testing.T) {
	t.Parallel()

	// CallerCancellationIsNeutral is unchanged: making a local rejection
	// neutral is the classifier's own choice.
	for _, sentinel := range localRejections {
		assert.Equal(t, OutcomeFailure, CallerCancellationIsNeutral(t.Context(), sentinel), "%v", sentinel)
	}
}

func TestPolicyBulkheadRejectionsAndTheBreaker(t *testing.T) {
	t.Parallel()

	neutralRejections := func(ctx context.Context, err error) Outcome {
		if IsLocalRejection(err) {
			return OutcomeNeutral
		}

		return CallerCancellationIsNeutral(ctx, err)
	}

	tests := []struct {
		name       string
		want       CircuitState
		breakerOpt []CircuitBreakerOption
	}{
		{name: "default classification counts them", want: CircuitOpen},
		{
			name:       "IsLocalRejection makes them neutral",
			want:       CircuitClosed,
			breakerOpt: []CircuitBreakerOption{CircuitBreakerClassifier(neutralRejections)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				p := NewPolicy[string]("", WithRegistry(NewRegistry()),
					WithBulkhead(1),
					WithCircuitBreaker(append([]CircuitBreakerOption{FailureThreshold(2)}, tt.breakerOpt...)...),
				)

				release := make(chan struct{}, 1)
				held := make(chan error, 1)

				// The held call returns on every exit path of the test, so a
				// failed assertion cannot leave it blocked in the bubble; the
				// buffered release below never blocks the test either.
				defer close(release)

				go func() {
					_, err := p.Do(t.Context(), func(ctx context.Context) (string, error) {
						select {
						case <-release:
							return "held", nil
						case <-ctx.Done():
							return "", ctx.Err()
						}
					})
					held <- err
				}()

				synctest.Wait()

				for range 3 {
					_, err := p.Do(t.Context(), func(context.Context) (string, error) {
						return "unreached", nil
					})
					require.True(t, errors.Is(err, ErrBulkheadFull) || errors.Is(err, ErrCircuitOpen), "got %v", err)
				}

				assert.Equal(t, tt.want, p.circuitBreaker.State())

				release <- struct{}{}
				require.NoError(t, <-held)
			})
		})
	}
}

// holdOneSlot starts a call that keeps the policy's only concurrency slot until
// the returned func is called, which then waits for that call to finish.
func holdOneSlot(t *testing.T, p *Policy[string]) func() {
	t.Helper()

	release := make(chan struct{})
	held := make(chan error, 1)

	go func() {
		_, err := p.Do(t.Context(), func(ctx context.Context) (string, error) {
			select {
			case <-release:
				return "held", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		held <- err
	}()

	synctest.Wait()

	return func() {
		close(release)
		require.NoError(t, <-held)
	}
}

// rejectTwice makes two calls that the policy's guard is expected to turn
// away, and returns their errors.
func rejectTwice(t *testing.T, p *Policy[string]) []error {
	t.Helper()

	errs := make([]error, 0, 2)
	for range 2 {
		_, err := p.Do(t.Context(), func(context.Context) (string, error) { return "unreached", nil })
		errs = append(errs, err)
	}

	return errs
}

func TestPolicyLocalRejectionsLeaveBreakerClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sentinel error
		provoke  func(t *testing.T, p *Policy[string]) []error
		name     string
		guard    []Option
	}{
		{
			name:     "rate limiter in reject mode",
			guard:    []Option{WithRateLimit(1)},
			sentinel: ErrRateLimited,
			provoke: func(t *testing.T, p *Policy[string]) []error {
				t.Helper()

				// The bucket starts with one token and time stands still.
				_, err := p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
				require.NoError(t, err)

				return rejectTwice(t, p)
			},
		},
		{
			name:     "bulkhead without wait",
			guard:    []Option{WithBulkhead(1)},
			sentinel: ErrBulkheadFull,
			provoke: func(t *testing.T, p *Policy[string]) []error {
				t.Helper()

				done := holdOneSlot(t, p)
				defer done()

				return rejectTwice(t, p)
			},
		},
		{
			name:     "bulkhead with max-wait",
			guard:    []Option{WithBulkhead(1, BulkheadMaxWait(10*time.Millisecond))},
			sentinel: ErrBulkheadTimeout,
			provoke: func(t *testing.T, p *Policy[string]) []error {
				t.Helper()

				done := holdOneSlot(t, p)
				defer done()

				return rejectTwice(t, p)
			},
		},
		{
			name: "bulkhead with controlled-delay queue",
			guard: []Option{WithBulkhead(1,
				BulkheadCoDel(5*time.Millisecond, 10*time.Millisecond),
				BulkheadQueueDepth(3),
			)},
			sentinel: ErrCoDelShed,
			provoke: func(t *testing.T, p *Policy[string]) []error {
				t.Helper()

				// Four calls of 20ms arrive together: one takes the slot and
				// three queue. The first release starts the over-target timer;
				// the second, two intervals later, latches the overload and
				// sheds the two waiters left, whose dwell is past 2 × target.
				results := make(chan error, 4)

				for range 4 {
					go func() {
						_, err := p.Do(t.Context(), func(context.Context) (string, error) {
							time.Sleep(20 * time.Millisecond)

							return "ok", nil
						})
						results <- err
					}()
				}

				var shed []error

				for range 4 {
					if err := <-results; err != nil {
						shed = append(shed, err)
					}
				}

				return shed
			},
		},
		{
			name:     "adaptive concurrency limiter",
			guard:    []Option{WithAdaptiveConcurrency(InitialLimit(1), MinLimit(1), MaxLimit(1))},
			sentinel: ErrConcurrencyLimited,
			provoke: func(t *testing.T, p *Policy[string]) []error {
				t.Helper()

				done := holdOneSlot(t, p)
				defer done()

				return rejectTwice(t, p)
			},
		},
	}

	neutralRejections := func(ctx context.Context, err error) Outcome {
		if IsLocalRejection(err) {
			return OutcomeNeutral
		}

		return CallerCancellationIsNeutral(ctx, err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				opts := append([]Option{
					WithRegistry(NewRegistry()),
					// One counted failure would open the breaker.
					WithCircuitBreaker(FailureThreshold(1), CircuitBreakerClassifier(neutralRejections)),
				}, tt.guard...)
				p := NewPolicy[string]("", opts...)

				rejections := tt.provoke(t, p)

				require.GreaterOrEqual(t, len(rejections), 2)

				for _, err := range rejections {
					require.ErrorIs(t, err, tt.sentinel)
				}

				assert.Equal(t, CircuitClosed, p.circuitBreaker.State(),
					"the guard must hand the breaker its sentinel unwrapped: IsLocalRejection compares by identity")
			})
		})
	}
}
