package r8e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// Tests: Primary wins fast (before hedge delay) -> returns primary result
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryWinsFast(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "primary" {
		t.Fatalf("DoHedge() = %q, want %q", result, "primary")
	}
	if hedgeTriggered.Load() {
		t.Fatal("OnHedgeTriggered should not be called when primary wins fast")
	}
}

// ---------------------------------------------------------------------------
// Tests: Primary slow + hedge wins -> OnHedgeTriggered + OnHedgeWon emitted
// ---------------------------------------------------------------------------

func TestDoHedgePrimarySlowHedgeWins(t *testing.T) {
	var hedgeTriggered atomic.Bool
	var hedgeWon atomic.Bool
	hooks := &r8e.Hooks{
		OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
		OnHedgeWon:       func() { hedgeWon.Store(true) },
	}

	callCount := atomic.Int32{}

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: very slow, will be cancelled
				select {
				case <-time.After(5 * time.Second):
					return "primary-late", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			// Secondary: returns immediately
			return "hedge", nil
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "hedge" {
		t.Fatalf("DoHedge() = %q, want %q", result, "hedge")
	}
	if !hedgeTriggered.Load() {
		t.Fatal("OnHedgeTriggered should be called when hedge fires")
	}
	if !hedgeWon.Load() {
		t.Fatal("OnHedgeWon should be called when hedge wins")
	}
}

// ---------------------------------------------------------------------------
// Tests: Primary slow + primary still wins after hedge triggered
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryWinsAfterHedgeTriggered(t *testing.T) {
	var hedgeTriggered atomic.Bool
	var hedgeWon atomic.Bool
	hooks := &r8e.Hooks{
		OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
		OnHedgeWon:       func() { hedgeWon.Store(true) },
	}

	callCount := atomic.Int32{}

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: a bit slow but completes
				time.Sleep(40 * time.Millisecond)
				return "primary", nil
			}
			// Secondary: very slow, will be cancelled
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
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "primary" {
		t.Fatalf("DoHedge() = %q, want %q", result, "primary")
	}
	if !hedgeTriggered.Load() {
		t.Fatal("OnHedgeTriggered should be called when hedge fires")
	}
	if hedgeWon.Load() {
		t.Fatal("OnHedgeWon should NOT be called when primary wins")
	}
}

// ---------------------------------------------------------------------------
// Tests: Both fail -> returns error
// ---------------------------------------------------------------------------

func TestDoHedgeBothFail(t *testing.T) {
	hooks := &r8e.Hooks{}
	callCount := atomic.Int32{}

	_, err := r8e.DoHedge[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: slow then fails
				time.Sleep(40 * time.Millisecond)
				return "", errors.New("primary error")
			}
			// Secondary: fails fast
			return "", errors.New("hedge error")
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)

	if err == nil {
		t.Fatal("DoHedge() error = nil, want non-nil")
	}
}

// ---------------------------------------------------------------------------
// Tests: Context cancellation -> returns ctx.Err()
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancellation(t *testing.T) {
	hooks := &r8e.Hooks{}

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
		r8e.HedgeParams{Delay: time.Hour, Hooks: hooks, Clock: r8e.RealClock{}},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoHedge() error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Context already cancelled -> returns ctx.Err() immediately
// ---------------------------------------------------------------------------

func TestDoHedgeContextAlreadyCancelled(t *testing.T) {
	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r8e.DoHedge[string](
		ctx,
		func(_ context.Context) (string, error) {
			return "should-not-run", nil
		},
		r8e.HedgeParams{Delay: time.Hour, Hooks: hooks, Clock: r8e.RealClock{}},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoHedge() error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestDoHedgeNilHooksDoNotPanic(t *testing.T) {
	hooks := &r8e.Hooks{} // all nil callbacks

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		r8e.HedgeParams{Delay: time.Hour, Hooks: hooks, Clock: r8e.RealClock{}},
	)
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "ok" {
		t.Fatalf("DoHedge() = %q, want %q", result, "ok")
	}
}

// ---------------------------------------------------------------------------
// Tests: Primary errors but hedge succeeds
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryErrorHedgeSucceeds(t *testing.T) {
	hooks := &r8e.Hooks{}
	callCount := atomic.Int32{}

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: slow then errors
				time.Sleep(40 * time.Millisecond)
				return "", errors.New("primary failed")
			}
			// Secondary: succeeds
			return "hedge-ok", nil
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "hedge-ok" {
		t.Fatalf("DoHedge() = %q, want %q", result, "hedge-ok")
	}
}

// ---------------------------------------------------------------------------
// Tests: Primary fails fast (before hedge delay) -> returns error, no hedge
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryFailsFast(t *testing.T) {
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

	if !errors.Is(err, sentinel) {
		t.Fatalf("DoHedge() error = %v, want %v", err, sentinel)
	}
	if hedgeTriggered.Load() {
		t.Fatal("OnHedgeTriggered should not be called when primary fails fast")
	}
}

// ---------------------------------------------------------------------------
// Tests: Hedge fails first, primary succeeds second -> primary result returned
// ---------------------------------------------------------------------------

func TestDoHedgeHedgeFailsPrimarySucceeds(t *testing.T) {
	hooks := &r8e.Hooks{}
	callCount := atomic.Int32{}

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: slow but succeeds
				time.Sleep(60 * time.Millisecond)
				return "primary-ok", nil
			}
			// Secondary: fails fast
			return "", errors.New("hedge failed")
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "primary-ok" {
		t.Fatalf("DoHedge() = %q, want %q", result, "primary-ok")
	}
}

// ---------------------------------------------------------------------------
// Tests: Context cancelled after hedge triggered (during wait for results)
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancelledAfterHedgeTriggered(t *testing.T) {
	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())
	callCount := atomic.Int32{}

	_, err := r8e.DoHedge[string](
		ctx,
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: blocks until cancelled
				<-ctx.Done()
				return "", ctx.Err()
			}
			// Secondary: cancel the parent, then block
			cancel()
			<-ctx.Done()
			return "", ctx.Err()
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoHedge() error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Context cancelled while waiting for second result after first fails
// ---------------------------------------------------------------------------

func TestDoHedgeContextCancelledWhileWaitingSecondResult(t *testing.T) {
	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())
	callCount := atomic.Int32{}

	_, err := r8e.DoHedge[string](
		ctx,
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: blocks forever
				<-ctx.Done()
				return "", ctx.Err()
			}
			// Secondary: fail fast, then parent will be cancelled
			go func() {
				time.Sleep(30 * time.Millisecond)
				cancel()
			}()
			return "", errors.New("hedge failed")
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoHedge() error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Primary errors first, hedge succeeds second (in waitForResults)
// ---------------------------------------------------------------------------

func TestDoHedgePrimaryFailsFirstHedgeSucceedsSecond(t *testing.T) {
	var hedgeWon atomic.Bool
	hooks := &r8e.Hooks{
		OnHedgeWon: func() { hedgeWon.Store(true) },
	}
	callCount := atomic.Int32{}

	result, err := r8e.DoHedge[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			n := callCount.Add(1)
			if n == 1 {
				// Primary: fails quickly after hedge delay
				time.Sleep(30 * time.Millisecond)
				return "", errors.New("primary failed first")
			}
			// Secondary: succeeds, but takes a bit longer than primary's error
			time.Sleep(40 * time.Millisecond)
			return "hedge-won", nil
		},
		r8e.HedgeParams{
			Delay: 20 * time.Millisecond,
			Hooks: hooks,
			Clock: r8e.RealClock{},
		},
	)
	if err != nil {
		t.Fatalf("DoHedge() error = %v, want nil", err)
	}
	if result != "hedge-won" {
		t.Fatalf("DoHedge() = %q, want %q", result, "hedge-won")
	}
	if !hedgeWon.Load() {
		t.Fatal(
			"OnHedgeWon should be called when hedge succeeds as second result",
		)
	}
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
