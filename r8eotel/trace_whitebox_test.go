package r8eotel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/byte4ever/r8e"
)

func TestRejectionReason(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want string
	}{
		"circuit_open":         {r8e.ErrCircuitOpen, "circuit_open"},
		"rate_limited":         {r8e.ErrRateLimited, "rate_limited"},
		"bulkhead_full":        {r8e.ErrBulkheadFull, "bulkhead_full"},
		"bulkhead_timeout":     {r8e.ErrBulkheadTimeout, "bulkhead_timeout"},
		"codel_shed":           {r8e.ErrCoDelShed, "codel_shed"},
		"concurrency_limited":  {r8e.ErrConcurrencyLimited, "concurrency_limited"},
		"throttled":            {r8e.ErrThrottled, "throttled"},
		"timeout":              {r8e.ErrTimeout, "timeout"},
		"time_budget_exceeded": {r8e.ErrTimeBudgetExceeded, "time_budget_exceeded"},
		"retries_exhausted":    {r8e.ErrRetriesExhausted, "retries_exhausted"},
		"plain_error":          {errors.New("something else"), "error"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, rejectionReason(tc.err))
		})
	}
}

// TestRejectionReasonWrapped verifies that wrapped sentinels are still
// classified correctly (errors.Is walks the chain).
func TestRejectionReasonWrapped(t *testing.T) {
	t.Parallel()

	wrapped := errors.Join(errors.New("outer"), r8e.ErrRetriesExhausted)
	assert.Equal(t, "retries_exhausted", rejectionReason(wrapped))
}
