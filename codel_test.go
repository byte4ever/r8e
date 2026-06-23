package r8e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codelBase is a fixed reference instant so the controlled-delay state machine
// is driven with deterministic timestamps.
var codelBase = time.Unix(0, 0)

// TestCoDelDisabled: a zero target leaves the discipline off and the slough
// timeout collapses to zero.
func TestCoDelDisabled(t *testing.T) {
	t.Parallel()

	var c codel

	assert.False(t, c.enabled())
	assert.Zero(t, c.sloughTimeout())
}

// TestCoDelEnabledAndSloughTimeout: a positive target enables the discipline and
// the slough timeout is twice the target (folly's getSloughTimeout).
func TestCoDelEnabledAndSloughTimeout(t *testing.T) {
	t.Parallel()

	c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}

	assert.True(t, c.enabled())
	assert.Equal(t, 10*time.Millisecond, c.sloughTimeout())
}

// TestCoDelObserveLatchesAndClears walks the RFC 8289 first-above-target test:
// a delay at or below target keeps the queue healthy, a delay above target arms
// the timer, the latch trips only once that delay has persisted a full interval,
// and a single sample back at or below target clears it at once.
func TestCoDelObserveLatchesAndClears(t *testing.T) {
	t.Parallel()

	c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}

	// Below target: healthy, timer disarmed.
	c.observe(3*time.Millisecond, codelBase)
	require.False(t, c.overloaded)
	require.True(t, c.aboveSince.IsZero())

	// Above target for the first time: arm the timer, not yet overloaded.
	at10 := codelBase.Add(10 * time.Millisecond)
	c.observe(8*time.Millisecond, at10)
	require.False(t, c.overloaded)
	require.Equal(t, at10, c.aboveSince)

	// Still above target but inside the interval: not overloaded, timer unchanged.
	c.observe(8*time.Millisecond, codelBase.Add(50*time.Millisecond))
	require.False(t, c.overloaded)
	require.Equal(t, at10, c.aboveSince, "aboveSince only set on the first crossing")

	// Above target past a full interval from aboveSince: overloaded latches.
	c.observe(8*time.Millisecond, codelBase.Add(120*time.Millisecond))
	require.True(t, c.overloaded)

	// One sample at or below target clears the overload immediately.
	c.observe(5*time.Millisecond, codelBase.Add(130*time.Millisecond))
	require.False(t, c.overloaded)
	require.True(t, c.aboveSince.IsZero())
}

// TestCoDelObserveLatchesAtExactInterval pins the persistence boundary: the latch
// trips when the above-target delay has persisted for EXACTLY a full interval
// (the comparison is >=, not >). Arming at +10ms and sampling at +110ms gives
// now-aboveSince == interval exactly.
func TestCoDelObserveLatchesAtExactInterval(t *testing.T) {
	t.Parallel()

	c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}

	c.observe(8*time.Millisecond, codelBase.Add(10*time.Millisecond)) // arm
	require.False(t, c.overloaded)

	// now - aboveSince = 110ms - 10ms = 100ms == interval → must latch.
	c.observe(8*time.Millisecond, codelBase.Add(110*time.Millisecond))
	require.True(t, c.overloaded, "a full interval (>=) at the boundary must latch overload")
}

// TestCoDelObserveAtTargetIsHealthy pins the boundary: a delay exactly equal to
// target is treated as healthy (the comparison is strictly greater-than).
func TestCoDelObserveAtTargetIsHealthy(t *testing.T) {
	t.Parallel()

	c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}

	c.observe(5*time.Millisecond, codelBase)
	assert.False(t, c.overloaded)
	assert.True(t, c.aboveSince.IsZero())
}

// TestCoDelReconfigureResetsOnChange: changing target or interval resets the
// latch; an identical pair is a no-op that preserves the armed timer.
func TestCoDelReconfigureResetsOnChange(t *testing.T) {
	t.Parallel()

	c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}

	// Arm the timer and latch overload.
	c.observe(8*time.Millisecond, codelBase.Add(10*time.Millisecond))
	c.observe(8*time.Millisecond, codelBase.Add(120*time.Millisecond))
	require.True(t, c.overloaded)
	require.False(t, c.aboveSince.IsZero())

	// No-op reconfigure preserves the latch and timer.
	c.reconfigure(5*time.Millisecond, 100*time.Millisecond)
	assert.True(t, c.overloaded)
	assert.False(t, c.aboveSince.IsZero())

	// A real change adopts the new thresholds and resets the latch and timer.
	c.reconfigure(20*time.Millisecond, 200*time.Millisecond)
	assert.Equal(t, 20*time.Millisecond, c.target)
	assert.Equal(t, 200*time.Millisecond, c.interval)
	assert.False(t, c.overloaded)
	assert.True(t, c.aboveSince.IsZero())
}

// FuzzCoDelObserve drives a stream of (standing, dt) steps through observe and
// asserts the controlled-delay invariants hold for any input: it never panics,
// the slough timeout stays 2×target, and the immediate-clear rule holds — after
// any sample at or below target the queue is never reported overloaded.
func FuzzCoDelObserve(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255, 255, 255})

	f.Fuzz(func(t *testing.T, data []byte) {
		c := codel{target: 5 * time.Millisecond, interval: 100 * time.Millisecond}
		require.Equal(t, 10*time.Millisecond, c.sloughTimeout())

		now := codelBase

		// Consume the bytes in pairs: standing delay (ms) and a time advance (ms).
		for i := 0; i+1 < len(data); i += 2 {
			standing := time.Duration(data[i]) * time.Millisecond
			now = now.Add(time.Duration(data[i+1]) * time.Millisecond)

			c.observe(standing, now)

			if standing <= c.target {
				require.False(t, c.overloaded,
					"a sample at or below target must clear overload")
			}
		}
	})
}
