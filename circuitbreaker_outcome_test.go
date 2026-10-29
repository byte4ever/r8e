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

// tripBreaker drives a closed breaker built with FailureThreshold(1) to the
// open state through one failed admission.
func tripBreaker(t *testing.T, cb *CircuitBreaker) {
	t.Helper()

	permit, err := cb.Acquire()
	require.NoError(t, err)
	permit.Done(0, OutcomeFailure)
	require.Equal(t, CircuitOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Neutral outcome
// ---------------------------------------------------------------------------

func TestCircuitNeutralInClosedLeavesFailureCount(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{}, FailureThreshold(2))

		for _, outcome := range []Outcome{OutcomeFailure, OutcomeNeutral} {
			permit, err := cb.Acquire()
			require.NoError(t, err)
			permit.Done(0, outcome)
		}

		require.Equal(t, CircuitClosed, cb.State(), "a neutral outcome is not a failure")

		permit, err := cb.Acquire()
		require.NoError(t, err)
		permit.Done(0, OutcomeFailure)

		require.Equal(t, CircuitOpen, cb.State(), "the neutral outcome did not reset the counter")

		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
	})
}

func TestCircuitNeutralProbeKeepsHalfOpenAndFreesSlot(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(10*time.Second),
			RecoveryBackoffMultiplier(2),
		)
		tripBreaker(t, cb)
		time.Sleep(10*time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)
		probe.Done(0, OutcomeNeutral)

		require.Equal(t, CircuitHalfOpen, cb.State())

		next, err := cb.Acquire()
		require.NoError(t, err, "the neutral probe freed its slot")
		next.Done(0, OutcomeFailure)
		require.Equal(t, CircuitOpen, cb.State())

		// One failed probe since the trip: the wait is 10s × 2^1. Had the
		// neutral probe counted, it would be 10s × 2^2.
		time.Sleep(20 * time.Second)
		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)

		time.Sleep(time.Millisecond)
		_, err = cb.Acquire()
		require.NoError(t, err)
		require.Equal(t, CircuitHalfOpen, cb.State())
	})
}

// ---------------------------------------------------------------------------
// Stale admissions
// ---------------------------------------------------------------------------

func TestCircuitStaleSuccessIgnoredDuringHalfOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
		)

		slow, err := cb.Acquire()
		require.NoError(t, err)
		tripBreaker(t, cb)
		time.Sleep(time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)

		slow.Done(5*time.Second, OutcomeSuccess)

		require.Equal(t, CircuitHalfOpen, cb.State())

		_, err = cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen, "the probe slot is still held")

		probe.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State())
	})
}

func TestCircuitStaleFailureIgnoredDuringHalfOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
		)

		slow, err := cb.Acquire()
		require.NoError(t, err)
		tripBreaker(t, cb)
		time.Sleep(time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)

		slow.Done(5*time.Second, OutcomeFailure)

		require.Equal(t, CircuitHalfOpen, cb.State(), "a stale failure does not reopen")

		probe.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State(), "only the probe decides")
	})
}

func TestCircuitStaleFailureWhileOpenKeepsRecovery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
		)

		stale, err := cb.Acquire()
		require.NoError(t, err)
		tripBreaker(t, cb)

		time.Sleep(900 * time.Millisecond)
		stale.Done(5*time.Second, OutcomeFailure)

		time.Sleep(101 * time.Millisecond)

		_, err = cb.Acquire()
		require.NoError(t, err, "the stale failure did not restart the recovery timeout")
		assert.Equal(t, CircuitHalfOpen, cb.State())
	})
}

func TestCircuitStaleOutcomeSkipsSlowCallWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
			SlowCallRate(time.Second, 0.5),
			SlowCallMinCalls(1),
		)

		slow, err := cb.Acquire()
		require.NoError(t, err)
		tripBreaker(t, cb)
		time.Sleep(time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)
		probe.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State())

		before := cb.SlowCallFraction()
		slow.Done(time.Minute, OutcomeSuccess)

		assert.InDelta(t, before, cb.SlowCallFraction(), 1e-9)
		assert.Equal(t, CircuitClosed, cb.State())
	})
}

func TestCircuitSameEpochOutcomeAppliesAfterClose(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
			HalfOpenMaxAttempts(2),
		)
		tripBreaker(t, cb)
		time.Sleep(time.Second + time.Millisecond)

		first, err := cb.Acquire()
		require.NoError(t, err)
		second, err := cb.Acquire()
		require.NoError(t, err)

		first.Done(0, OutcomeSuccess)

		third, err := cb.Acquire()
		require.NoError(t, err)

		second.Done(0, OutcomeSuccess)
		require.Equal(t, CircuitClosed, cb.State())

		// The third probe belongs to the recovery that just closed the
		// breaker: its failure counts, as it did before permits existed.
		third.Done(0, OutcomeFailure)
		require.Equal(t, CircuitOpen, cb.State())
	})
}

// ---------------------------------------------------------------------------
// Permit
// ---------------------------------------------------------------------------

func TestCircuitPermitDoneIsIdempotent(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{}, FailureThreshold(2))

		permit, err := cb.Acquire()
		require.NoError(t, err)

		permit.Done(0, OutcomeFailure)
		permit.Done(0, OutcomeFailure)

		require.Equal(t, CircuitClosed, cb.State(), "the second Done counted nothing")
	})
}

func TestCircuitPermitZeroValueDoneIsNoop(t *testing.T) {
	t.Parallel()

	var permit CircuitPermit

	assert.NotPanics(t, func() { permit.Done(time.Second, OutcomeFailure) })
}

func TestCircuitPermitUnknownOutcomeCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{}, FailureThreshold(1))

		permit, err := cb.Acquire()
		require.NoError(t, err)
		permit.Done(0, Outcome(42))

		require.Equal(t, CircuitOpen, cb.State())
	})
}

func TestCircuitPermitUnsetOutcomeFailsSafe(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
		)
		tripBreaker(t, cb)
		time.Sleep(time.Second + time.Millisecond)

		probe, err := cb.Acquire()
		require.NoError(t, err)

		require.Panics(t, func() {
			var outcome Outcome

			defer func() { probe.Done(0, outcome) }()

			panic("boom")
		})

		assert.Equal(t, CircuitOpen, cb.State(), "the zero Outcome counts as a failure")
	})
}

func TestOutcomeString(t *testing.T) {
	t.Parallel()

	cases := map[Outcome]string{
		OutcomeSuccess: "success",
		OutcomeFailure: "failure",
		OutcomeNeutral: "neutral",
		Outcome(0):     "unknown",
		Outcome(42):    "unknown",
	}

	for outcome, want := range cases {
		assert.Equal(t, want, outcome.String(), "Outcome(%d).String()", int(outcome))
	}
}

func TestCircuitAcquireRejectsWhileOpen(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(RealClock{}, &Hooks{},
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
		)
		tripBreaker(t, cb)

		permit, err := cb.Acquire()
		require.ErrorIs(t, err, ErrCircuitOpen)
		assert.Equal(t, CircuitPermit{}, permit)
	})
}

// ---------------------------------------------------------------------------
// Classifier
// ---------------------------------------------------------------------------

type classifierCtxKey struct{}

func TestCircuitBreakerClassifierSeesCallContextAndError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			gotValue any
			gotErr   error
		)

		policy := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(func(ctx context.Context, err error) Outcome {
					gotValue = ctx.Value(classifierCtxKey{})
					gotErr = err

					return OutcomeNeutral
				}),
			),
		)

		ctx := context.WithValue(t.Context(), classifierCtxKey{}, "marker")
		_, err := policy.Do(ctx, func(context.Context) (string, error) {
			return "", errDownstream
		})

		require.ErrorIs(t, err, errDownstream, "the caller still gets the real error")
		assert.Equal(t, "marker", gotValue)
		require.ErrorIs(t, gotErr, errDownstream)
		assert.Equal(t, CircuitClosed, policy.circuitBreaker.State())
	})
}

func TestCircuitBreakerClassifierNilRestoresDefault(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		neutral := func(context.Context, error) Outcome { return OutcomeNeutral }
		policy := NewPolicy[string]("",
			WithCircuitBreaker(FailureThreshold(1), CircuitBreakerClassifier(neutral)),
		)
		failing := func(context.Context) (string, error) { return "", errDownstream }

		_, _ = policy.Do(t.Context(), failing)
		require.Equal(t, CircuitClosed, policy.circuitBreaker.State())

		policy.circuitBreaker.Reconfigure(CircuitBreakerClassifier(nil))

		_, _ = policy.Do(t.Context(), failing)
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State())
	})
}

func TestCallerCancellationIsNeutral(t *testing.T) {
	t.Parallel()

	live := t.Context()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	assert.Equal(t, OutcomeSuccess, CallerCancellationIsNeutral(live, nil))
	assert.Equal(t, OutcomeFailure, CallerCancellationIsNeutral(live, errDownstream))
	assert.Equal(t, OutcomeNeutral, CallerCancellationIsNeutral(cancelled, context.Canceled))
	assert.Equal(t, OutcomeFailure, CallerCancellationIsNeutral(cancelled, errDownstream))
}

func TestIsCallerCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		errSibling := errors.New("sibling task failed")
		deadline := time.Now().Add(time.Second)

		cancelled, cancel := context.WithCancel(t.Context())
		cancel()

		causeCancelled, cancelCause := context.WithCancelCause(t.Context())
		cancelCause(errSibling)

		expired, cancelExpired := context.WithDeadline(t.Context(), deadline)
		defer cancelExpired()

		// The context DoTimeout hands to fn once the policy timeout fires.
		timedOutCh := make(chan context.Context, 1)

		go func() {
			_, _ = DoTimeout(t.Context(), time.Second, func(ctx context.Context) (struct{}, error) {
				<-ctx.Done()
				timedOutCh <- ctx

				return struct{}{}, ctx.Err()
			}, &Hooks{})
		}()

		// The context a per-attempt retry timeout hands to fn once it fires.
		attemptCh := make(chan context.Context, 1)
		retrying := NewPolicy[struct{}]("",
			WithRetry(1, ConstantBackoff(time.Millisecond), PerAttemptTimeout(time.Second)),
		)

		go func() {
			_, _ = retrying.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
				<-ctx.Done()
				attemptCh <- ctx

				return struct{}{}, ctx.Err()
			})
		}()

		// The contexts a PropagateDeadline time budget hands to next.
		budget, cancelBudget := newBudgetDeadlineCtx(t.Context(), RealClock{}, deadline)
		defer cancelBudget()

		spent, cancelSpent := newBudgetDeadlineCtx(t.Context(), RealClock{}, time.Now())
		defer cancelSpent()

		budgetParent, cancelBudgetParent := context.WithCancel(t.Context())
		budgetCancelled, cancelBudgetCancelled := newBudgetDeadlineCtx(budgetParent, RealClock{}, deadline)
		defer cancelBudgetCancelled()
		cancelBudgetParent()

		// RespectInboundDeadline tightens the budget to the caller's deadline.
		inboundParent, cancelInboundParent := context.WithDeadline(t.Context(), deadline)
		defer cancelInboundParent()

		inbound, cancelInbound := newBudgetDeadlineCtx(inboundParent, RealClock{}, deadline)
		defer cancelInbound()

		// Without RespectInboundDeadline, a caller deadline earlier than the
		// budget still ends the call first.
		earlyParent, cancelEarlyParent := context.WithDeadline(t.Context(), deadline.Add(-time.Second/2))
		defer cancelEarlyParent()

		callerFirst, cancelCallerFirst := newBudgetDeadlineCtx(earlyParent, RealClock{}, deadline)
		defer cancelCallerFirst()

		// A budget earlier than the caller's own, later deadline.
		laterParent, cancelLaterParent := context.WithDeadline(t.Context(), deadline.Add(time.Second))
		defer cancelLaterParent()

		budgetFirst, cancelBudgetFirst := newBudgetDeadlineCtx(laterParent, RealClock{}, deadline)
		defer cancelBudgetFirst()

		// A caller deadline set below a budget that expires later; read
		// after the budget expired.
		outerBudget, cancelOuterBudget := newBudgetDeadlineCtx(t.Context(), RealClock{}, deadline)
		defer cancelOuterBudget()

		belowBudget, cancelBelowBudget := context.WithDeadline(outerBudget, deadline.Add(-time.Second/2))
		defer cancelBelowBudget()

		// A plain child of a budget, ended when the budget expires.
		budgetChild, cancelBudgetChild := context.WithCancel(outerBudget)
		defer cancelBudgetChild()

		// Caller deadlines set under context.WithoutCancel below a shorter
		// r8e deadline: the detach keeps the recorded instant but drops the
		// deadline it describes.
		detachedDeadline := deadline.Add(-time.Second / 4)

		policyTimeout, cancelPolicyTimeout := withPolicyTimeout(t.Context(), 100*time.Millisecond)
		defer cancelPolicyTimeout()

		detachedFromTimeout, cancelDetachedFromTimeout := context.WithDeadline(
			context.WithoutCancel(policyTimeout), detachedDeadline)
		defer cancelDetachedFromTimeout()

		shortBudget, cancelShortBudget := newBudgetDeadlineCtx(
			t.Context(), RealClock{}, time.Now().Add(100*time.Millisecond))
		defer cancelShortBudget()

		detachedFromBudget, cancelDetachedFromBudget := context.WithDeadline(
			context.WithoutCancel(shortBudget), detachedDeadline)
		defer cancelDetachedFromBudget()

		// A detached caller deadline on the innermost recorded instant reads as
		// r8e's.
		policyInstant, _ := policyTimeout.Deadline()

		detachedCopy, cancelDetachedCopy := context.WithDeadline(
			context.WithoutCancel(policyTimeout), policyInstant)
		defer cancelDetachedCopy()

		// A looser r8e timeout does not bind and records nothing, so the
		// innermost recorded instant stays the one above it.
		looseUnderPolicy, cancelLooseUnderPolicy := withPolicyTimeout(policyTimeout, 200*time.Millisecond)
		defer cancelLooseUnderPolicy()

		copyUnderLoose, cancelCopyUnderLoose := context.WithDeadline(
			context.WithoutCancel(looseUnderPolicy), policyInstant)
		defer cancelCopyUnderLoose()

		// Under a tighter timeout that binds, the enclosing instant is no
		// longer the innermost recorded one: a copy of it is the caller's.
		enclosingTimeout, cancelEnclosingTimeout := withPolicyTimeout(t.Context(), 200*time.Millisecond)
		defer cancelEnclosingTimeout()

		innerTimeout, cancelInnerTimeout := withPolicyTimeout(enclosingTimeout, 100*time.Millisecond)
		defer cancelInnerTimeout()

		enclosingInstant, _ := enclosingTimeout.Deadline()

		detachedEnclosingCopy, cancelDetachedEnclosingCopy := context.WithDeadline(
			context.WithoutCancel(innerTimeout), enclosingInstant)
		defer cancelDetachedEnclosingCopy()

		// A caller deadline between two r8e timeouts: the inner one does not
		// bind, so the enclosing instant stays the innermost recorded one.
		callerUnderEnclosing, cancelCallerUnderEnclosing := context.WithTimeout(enclosingTimeout, 50*time.Millisecond)
		defer cancelCallerUnderEnclosing()

		looseUnderCaller, cancelLooseUnderCaller := withPolicyTimeout(callerUnderEnclosing, 100*time.Millisecond)
		defer cancelLooseUnderCaller()

		callerInstant, _ := callerUnderEnclosing.Deadline()

		callerCopy, cancelCallerCopy := context.WithDeadline(
			context.WithoutCancel(looseUnderCaller), callerInstant)
		defer cancelCallerCopy()

		enclosingCopy, cancelEnclosingCopy := context.WithDeadline(
			context.WithoutCancel(looseUnderCaller), enclosingInstant)
		defer cancelEnclosingCopy()

		// A DoTimeout longer than the caller's deadline does not bind.
		looseTimeoutCh := make(chan context.Context, 1)

		go func() {
			_, _ = DoTimeout(earlyParent, 2*time.Second, func(ctx context.Context) (struct{}, error) {
				<-ctx.Done()
				looseTimeoutCh <- ctx

				return struct{}{}, ctx.Err()
			}, &Hooks{})
		}()

		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()

		timedOut := <-timedOutCh
		attempt := <-attemptCh
		looseTimeout := <-looseTimeoutCh

		cases := map[string]struct {
			ctx  context.Context //nolint:containedctx // table input
			err  error
			want bool
		}{
			"live context":                                   {ctx: t.Context(), err: context.Canceled},
			"nil error":                                      {ctx: cancelled},
			"caller cancelled":                               {ctx: cancelled, err: context.Canceled, want: true},
			"caller cancelled, wrapped":                      {ctx: cancelled, err: wrapped(context.Canceled), want: true},
			"caller cancelled, other ctx error":              {ctx: cancelled, err: context.DeadlineExceeded},
			"caller cancel cause returned":                   {ctx: causeCancelled, err: errSibling, want: true},
			"caller cancel cause, ctx.Err()":                 {ctx: causeCancelled, err: context.Canceled, want: true},
			"caller deadline":                                {ctx: expired, err: context.DeadlineExceeded, want: true},
			"unrelated error":                                {ctx: cancelled, err: errDownstream},
			"policy timeout":                                 {ctx: timedOut, err: context.DeadlineExceeded},
			"policy timeout cause returned":                  {ctx: timedOut, err: context.Cause(timedOut)},
			"per-attempt retry timeout":                      {ctx: attempt, err: context.DeadlineExceeded},
			"time budget hard deadline":                      {ctx: budget, err: context.DeadlineExceeded},
			"time budget already spent":                      {ctx: spent, err: context.DeadlineExceeded},
			"time budget, caller cancelled":                  {ctx: budgetCancelled, err: context.Canceled, want: true},
			"time budget at the caller's deadline":           {ctx: inbound, err: context.DeadlineExceeded, want: true},
			"time budget, caller deadline first":             {ctx: callerFirst, err: context.DeadlineExceeded, want: true},
			"time budget before a later caller deadline":     {ctx: budgetFirst, err: context.DeadlineExceeded},
			"caller deadline below a later budget":           {ctx: belowBudget, err: context.DeadlineExceeded, want: true},
			"child of an expired time budget":                {ctx: budgetChild, err: context.DeadlineExceeded},
			"policy timeout after the caller's deadline":     {ctx: looseTimeout, err: context.DeadlineExceeded, want: true},
			"caller deadline detached from a policy timeout": {ctx: detachedFromTimeout, err: context.DeadlineExceeded, want: true},
			"caller deadline detached from a time budget":    {ctx: detachedFromBudget, err: context.DeadlineExceeded, want: true},
			"detached copy of a policy timeout":              {ctx: detachedCopy, err: context.DeadlineExceeded},
			"detached copy of an enclosing policy timeout":   {ctx: detachedEnclosingCopy, err: context.DeadlineExceeded, want: true},
			"detached copy under a looser policy timeout":    {ctx: copyUnderLoose, err: context.DeadlineExceeded},
			"caller deadline copied above a loose timeout":   {ctx: callerCopy, err: context.DeadlineExceeded, want: true},
			"copy of an enclosing timeout above a loose one": {ctx: enclosingCopy, err: context.DeadlineExceeded},
			"cancel error, live context":                     {ctx: t.Context(), err: context.DeadlineExceeded},
		}

		for name, tc := range cases {
			assert.Equal(t, tc.want, IsCallerCancellation(tc.ctx, tc.err), name)
		}
	})
}

func TestPolicyTimeoutsKeepContextErrAndCause(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		type ending struct {
			path       string
			err, cause error
		}

		endings := make(chan ending, 2)
		capture := func(path string) func(context.Context) (struct{}, error) {
			return func(ctx context.Context) (struct{}, error) {
				<-ctx.Done()
				endings <- ending{path: path, err: ctx.Err(), cause: context.Cause(ctx)}

				return struct{}{}, ctx.Err()
			}
		}

		_, err := DoTimeout(t.Context(), time.Second, capture("DoTimeout"), &Hooks{})
		require.ErrorIs(t, err, ErrTimeout)

		retrying := NewPolicy[struct{}]("",
			WithRetry(1, ConstantBackoff(time.Millisecond), PerAttemptTimeout(time.Second)),
		)
		_, err = retrying.Do(t.Context(), capture("per-attempt timeout"))
		require.ErrorIs(t, err, context.DeadlineExceeded)

		synctest.Wait()

		for range 2 {
			got := <-endings

			// Compared with ==, as code written before this change may do.
			assert.True(t, got.err == context.DeadlineExceeded, //nolint:errorlint // identity is the contract
				"%s: ctx.Err() is %#v", got.path, got.err)
			assert.True(t, got.cause == context.DeadlineExceeded, //nolint:errorlint // identity is the contract
				"%s: context.Cause is %#v", got.path, got.cause)
		}
	})
}

func TestPolicyTimeoutsRecordTheirOwnDeadline(t *testing.T) {
	t.Parallel()

	// Runs on the real clock on purpose: inside a synctest bubble two clock
	// reads always agree, so a recorded instant read from the clock rather
	// than from the context's deadline would go unnoticed.
	attribution := make(chan bool, 1)
	release := make(chan struct{})
	report := func(ctx context.Context) (struct{}, error) {
		<-ctx.Done()
		attribution <- IsCallerCancellation(ctx, ctx.Err())

		// Held until DoTimeout has returned, so its select never finds this
		// result ready alongside the timeout.
		<-release

		return struct{}{}, ctx.Err()
	}

	_, err := DoTimeout(t.Context(), time.Millisecond, report, &Hooks{})
	close(release)
	require.ErrorIs(t, err, ErrTimeout)
	assert.False(t, <-attribution, "DoTimeout: the policy timeout read as the caller's")

	retrying := NewPolicy[struct{}]("",
		WithRetry(1, ConstantBackoff(time.Millisecond), PerAttemptTimeout(time.Millisecond)),
	)
	_, err = retrying.Do(t.Context(), report)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, <-attribution, "per-attempt timeout: the retry timeout read as the caller's")
}

func wrapped(err error) error {
	return &permanentError{err: err}
}

// ---------------------------------------------------------------------------
// Policy middleware
// ---------------------------------------------------------------------------

func blockUntilDone(ctx context.Context) (string, error) {
	<-ctx.Done()

	return "", ctx.Err()
}

func TestPolicyCallerCancellationLeavesCounter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(2),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		failing := func(context.Context) (string, error) { return "", errDownstream }

		_, _ = policy.Do(t.Context(), failing)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			_, err := policy.Do(ctx, blockUntilDone)
			done <- err
		}()

		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.Equal(t, CircuitClosed, policy.circuitBreaker.State())

		_, _ = policy.Do(t.Context(), failing)
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State(),
			"the cancelled call neither counted nor reset the counter")
	})
}

func TestPolicyCallerCancelledProbeFreesSlot(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(1),
				RecoveryTimeout(time.Second),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		_, _ = policy.Do(t.Context(), func(context.Context) (string, error) {
			return "", errDownstream
		})
		time.Sleep(time.Second + time.Millisecond)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			_, err := policy.Do(ctx, blockUntilDone)
			done <- err
		}()

		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.Equal(t, CircuitHalfOpen, policy.circuitBreaker.State())

		got, err := policy.Do(t.Context(), func(context.Context) (string, error) {
			return "ok", nil
		})
		require.NoError(t, err, "the next call is admitted as a probe right away")
		assert.Equal(t, "ok", got)
		assert.Equal(t, CircuitClosed, policy.circuitBreaker.State())
	})
}

func TestPolicyTimeoutCountsAsFailureWithCallerCancellationIsNeutral(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeout(100*time.Millisecond),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		_, err := policy.Do(t.Context(), blockUntilDone)
		require.ErrorIs(t, err, ErrTimeout)

		synctest.Wait()
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State())
	})
}

func TestPolicyTimeBudgetDeadlineCountsAsFailureWithCallerCancellationIsNeutral(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeBudget(100*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		_, err := policy.Do(t.Context(), blockUntilDone)
		require.ErrorIs(t, err, ErrTimeBudgetExceeded)
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State())
	})
}

func TestPolicyCircuitBreakerPanicCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(time.Second)),
		)
		panicking := func(context.Context) (string, error) { panic("boom") }

		require.PanicsWithValue(t, "boom", func() { _, _ = policy.Do(t.Context(), panicking) })
		require.Equal(t, CircuitOpen, policy.circuitBreaker.State())

		time.Sleep(time.Second + time.Millisecond)

		require.PanicsWithValue(t, "boom", func() { _, _ = policy.Do(t.Context(), panicking) })
		require.Equal(t, CircuitOpen, policy.circuitBreaker.State(),
			"the panicking probe counted as a failure and freed its slot")

		time.Sleep(time.Second + time.Millisecond)

		_, err := policy.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.NoError(t, err)
		assert.Equal(t, CircuitClosed, policy.circuitBreaker.State())
	})
}

func TestPolicyCallerCancellationWithCauseIsNeutral(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		errSibling := errors.New("sibling task failed")
		policy := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)

		go func() {
			_, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
				<-ctx.Done()

				// net/http returns the cause of a cancelled request context.
				return "", context.Cause(ctx)
			})
			done <- err
		}()

		synctest.Wait()
		cancel(errSibling)
		require.ErrorIs(t, <-done, errSibling)
		assert.Equal(t, CircuitClosed, policy.circuitBreaker.State())
	})
}

func TestPolicyEnclosingPerAttemptTimeoutCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(2),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		outer := NewPolicy[string]("",
			WithRetry(2, ConstantBackoff(time.Millisecond), PerAttemptTimeout(100*time.Millisecond)),
		)

		_, err := outer.Do(t.Context(), func(ctx context.Context) (string, error) {
			return inner.Do(ctx, blockUntilDone)
		})
		require.Error(t, err)
		assert.Equal(t, CircuitOpen, inner.circuitBreaker.State())
	})
}

func TestPolicyNestedCallerCancellationIgnoresLaterBudgetExpiry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		outer := NewPolicy[string]("",
			WithTimeBudget(100*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
		)
		answered := 0

		_, _ = outer.Do(t.Context(), func(ctx context.Context) (string, error) {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			time.AfterFunc(10*time.Millisecond, cancel)

			return inner.Do(innerCtx, func(ctx context.Context) (string, error) {
				// Returns long after its caller cancelled and the outer
				// budget expired.
				time.Sleep(200 * time.Millisecond)

				answered++

				return "", fmt.Errorf("late answer: %w", ctx.Err())
			})
		})

		require.Equal(t, 1, answered, "the inner call ran and was classified")
		assert.Equal(t, CircuitClosed, inner.circuitBreaker.State())
	})
}

func TestPolicyNestedCallerDeadlineIgnoresLaterBudgetExpiry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := NewPolicy[string]("",
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		outer := NewPolicy[string]("",
			WithTimeBudget(100*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
		)
		answered := 0

		_, _ = outer.Do(t.Context(), func(ctx context.Context) (string, error) {
			innerCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
			defer cancel()

			return inner.Do(innerCtx, func(ctx context.Context) (string, error) {
				// Returns long after its caller's own deadline and the
				// outer budget expired.
				time.Sleep(200 * time.Millisecond)

				answered++

				return "", fmt.Errorf("late answer: %w", ctx.Err())
			})
		})

		require.Equal(t, 1, answered, "the inner call ran and was classified")
		assert.Equal(t, CircuitClosed, inner.circuitBreaker.State())
	})
}

func TestPolicyDetachedCallerDeadlineIsTheCallers(t *testing.T) {
	t.Parallel()

	outers := map[string][]Option{
		"timeout": {WithTimeout(20 * time.Millisecond)},
		"time budget": {
			WithTimeBudget(20*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
		},
		"per-attempt timeout": {
			WithRetry(1, ConstantBackoff(time.Millisecond), PerAttemptTimeout(20*time.Millisecond)),
		},
	}

	for name, opts := range outers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				inner := NewPolicy[string]("",
					WithCircuitBreaker(
						FailureThreshold(1),
						CircuitBreakerClassifier(CallerCancellationIsNeutral),
					),
				)
				outer := NewPolicy[string]("", opts...)
				calls := 0
				innerErr := make(chan error, 1)

				_, err := outer.Do(t.Context(), func(ctx context.Context) (string, error) {
					// Work meant to outlive the outer call, under the
					// caller's own, later deadline.
					detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 50*time.Millisecond)
					defer cancel()

					_, err := inner.Do(detached, func(ctx context.Context) (string, error) {
						calls++

						return blockUntilDone(ctx)
					})
					innerErr <- err

					return "", err
				})
				require.Error(t, err)

				require.ErrorIs(t, <-innerErr, context.DeadlineExceeded)
				require.Equal(t, 1, calls, "the call reached the downstream")
				assert.Equal(t, CircuitClosed, inner.circuitBreaker.State(),
					"the caller's own deadline ended the call")
			})
		})
	}
}

func TestPolicyBudgetAtInboundDeadlineIsTheCallers(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeBudget(time.Second, PropagateDeadline(), RespectInboundDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		calls := 0

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
		defer cancel()

		_, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
			calls++

			return blockUntilDone(ctx)
		})
		require.Error(t, err)
		require.Equal(t, 1, calls, "the call reached the downstream")
		assert.Equal(t, CircuitClosed, policy.circuitBreaker.State(),
			"the budget adopted the caller's deadline, so the caller gave up")
	})
}

func TestPolicyBudgetBeforeLaterCallerDeadlineCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeBudget(100*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		_, err := policy.Do(ctx, blockUntilDone)
		require.ErrorIs(t, err, ErrTimeBudgetExceeded)
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State())
	})
}

func TestPolicyOwnTimeoutAdoptedByBudgetCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeout(20*time.Millisecond),
			WithTimeBudget(time.Second, PropagateDeadline(), RespectInboundDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		_, err := policy.Do(t.Context(), blockUntilDone)
		require.Error(t, err)

		synctest.Wait()
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State(),
			"the budget adopted the policy's own timeout, which is r8e's")
	})
}

func TestPolicyEnclosingTimeoutAdoptedByBudgetCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := NewPolicy[string]("",
			WithTimeBudget(time.Second, PropagateDeadline(), RespectInboundDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		outer := NewPolicy[string]("", WithTimeout(20*time.Millisecond))

		_, err := outer.Do(t.Context(), func(ctx context.Context) (string, error) {
			return inner.Do(ctx, blockUntilDone)
		})
		// The outer timeout and the budget that adopted it end together, so
		// the caller sees either ErrTimeout or ErrTimeBudgetExceeded.
		require.Error(t, err)

		synctest.Wait()
		assert.Equal(t, CircuitOpen, inner.circuitBreaker.State())
	})
}

func TestPolicyEqualTimeoutAndBudgetCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		policy := NewPolicy[string]("",
			WithTimeout(20*time.Millisecond),
			WithTimeBudget(20*time.Millisecond, PropagateDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)

		_, err := policy.Do(t.Context(), blockUntilDone)
		require.Error(t, err)

		synctest.Wait()
		assert.Equal(t, CircuitOpen, policy.circuitBreaker.State())
	})
}

func TestPolicyEnclosingPerAttemptTimeoutAdoptedByBudgetCountsAsFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := NewPolicy[string]("",
			WithTimeBudget(time.Second, PropagateDeadline(), RespectInboundDeadline()),
			WithRetry(1, ConstantBackoff(time.Millisecond)),
			WithCircuitBreaker(
				FailureThreshold(1),
				CircuitBreakerClassifier(CallerCancellationIsNeutral),
			),
		)
		outer := NewPolicy[string]("",
			WithRetry(1, ConstantBackoff(time.Millisecond), PerAttemptTimeout(20*time.Millisecond)),
		)

		_, err := outer.Do(t.Context(), func(ctx context.Context) (string, error) {
			return inner.Do(ctx, blockUntilDone)
		})
		require.Error(t, err)
		assert.Equal(t, CircuitOpen, inner.circuitBreaker.State())
	})
}
