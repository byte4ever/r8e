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

// TestRetryBudgetParentLink: Parent nests a budget under another; Parent(nil)
// (the default) leaves it a root.
func TestRetryBudgetParentLink(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(10))
	child := NewRetryBudget(MaxTokens(4), Parent(root))

	assert.Same(t, root, child.parent)
	assert.Nil(t, NewRetryBudget(MaxTokens(4)).parent)
	assert.Nil(t, NewRetryBudget(MaxTokens(4), Parent(nil)).parent)
}

// TestRetryBudgetRecordPropagatesUp: a leaf's outcomes are recorded against
// every ancestor, each crediting a success by its OWN ratio.
func TestRetryBudgetRecordPropagatesUp(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(10), TokenRatio(0.1)) // starts 10
	mid := NewRetryBudget(MaxTokens(8), TokenRatio(0.2), Parent(root))
	leaf := NewRetryBudget(MaxTokens(4), TokenRatio(0.5), Parent(mid))

	// One failure charges every level by a whole token.
	leaf.recordFailure()
	assert.InDelta(t, 3.0, leaf.Tokens(), 1e-9)
	assert.InDelta(t, 7.0, mid.Tokens(), 1e-9)
	assert.InDelta(t, 9.0, root.Tokens(), 1e-9)

	// One success credits each level by that level's own ratio.
	leaf.recordSuccess()
	assert.InDelta(t, 3.5, leaf.Tokens(), 1e-9) // +0.5
	assert.InDelta(t, 7.2, mid.Tokens(), 1e-9)  // +0.2
	assert.InDelta(t, 9.1, root.Tokens(), 1e-9) // +0.1
}

// TestRetryBudgetAllowRetryANDsAcrossChain: a retry is permitted only when the
// child AND every ancestor allow it — an exhausted level anywhere on the path
// blocks it.
func TestRetryBudgetAllowRetryANDsAcrossChain(t *testing.T) {
	t.Parallel()

	t.Run("both healthy allows", func(t *testing.T) {
		t.Parallel()

		root := NewRetryBudget(MaxTokens(10))
		child := NewRetryBudget(MaxTokens(10), Parent(root))

		assert.True(t, child.allowRetry())
	})

	t.Run("exhausted child blocks even with a healthy parent", func(t *testing.T) {
		t.Parallel()

		root := NewRetryBudget(MaxTokens(100))
		child := NewRetryBudget(MaxTokens(4), Parent(root))

		// Drain the child below half (its failures also charge root, but root is
		// far larger so it stays healthy).
		for range 3 {
			child.recordFailure()
		}

		require.True(t, root.allowRetry(), "root must still be healthy")
		assert.False(t, child.allowRetry(), "an exhausted child blocks regardless of parent")
	})

	t.Run("exhausted parent blocks even with a healthy child", func(t *testing.T) {
		t.Parallel()

		root := NewRetryBudget(MaxTokens(4))
		child := NewRetryBudget(MaxTokens(100), Parent(root))

		// Drain only the parent (directly), leaving the child full.
		for range 4 {
			root.recordFailure()
		}

		require.True(t, child.tokens > child.maxTokens/2, "child must be healthy")
		assert.False(t, child.allowRetry(), "an exhausted ancestor blocks the child")
	})
}

// TestRetryBudgetExhaustedIsLocalNotChain pins that Exhausted() reports only THIS
// bucket, not the chain: a child blocked solely by an exhausted ancestor reports
// healthy, even though allowRetry (the chain) denies it. This guards against
// "restoring" the pre-tree `Exhausted = !allowRetry()` identity, which would flood
// a whole subtree's health with the parent's single exhaustion instead of
// pinpointing the drained level.
func TestRetryBudgetExhaustedIsLocalNotChain(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(4))
	child := NewRetryBudget(MaxTokens(100), Parent(root))

	for range 4 {
		root.recordFailure() // drain ONLY the parent, leaving the child full
	}

	require.False(t, child.allowRetry(), "the chain denies the retry (parent exhausted)")
	assert.False(t, child.Exhausted(), "Exhausted is LOCAL: the child's own bucket is healthy")
	assert.True(t, root.Exhausted(), "the parent reports its own exhaustion")
}

// TestRetryBudgetSiblingThrottledBySharedParent: a retry storm in one child
// drains the shared parent and throttles its sibling — amplification cannot
// cascade out of one leaf.
func TestRetryBudgetSiblingThrottledBySharedParent(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(10), TokenRatio(0.1))
	noisy := NewRetryBudget(MaxTokens(100), Parent(root))
	quiet := NewRetryBudget(MaxTokens(100), Parent(root))

	// The noisy child storms: six failures drain the shared root to 4 (<= half),
	// while the noisy child itself (cap 100) stays healthy.
	for range 6 {
		noisy.recordFailure()
	}

	require.True(t, noisy.tokens > noisy.maxTokens/2, "the noisy child is locally healthy")
	assert.True(t, root.Exhausted(), "the shared parent is drained")
	assert.False(t, quiet.allowRetry(), "the quiet sibling is throttled via the shared parent")
}

// TestRetryBudgetReconfigureLeavesParentAndIgnoresParentOption: reconfiguring a
// child does not touch its parent, and Parent cannot be changed via Reconfigure.
func TestRetryBudgetReconfigureLeavesParentAndIgnoresParentOption(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(10))
	other := NewRetryBudget(MaxTokens(10))
	child := NewRetryBudget(MaxTokens(4), Parent(root))

	child.Reconfigure(MaxTokens(8), Parent(other))

	assert.InDelta(t, 8.0, child.Tokens(), 1e-9)
	assert.Same(t, root, child.parent, "Parent is immutable; Reconfigure must not rewire it")
	assert.InDelta(t, 10.0, root.Tokens(), 1e-9, "reconfiguring the child leaves the parent untouched")
}

// TestRetryBudgetNestedConcurrent: a parent shared across goroutines (as several
// child policies would) stays consistent under concurrent record/allow, with no
// data race and the token invariant preserved.
func TestRetryBudgetNestedConcurrent(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(1000), TokenRatio(0.5))
	children := []*RetryBudget{
		NewRetryBudget(MaxTokens(100), Parent(root)),
		NewRetryBudget(MaxTokens(100), Parent(root)),
		NewRetryBudget(MaxTokens(100), Parent(root)),
	}

	var wg sync.WaitGroup

	for _, child := range children {
		wg.Add(1)

		go func(b *RetryBudget) {
			defer wg.Done()

			for range 1000 {
				b.recordFailure()
				b.recordSuccess()
				_ = b.allowRetry()
			}
		}(child)
	}

	wg.Wait()

	assert.True(t, budgetWithinBounds(root), "root token invariant holds")

	for _, child := range children {
		assert.True(t, budgetWithinBounds(child), "child token invariant holds")
	}
}

// TestPolicyNestedRetryBudgetThrottlesSibling: end-to-end through the policy
// chain — one policy's retry storm drains the shared parent so a sibling policy
// (its own child budget healthy) has its very first failure not retried.
func TestPolicyNestedRetryBudgetThrottlesSibling(t *testing.T) {
	t.Parallel()

	root := NewRetryBudget(MaxTokens(4), TokenRatio(0.1))

	noisy := NewPolicy[string]("",
		WithClock(newImmediateTestClock()),
		WithRetry(10, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(NewRetryBudget(MaxTokens(100), Parent(root))),
	)

	_, err := noisy.Do(context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("down"))
		})
	require.ErrorContains(t, err, "down", "suppression surfaces the real downstream error")
	require.True(t, root.Exhausted(), "the noisy policy drains the shared parent")

	// A sibling policy with its own full child budget is still throttled, because
	// the shared parent is exhausted: its first failure is not retried.
	quietChild := NewRetryBudget(MaxTokens(100), Parent(root))
	quiet := NewPolicy[string]("",
		WithClock(newImmediateTestClock()),
		WithRetry(10, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(quietChild),
	)

	var attempts atomic.Int64

	_, err = quiet.Do(context.Background(),
		func(_ context.Context) (string, error) {
			attempts.Add(1)

			return "", Transient(errors.New("down"))
		})
	require.ErrorContains(t, err, "down", "the suppressed sibling still surfaces the real downstream error")
	assert.Equal(t, int64(1), attempts.Load(), "sibling's retry is suppressed by the shared parent")
	require.False(t, quietChild.Exhausted(), "the sibling's own budget is still healthy")
}
