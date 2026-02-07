package r8e

import (
	"testing"
	"time"
)

func TestRealClockNow(t *testing.T) {
	c := RealClock{}
	before := time.Now()
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestRealClockSince(t *testing.T) {
	c := RealClock{}
	start := c.Now()

	// Sleep a tiny bit so Since returns a positive duration.
	time.Sleep(1 * time.Millisecond)

	elapsed := c.Since(start)
	if elapsed <= 0 {
		t.Fatalf("Since() = %v, want > 0", elapsed)
	}
}

func TestRealClockNewTimerFires(t *testing.T) {
	c := RealClock{}
	tmr := c.NewTimer(10 * time.Millisecond)

	select {
	case ts := <-tmr.C():
		if ts.IsZero() {
			t.Fatal("timer fired with zero time")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timer did not fire within 1s")
	}
}

func TestRealClockNewTimerStop(t *testing.T) {
	c := RealClock{}
	tmr := c.NewTimer(1 * time.Hour) // very long; will not fire

	if !tmr.Stop() {
		t.Fatal("Stop() = false, want true for unfired timer")
	}
}

func TestRealClockNewTimerReset(t *testing.T) {
	c := RealClock{}
	tmr := c.NewTimer(1 * time.Hour) // very long; will not fire

	tmr.Stop()

	// Reset to a short duration; timer should fire.
	tmr.Reset(10 * time.Millisecond)

	select {
	case ts := <-tmr.C():
		if ts.IsZero() {
			t.Fatal("timer fired with zero time after Reset")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timer did not fire after Reset within 1s")
	}
}

// TestFakeClockSatisfiesInterface is a compile-time check that a minimal
// fakeClock can satisfy the Clock interface. This proves the interface is
// implementable outside of the real implementation.
func TestFakeClockSatisfiesInterface(t *testing.T) {
	var _ Clock = (*fakeClock)(nil)
	var _ Timer = (*fakeTimer)(nil)
}

// fakeClock is a minimal stub that satisfies Clock for the compile check.
type fakeClock struct{}

func (f *fakeClock) Now() time.Time                        { return time.Time{} }
func (f *fakeClock) Since(time.Time) time.Duration         { return 0 }
func (f *fakeClock) NewTimer(time.Duration) Timer          { return &fakeTimer{} }

type fakeTimer struct{}

func (f *fakeTimer) C() <-chan time.Time        { return make(chan time.Time) }
func (f *fakeTimer) Stop() bool                 { return false }
func (f *fakeTimer) Reset(time.Duration) bool   { return false }

// TestRealClockConcurrentAccess verifies that concurrent reads are safe.
// RealClock is stateless (zero-value struct), so concurrent use is inherently
// safe; this test confirms it under the race detector.
func TestRealClockConcurrentAccess(t *testing.T) {
	c := RealClock{}
	done := make(chan struct{})

	for range 10 {
		go func() {
			_ = c.Now()
			_ = c.Since(time.Now())
			tmr := c.NewTimer(time.Hour)
			tmr.Stop()
			done <- struct{}{}
		}()
	}

	for range 10 {
		<-done
	}
}
