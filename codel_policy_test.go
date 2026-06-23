package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildOptionsBulkheadCoDel: a config with both controlled-delay fields plus
// a bulkhead builds a working policy whose bulkhead queues under load.
func TestBuildOptionsBulkheadCoDel(t *testing.T) {
	t.Parallel()

	bulkhead := 2
	target := "5ms"
	interval := "100ms"

	opts, err := BuildOptions(&PolicyConfig{
		Bulkhead:              &bulkhead,
		BulkheadCoDelTarget:   &target,
		BulkheadCoDelInterval: &interval,
	})
	require.NoError(t, err)

	p := NewPolicy[string]("codel-cfg", append(opts, WithClock(newPolicyClock()))...)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// TestBuildOptionsBulkheadCoDelQueueDepthNoMaxWait: a queue depth is accepted
// alongside the controlled-delay discipline without a fixed max-wait — CoDel
// counts as the wait the depth bounds.
func TestBuildOptionsBulkheadCoDelQueueDepthNoMaxWait(t *testing.T) {
	t.Parallel()

	bulkhead := 2
	depth := 8
	target := "5ms"
	interval := "100ms"

	opts, err := BuildOptions(&PolicyConfig{
		Bulkhead:              &bulkhead,
		BulkheadQueueDepth:    &depth,
		BulkheadCoDelTarget:   &target,
		BulkheadCoDelInterval: &interval,
	})
	require.NoError(t, err)
	require.NotEmpty(t, opts)
}

// TestBuildOptionsBulkheadCoDelErrorPaths covers every failure of the
// controlled-delay config: an incomplete pair, unparseable durations, and the
// fields without a bulkhead to apply to.
func TestBuildOptionsBulkheadCoDelErrorPaths(t *testing.T) {
	t.Parallel()

	bulkhead := 2
	good := "5ms"
	bad := "not-a-duration"

	tests := []struct {
		name    string
		pc      *PolicyConfig
		wantSub string
	}{
		{
			"target without interval",
			&PolicyConfig{Bulkhead: &bulkhead, BulkheadCoDelTarget: &good},
			"must be set together",
		},
		{
			"interval without target",
			&PolicyConfig{Bulkhead: &bulkhead, BulkheadCoDelInterval: &good},
			"must be set together",
		},
		{
			"bad target",
			&PolicyConfig{Bulkhead: &bulkhead, BulkheadCoDelTarget: &bad, BulkheadCoDelInterval: &good},
			"bulkhead_codel_target",
		},
		{
			"bad interval",
			&PolicyConfig{Bulkhead: &bulkhead, BulkheadCoDelTarget: &good, BulkheadCoDelInterval: &bad},
			"bulkhead_codel_interval",
		},
		{
			"codel without bulkhead",
			&PolicyConfig{BulkheadCoDelTarget: &good, BulkheadCoDelInterval: &good},
			"require a bulkhead",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildOptions(tt.pc)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantSub)
		})
	}
}

// TestBuildOptionsBulkheadCoDelIncompleteSentinel pins the sentinel identity of
// the incomplete-pair error.
func TestBuildOptionsBulkheadCoDelIncompleteSentinel(t *testing.T) {
	t.Parallel()

	bulkhead := 2
	good := "5ms"

	_, err := BuildOptions(&PolicyConfig{
		Bulkhead:            &bulkhead,
		BulkheadCoDelTarget: &good,
	})
	require.ErrorIs(t, err, ErrBulkheadCoDelConfigIncomplete)
}

// TestBuildOptionsBulkheadCoDelBadDurationWraps: a parse failure on a CoDel
// duration field is wrapped with %w (not flattened with %v), so the cause stays
// reachable through the error chain.
func TestBuildOptionsBulkheadCoDelBadDurationWraps(t *testing.T) {
	t.Parallel()

	bulkhead := 2
	bad := "not-a-duration"
	good := "5ms"

	_, err := BuildOptions(&PolicyConfig{
		Bulkhead:              &bulkhead,
		BulkheadCoDelTarget:   &bad,
		BulkheadCoDelInterval: &good,
	})
	require.Error(t, err)
	require.NotNil(t, errors.Unwrap(err),
		"parse error must be wrapped with %%w so the cause survives the chain")
	// Pin the actual parse cause, not merely "something is wrapped": a re-wrap
	// with a fresh error would still be non-nil but would lose this message.
	require.ErrorContains(t, err, "bulkhead_codel_target")
	require.ErrorContains(t, err, "invalid duration")
}

// TestPolicyReconfigureBulkheadCoDel: hot-reloading the controlled-delay fields
// re-tunes the existing bulkhead.
func TestPolicyReconfigureBulkheadCoDel(t *testing.T) {
	t.Parallel()

	cc := newPolicyClock()
	p := NewPolicy[string]("codel-reload",
		WithClock(cc),
		WithBulkhead(1, BulkheadCoDel(5*time.Millisecond, 100*time.Millisecond)),
	)

	target := "20ms"
	interval := "200ms"

	require.NoError(t, p.Reconfigure(PolicyConfig{
		Bulkhead:              intPtr(1),
		BulkheadCoDelTarget:   &target,
		BulkheadCoDelInterval: &interval,
	}))

	assert.Equal(t, 20*time.Millisecond, p.bulkhead.codel.target)
	assert.Equal(t, 200*time.Millisecond, p.bulkhead.codel.interval)
}

// TestPolicyBulkheadOverloadedHealth drives the bulkhead into the controlled-delay
// overloaded state and asserts HealthStatus reports it as a degradation.
func TestPolicyBulkheadOverloadedHealth(t *testing.T) {
	t.Parallel()

	cc := newPolicyClock()
	p := NewPolicy[string]("codel-health",
		WithClock(cc),
		WithBulkhead(1,
			BulkheadCoDel(5*time.Millisecond, 100*time.Millisecond),
			BulkheadQueueDepth(10)),
	)
	bh := p.bulkhead

	require.NoError(t, bh.Acquire(context.Background())) // hold the slot

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Arm the overload timer, then serve the arming caller (FIFO).
	arm := enqueueOnBulkhead(t, bh, ctx, 1)
	cc.advance(10 * time.Millisecond)
	bh.Release()
	require.NoError(t, <-arm)

	// Two stale callers and two fresh ones: the release sheds the stale and serves
	// a fresh one, leaving the queue non-empty and the latch overloaded.
	cc.advance(5 * time.Millisecond) // 15ms
	staleA := enqueueOnBulkhead(t, bh, ctx, 1)
	staleB := enqueueOnBulkhead(t, bh, ctx, 2)

	cc.advance(103 * time.Millisecond) // 118ms
	freshE := enqueueOnBulkhead(t, bh, ctx, 3)
	enqueueOnBulkhead(t, bh, ctx, 4) // newest, served LIFO

	cc.advance(2 * time.Millisecond) // 120ms
	bh.Release()

	require.ErrorIs(t, <-staleA, ErrCoDelShed)
	require.ErrorIs(t, <-staleB, ErrCoDelShed)
	require.True(t, bh.Overloaded())

	status := p.HealthStatus()
	assert.Contains(t, status.Conditions, ConditionBulkheadOverloaded)
	assert.Equal(t, CriticalityDegraded, status.Criticality)

	cancel()
	assert.ErrorIs(t, <-freshE, context.Canceled)
}

// enqueueOnBulkhead spawns bh.Acquire and blocks until it has joined the wait
// queue, returning a channel carrying its eventual result.
func enqueueOnBulkhead(
	t *testing.T,
	bh *Bulkhead,
	ctx context.Context,
	wantQueued int64,
) <-chan error {
	t.Helper()

	res := make(chan error, 1)
	go func() { res <- bh.Acquire(ctx) }()

	require.Eventually(t, func() bool { return bh.Queued() == wantQueued },
		time.Second, time.Millisecond, "waiter did not enqueue")

	return res
}
