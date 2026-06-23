package httpx_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// fixedClock reports a fixed instant so InjectDeadline computes a deterministic
// remaining budget independent of the wall clock.
type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time                  { return c.now }
func (c fixedClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

//nolint:ireturn // satisfies the r8e.Timer interface; unused by these helpers
func (fixedClock) NewTimer(d time.Duration) r8e.Timer {
	return r8e.RealClock{}.NewTimer(d)
}

func newRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://svc/x", nil)
	require.NoError(t, err)

	return req
}

func TestInjectDeadline(t *testing.T) {
	t.Parallel()

	clk := fixedClock{now: time.Unix(1_700_000_000, 0)}

	t.Run("writes the remaining budget in milliseconds", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithDeadline(
			context.Background(), clk.now.Add(2*time.Second),
		)
		defer cancel()

		req := newRequest(t, ctx)

		require.True(t, httpx.InjectDeadline(req, clk))
		assert.Equal(t, "2000", req.Header.Get(httpx.DeadlineHeader))
	})

	t.Run("no deadline writes nothing", func(t *testing.T) {
		t.Parallel()

		req := newRequest(t, context.Background())

		require.False(t, httpx.InjectDeadline(req, clk))
		assert.Empty(t, req.Header.Get(httpx.DeadlineHeader))
	})

	t.Run("a spent budget floors to 1ms", func(t *testing.T) {
		t.Parallel()

		// Sub-millisecond and already-past budgets still propagate as "almost no
		// time" rather than vanishing into an absent header.
		ctx, cancel := context.WithDeadline(
			context.Background(), clk.now.Add(500*time.Microsecond),
		)
		defer cancel()

		req := newRequest(t, ctx)

		require.True(t, httpx.InjectDeadline(req, clk))
		assert.Equal(t, "1", req.Header.Get(httpx.DeadlineHeader))
	})
}

func TestExtractDeadline(t *testing.T) {
	t.Parallel()

	t.Run("reconstructs a bounded context", func(t *testing.T) {
		t.Parallel()

		req := newRequest(t, context.Background())
		req.Header.Set(httpx.DeadlineHeader, "1500")

		ctx, cancel := httpx.ExtractDeadline(context.Background(), req)
		defer cancel()

		deadline, ok := ctx.Deadline()
		require.True(t, ok)

		remaining := time.Until(deadline)
		assert.Positive(t, remaining)
		assert.LessOrEqual(t, remaining, 1500*time.Millisecond)
		assert.Greater(t, remaining, time.Second, "the ~1.5s window should be intact")
	})

	t.Run("no header returns the parent unchanged", func(t *testing.T) {
		t.Parallel()

		parent := context.Background()
		req := newRequest(t, parent)

		ctx, cancel := httpx.ExtractDeadline(parent, req)
		defer cancel()

		assert.Equal(t, parent, ctx)
		_, ok := ctx.Deadline()
		assert.False(t, ok)
	})

	t.Run("an invalid header is ignored", func(t *testing.T) {
		t.Parallel()

		parent := context.Background()

		for _, value := range []string{"not-a-number", "0", "-5", ""} {
			req := newRequest(t, parent)
			if value != "" {
				req.Header.Set(httpx.DeadlineHeader, value)
			}

			ctx, cancel := httpx.ExtractDeadline(parent, req)
			cancel()

			_, ok := ctx.Deadline()
			assert.Falsef(t, ok, "value %q must not set a deadline", value)
		}
	})

	t.Run("an overflowing header is clamped, not overflowed", func(t *testing.T) {
		t.Parallel()

		req := newRequest(t, context.Background())
		// Far past what fits in a time.Duration; a naive multiply would wrap
		// negative and expire instantly.
		req.Header.Set(httpx.DeadlineHeader, strconv.FormatInt(int64(1)<<62, 10))

		ctx, cancel := httpx.ExtractDeadline(context.Background(), req)
		defer cancel()

		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.True(t, deadline.After(time.Now()),
			"clamped deadline must be in the future, never an instant expiry")
	})
}

func TestDeadlineRoundTrip(t *testing.T) {
	t.Parallel()

	// Egress on the caller, ingress on the callee: a budget set at the edge
	// survives the wire as a relative value and reconstructs into a live
	// deadline on the other side.
	clk := fixedClock{now: time.Now()}

	ctx, cancel := context.WithDeadline(context.Background(), clk.now.Add(3*time.Second))
	defer cancel()

	req := newRequest(t, ctx)
	require.True(t, httpx.InjectDeadline(req, clk))

	serverCtx, serverCancel := httpx.ExtractDeadline(context.Background(), req)
	defer serverCancel()

	deadline, ok := serverCtx.Deadline()
	require.True(t, ok)

	remaining := time.Until(deadline)
	assert.Greater(t, remaining, 2*time.Second)
	assert.LessOrEqual(t, remaining, 3*time.Second)
}

// FuzzExtractDeadline asserts the header parser never panics and never
// reconstructs a deadline in the past — the clamp and the positive-value guard
// must keep any bound it sets strictly in the future, whatever the header says.
func FuzzExtractDeadline(f *testing.F) {
	for _, seed := range []string{
		"1500", "1", "0", "-5", "not-a-number", "",
		strconv.FormatInt(int64(1)<<62, 10),
		strconv.FormatInt(int64(9_223_372_036_854_775_807), 10),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, header string) {
		req := newRequest(t, context.Background())
		req.Header.Set(httpx.DeadlineHeader, header)

		before := time.Now()

		ctx, cancel := httpx.ExtractDeadline(context.Background(), req)
		defer cancel()

		if deadline, ok := ctx.Deadline(); ok && deadline.Before(before) {
			t.Fatalf("header %q reconstructed a past deadline %v", header, deadline)
		}
	})
}
