package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dead child that shields its parent drains it by half of its own capacity,
// then stops: the parent keeps allowing its other children's retries.
func TestShieldParentBoundsTheDrain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       []RetryBudgetOption
		wantParent float64
	}{
		{name: "shielded", opts: []RetryBudgetOption{ShieldParent()}, wantParent: 95},
		{name: "not shielded", wantParent: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parent := NewRetryBudget(MaxTokens(100))
			child := NewRetryBudget(append([]RetryBudgetOption{MaxTokens(10), Parent(parent)}, tt.opts...)...)

			for range 1000 {
				child.recordFailure()
			}

			assert.InDelta(t, tt.wantParent, parent.Tokens(), 1e-9)
			assert.InDelta(t, 0.0, child.Tokens(), 1e-9)
		})
	}
}

// Once successes bring the shielded child back above half its capacity, its
// failures debit the parent again.
func TestShieldParentResumesAboveThreshold(t *testing.T) {
	t.Parallel()

	parent := NewRetryBudget(MaxTokens(100), TokenRatio(1))
	child := NewRetryBudget(MaxTokens(10), TokenRatio(1), Parent(parent), ShieldParent())

	for range 10 {
		child.recordFailure()
	}

	require.InDelta(t, 95.0, parent.Tokens(), 1e-9, "5 failures reach the parent, then the child is exhausted")
	require.True(t, child.Exhausted())

	for range 6 {
		child.recordSuccess()
	}

	require.False(t, child.Exhausted(), "6 tokens: back above half of 10")
	require.InDelta(t, 100.0, parent.Tokens(), 1e-9, "successes are always credited to the parent")

	child.recordFailure()
	assert.InDelta(t, 99.0, parent.Tokens(), 1e-9, "the failure above the threshold reaches the parent")

	child.recordFailure()
	assert.InDelta(t, 99.0, parent.Tokens(), 1e-9, "the child is at half again: shielded")
}

// ShieldParent on a root budget is accepted and changes nothing.
func TestShieldParentOnARootIsANoOp(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(10), ShieldParent())
	for range 20 {
		root.recordFailure()
	}

	assert.InDelta(t, 0.0, root.Tokens(), 1e-9, "a root drains as usual")
}

// Reconfigure neither adds nor removes the shield.
func TestShieldParentAcrossReconfigure(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts       []RetryBudgetOption
		reconfig   []RetryBudgetOption
		wantParent float64
	}{
		"not added":   {reconfig: []RetryBudgetOption{ShieldParent()}, wantParent: 80},
		"not removed": {opts: []RetryBudgetOption{ShieldParent()}, reconfig: []RetryBudgetOption{MaxTokens(10)}, wantParent: 95},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parent := NewRetryBudget(MaxTokens(100))
			child := NewRetryBudget(append([]RetryBudgetOption{MaxTokens(10), Parent(parent)}, tt.opts...)...)
			child.Reconfigure(tt.reconfig...)

			for range 20 {
				child.recordFailure()
			}

			assert.InDelta(t, tt.wantParent, parent.Tokens(), 1e-9)
		})
	}
}

// The shield follows Exhausted exactly: walking a shielded child from full to
// empty, a failure reaches the parent if and only if the child was not
// exhausted when it arrived.
func TestShieldParentFollowsExhausted(t *testing.T) {
	t.Parallel()

	parent := NewRetryBudget(MaxTokens(100))
	child := NewRetryBudget(MaxTokens(7), Parent(parent), ShieldParent())

	for range 10 {
		exhausted := child.Exhausted()
		before := parent.Tokens()

		child.recordFailure()

		reached := parent.Tokens() < before
		assert.Equal(t, !exhausted, reached, "child tokens %.1f", child.Tokens()+1)
	}
}

// A shielded middle budget stops its whole subtree's failures from reaching
// the root once it is exhausted.
func TestShieldParentOnAMiddleBudget(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(100))
	mid := NewRetryBudget(MaxTokens(10), Parent(root), ShieldParent())
	leaf := NewRetryBudget(MaxTokens(1000), Parent(mid))

	for range 50 {
		leaf.recordFailure()
	}

	assert.InDelta(t, 95.0, root.Tokens(), 1e-9)
	assert.InDelta(t, 950.0, leaf.Tokens(), 1e-9)
}

// Concurrent outcomes on a shared shielded child keep every bucket within its
// bounds, and concurrent failures alone still let exactly half the child's
// capacity through to the parent.
func TestShieldParentConcurrent(t *testing.T) {
	t.Parallel()

	t.Run("mixed outcomes", func(t *testing.T) {
		t.Parallel()

		parent := NewRetryBudget(MaxTokens(1000), TokenRatio(0.5))
		child := NewRetryBudget(MaxTokens(100), Parent(parent), ShieldParent())

		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() {
				for range 1000 {
					child.recordFailure()
					child.recordSuccess()
					_ = child.allowRetry()
				}
			})
		}

		wg.Wait()

		assert.True(t, budgetWithinBounds(parent), "parent token invariant holds")
		assert.True(t, budgetWithinBounds(child), "child token invariant holds")
	})

	t.Run("failures only", func(t *testing.T) {
		t.Parallel()

		parent := NewRetryBudget(MaxTokens(100))
		child := NewRetryBudget(MaxTokens(10), Parent(parent), ShieldParent())

		var wg sync.WaitGroup

		for range 8 {
			wg.Go(func() {
				for range 100 {
					child.recordFailure()
				}
			})
		}

		wg.Wait()

		assert.InDelta(t, 95.0, parent.Tokens(), 1e-9)
	})
}

// Through policies: a dead route whose budget shields the shared parent stops
// draining it, so a healthy sibling keeps its retries.
func TestPolicyShieldParentKeepsSiblingRetries(t *testing.T) {
	t.Parallel()

	parent := NewRetryBudget(MaxTokens(20))

	dead := NewPolicy[string]("",
		WithClock(newImmediateTestClock()),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(NewRetryBudget(MaxTokens(10), Parent(parent), ShieldParent())),
	)

	for range 100 {
		_, err := dead.Do(context.Background(), func(context.Context) (string, error) {
			return "", errors.New("down")
		})
		require.Error(t, err)
	}

	require.False(t, parent.Exhausted(), "the shielded route drained the parent by half its own capacity only")

	sibling := NewPolicy[string]("",
		WithClock(newImmediateTestClock()),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(NewRetryBudget(MaxTokens(10), Parent(parent))),
	)

	var attempts atomic.Int64

	result, err := sibling.Do(context.Background(), func(context.Context) (string, error) {
		if attempts.Add(1) == 1 {
			return "", errors.New("blip")
		}

		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, int64(2), attempts.Load(), "the sibling's retry is still allowed")
}
