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
