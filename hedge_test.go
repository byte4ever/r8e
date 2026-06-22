package r8e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

// The hedge tests use r8e.RealClock and real durations, so they run inside a
// testing/synctest bubble: time is virtual and deterministic, durations are
// honoured in order without real waiting, and a leaked goroutine surfaces as a
// bubble deadlock.

// ---------------------------------------------------------------------------
// Primary wins fast (before hedge delay) -> returns primary result
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryWinsFast(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var hedgeTriggered atomic.Bool
		hooks := &r8e.Hooks{
			OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
		}

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(_ context.Context) (string, error) {
				return "primary", nil
			},
			r8e.HedgeParams{Delay: time.Hour, Hooks: hooks, Clock: r8e.RealClock{}},
		)
		require.NoError(t, err)
		require.Equal(t, "primary", result)
		require.False(
			t,
			hedgeTriggered.Load(),
			"OnHedgeTriggered should not fire when primary wins fast",
		)
	})
}

// ---------------------------------------------------------------------------
// Primary slow + hedge wins -> OnHedgeTriggered + OnHedgeWon emitted
// ---------------------------------------------------------------------------

func TestDoHedgePrimarySlowHedgeWins(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var hedgeTriggered, hedgeWon atomic.Bool
		hooks := &r8e.Hooks{
			OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
			OnHedgeWon:       func() { hedgeWon.Store(true) },
		}

		var callCount atomic.Int32

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					// Primary: very slow, will be cancelled.
					select {
					case <-time.After(5 * time.Second):
						return "primary-late", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}

				return "hedge", nil
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: hooks,
				Clock: r8e.RealClock{},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "hedge", result)
		require.True(t, hedgeTriggered.Load(), "OnHedgeTriggered should fire")
		require.True(t, hedgeWon.Load(), "OnHedgeWon should fire")
	})
}

// ---------------------------------------------------------------------------
// Primary slow + primary still wins after hedge triggered
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryWinsAfterHedgeTriggered(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var hedgeTriggered, hedgeWon atomic.Bool
		hooks := &r8e.Hooks{
			OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
			OnHedgeWon:       func() { hedgeWon.Store(true) },
		}

		var callCount atomic.Int32

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					// Primary: a bit slow but completes.
					select {
					case <-time.After(40 * time.Millisecond):
						return "primary", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				// Secondary: very slow, will be cancelled.
				select {
				case <-time.After(5 * time.Second):
					return "hedge-late", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: hooks,
				Clock: r8e.RealClock{},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "primary", result)
		require.True(t, hedgeTriggered.Load(), "OnHedgeTriggered should fire")
		require.False(t, hedgeWon.Load(), "OnHedgeWon should not fire")
	})
}

// ---------------------------------------------------------------------------
// Both fail -> returns error
// ---------------------------------------------------------------------------

func TestDoHedgeBothFail(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var callCount atomic.Int32

		_, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					select {
					case <-time.After(40 * time.Millisecond):
						return "", errors.New("primary error")
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}

				return "", errors.New("hedge error")
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
			},
		)
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// Context cancellation -> returns ctx.Err()
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		_, err := r8e.DoHedge[string](
			ctx,
			func(ctx context.Context) (string, error) {
				<-ctx.Done()

				return "", ctx.Err()
			},
			r8e.HedgeParams{Delay: time.Hour, Hooks: &r8e.Hooks{}, Clock: r8e.RealClock{}},
		)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// ---------------------------------------------------------------------------
// Context already cancelled -> returns ctx.Err() immediately
// ---------------------------------------------------------------------------

func TestDoHedgeContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r8e.DoHedge[string](
		ctx,
		func(_ context.Context) (string, error) {
			return "should-not-run", nil
		},
		r8e.HedgeParams{Delay: time.Hour, Hooks: &r8e.Hooks{}, Clock: r8e.RealClock{}},
	)
	require.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestDoHedgeNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	// Nil Hooks and nil Clock must both default to no-op / RealClock.
	result, err := r8e.DoHedge[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		r8e.HedgeParams{Delay: time.Hour},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// Primary errors but hedge succeeds
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryErrorHedgeSucceeds(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var callCount atomic.Int32

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					select {
					case <-time.After(40 * time.Millisecond):
						return "", errors.New("primary failed")
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}

				return "hedge-ok", nil
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "hedge-ok", result)
	})
}

// ---------------------------------------------------------------------------
// Primary fails fast (before hedge delay) -> returns error, no hedge
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryFailsFast(t *testing.T) {
	t.Parallel()

	var hedgeTriggered atomic.Bool
	hooks := &r8e.Hooks{
		OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
	}
	sentinel := errors.New("primary fast error")

	_, err := r8e.DoHedge[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", sentinel
		},
		r8e.HedgeParams{Delay: time.Hour, Hooks: hooks, Clock: r8e.RealClock{}},
	)
	require.ErrorIs(t, err, sentinel)
	require.False(
		t,
		hedgeTriggered.Load(),
		"OnHedgeTriggered should not fire when primary fails fast",
	)
}

// ---------------------------------------------------------------------------
// Hedge fails first, primary succeeds second -> primary result returned
// ---------------------------------------------------------------------------

func TestDoHedgeHedgeFailsPrimarySucceeds(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var callCount atomic.Int32

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					select {
					case <-time.After(60 * time.Millisecond):
						return "primary-ok", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}

				return "", errors.New("hedge failed")
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "primary-ok", result)
	})
}

// ---------------------------------------------------------------------------
// Context cancelled after hedge triggered (during wait for results)
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancelledAfterHedgeTriggered(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var callCount atomic.Int32

		_, err := r8e.DoHedge[string](
			ctx,
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					<-ctx.Done()

					return "", ctx.Err()
				}
				cancel()
				<-ctx.Done()

				return "", ctx.Err()
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
			},
		)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// ---------------------------------------------------------------------------
// Context cancelled while waiting for second result after first fails
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancelledWhileWaitingSecondResult(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var callCount atomic.Int32

		_, err := r8e.DoHedge[string](
			ctx,
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					<-ctx.Done()

					return "", ctx.Err()
				}
				go func() {
					time.Sleep(30 * time.Millisecond)
					cancel()
				}()

				return "", errors.New("hedge failed")
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
			},
		)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// ---------------------------------------------------------------------------
// Primary errors first, hedge succeeds second (in waitForResults)
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryFailsFirstHedgeSucceedsSecond(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var hedgeWon atomic.Bool
		hooks := &r8e.Hooks{
			OnHedgeWon: func() { hedgeWon.Store(true) },
		}

		var callCount atomic.Int32

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(_ context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					time.Sleep(30 * time.Millisecond)

					return "", errors.New("primary failed first")
				}
				time.Sleep(40 * time.Millisecond)

				return "hedge-won", nil
			},
			r8e.HedgeParams{
				Delay: 20 * time.Millisecond,
				Hooks: hooks,
				Clock: r8e.RealClock{},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "hedge-won", result)
		assert.True(t, hedgeWon.Load(), "OnHedgeWon should fire")
	})
}

// ---------------------------------------------------------------------------
// RecordPrimary — the adaptive-hedge latency feed (primary only, success only)
// ---------------------------------------------------------------------------

// recordedPrimary captures the (elapsed, err) a hedged call reports for its
// primary attempt.
type recordedPrimary struct {
	elapsed time.Duration
	err     error
	called  atomic.Bool
}

// TestDoHedgeRecordsPrimaryOnSuccess proves DoHedge reports the primary's own
// completion latency (and a nil error) when the primary wins — the sample the
// adaptive hedge window would record. The hedge never fires (Delay is large).
// synctest.Wait() drains the primary goroutine so the captured value/error/elapsed
// are deterministic; the record-BEFORE-send ORDERING is pinned separately by
// TestDoHedgeRecordsPrimaryBeforeDelivering (which synctest, by serializing the
// goroutine to completion, cannot distinguish).
func TestDoHedgeRecordsPrimaryOnSuccess(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec recordedPrimary

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				select {
				case <-time.After(10 * time.Millisecond):
					return "primary", nil
				case <-ctx.Done():
					return "", ctx.Err() //nolint:wrapcheck // surfacing cancellation
				}
			},
			r8e.HedgeParams{
				Delay: time.Hour,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
				RecordPrimary: func(elapsed time.Duration, err error) {
					rec.elapsed, rec.err = elapsed, err
					rec.called.Store(true)
				},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "primary", result)

		synctest.Wait() // let the primary goroutine finish recording

		require.True(t, rec.called.Load(), "RecordPrimary must be called")
		assert.NoError(t, rec.err, "a winning primary records a nil error")
		assert.Equal(t, 10*time.Millisecond, rec.elapsed, "records the primary's own latency")
	})
}

// TestDoHedgeRecordsPrimaryBeforeDelivering pins the production ORDERING that the
// adaptive hedge window relies on: the primary records its sample BEFORE sending
// its result on the channel, so the send→receive edge that delivers the result to
// a caller also publishes the recorded sample (the caller never observes a stale
// window). This runs with REAL goroutines — NOT a synctest bubble, which serializes
// the primary to completion and so cannot tell record-before-send from
// record-after-send. RecordPrimary writes a plain, deliberately non-atomic field
// that the test reads after Do returns:
//   - record BEFORE send (correct): write →hb→ send →hb→ receive →hb→ read — a clean
//     happens-before chain, race-free.
//   - record AFTER send (regression): the read races the write with no happens-before
//     between them — flagged by `go test -race`, under which CI runs this test.
//
// So a reorder of the record past the channel send fails this test under -race,
// which the synctest-based tests cannot catch.
func TestDoHedgeRecordsPrimaryBeforeDelivering(t *testing.T) {
	t.Parallel()

	var gotElapsed time.Duration // plain field on purpose: the -race tripwire

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(_ context.Context) (string, error) { return "primary", nil },
		r8e.HedgeParams{
			Delay: time.Hour,
			Hooks: &r8e.Hooks{},
			Clock: r8e.RealClock{},
			RecordPrimary: func(elapsed time.Duration, _ error) {
				gotElapsed = elapsed // must happen-before the channel send in DoHedge
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, "primary", result)

	// Reading gotElapsed is race-free ONLY because record precedes send; the value
	// itself is incidental (a small positive real-clock duration).
	assert.GreaterOrEqual(t, gotElapsed, time.Duration(0))
}

// TestDoHedgeRecordsPrimaryErrorWhenHedgeWins proves that when the hedge wins the
// race the primary is cancelled, so DoHedge reports a NON-nil error for it. The
// adaptive controller's record drops that sample, so a winning hedge never biases
// the percentile that sized its own delay.
func TestDoHedgeRecordsPrimaryErrorWhenHedgeWins(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			rec       recordedPrimary
			callCount atomic.Int32
		)

		result, err := r8e.DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					// Primary: a straggler until cancelled by the winning hedge.
					select {
					case <-time.After(time.Hour):
						return "primary", nil
					case <-ctx.Done():
						return "", ctx.Err() //nolint:wrapcheck // surfacing cancellation
					}
				}

				return "hedge", nil // the hedge completes first and wins
			},
			r8e.HedgeParams{
				Delay: 10 * time.Millisecond,
				Hooks: &r8e.Hooks{},
				Clock: r8e.RealClock{},
				RecordPrimary: func(elapsed time.Duration, err error) {
					rec.elapsed, rec.err = elapsed, err
					rec.called.Store(true)
				},
			},
		)
		require.NoError(t, err)
		require.Equal(t, "hedge", result) // the hedge produced the value

		synctest.Wait() // let the cancelled primary goroutine finish recording

		require.True(t, rec.called.Load(), "RecordPrimary must be called even when censored")
		assert.Error(t, rec.err, "a cancelled (hedge-won) primary records a non-nil error")
	})
}

// TestDoHedgeNilRecordPrimaryNoPanic proves a fixed (non-adaptive) hedge, which
// passes no RecordPrimary, runs without panicking.
func TestDoHedgeNilRecordPrimaryNoPanic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		result, err := r8e.DoHedge[string](
			context.Background(),
			func(_ context.Context) (string, error) { return "ok", nil },
			r8e.HedgeParams{Delay: time.Hour, Hooks: &r8e.Hooks{}, Clock: r8e.RealClock{}},
		)
		require.NoError(t, err)
		assert.Equal(t, "ok", result)
	})
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkDoHedge(b *testing.B) {
	hooks := &r8e.Hooks{}
	ctx := context.Background()

	for b.Loop() {
		_, _ = r8e.DoHedge[string](
			ctx,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			r8e.HedgeParams{
				Delay: time.Second,
				Hooks: hooks,
				Clock: r8e.RealClock{},
			},
		)
	}
}
