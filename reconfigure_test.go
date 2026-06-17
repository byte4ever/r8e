package r8e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int          { return &i }
func f64Ptr(f float64) *float64  { return &f }

// kitchenSinkPolicy builds a policy with every reloadable pattern present.
func kitchenSinkPolicy(t *testing.T) *Policy[string] {
	t.Helper()

	return NewPolicy[string]("sink",
		WithRegistry(NewRegistry()),
		WithClock(newPolicyClock()),
		WithTimeout(time.Second),
		WithHedge(50*time.Millisecond),
		WithRateLimit(10),
		WithBulkhead(5),
		WithCircuitBreaker(FailureThreshold(5)),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
	)
}

func TestPolicyReconfigureAllPatterns(t *testing.T) {
	p := kitchenSinkPolicy(t)

	err := p.Reconfigure(PolicyConfig{
		Timeout:   strPtr("2s"),
		Hedge:     strPtr("200ms"),
		RateLimit: f64Ptr(99),
		Bulkhead:  intPtr(42),
		CircuitBreaker: &CircuitBreakerConfig{
			FailureThreshold:    intPtr(11),
			RecoveryTimeout:     strPtr("1m"),
			HalfOpenMaxAttempts: intPtr(7),
		},
		Retry: &RetryConfig{
			Backoff:     strPtr("exponential"),
			BaseDelay:   strPtr("10ms"),
			MaxAttempts: intPtr(9),
		},
	})
	require.NoError(t, err)

	assert.Equal(t, int64(2*time.Second), p.timeout.Load())
	assert.Equal(t, int64(200*time.Millisecond), p.hedge.Load())
	assert.Equal(t, int64(42), p.bulkhead.Cap())
	assert.Equal(t, 11, p.circuitBreaker.cfg.failureThreshold)
	assert.Equal(t, time.Minute, p.circuitBreaker.cfg.recoveryTimeout)
	assert.Equal(t, 7, p.circuitBreaker.cfg.halfOpenMaxAttempts)
	assert.Equal(t, 9, p.retry.Load().maxAttempts)
	// rate=99 -> capacity is 99 tokens in fixed-point.
	assert.Equal(t, int64(99)*fixedPointScale, p.rateLimiter.capacity.Load())
}

func TestPolicyReconfigureNilFieldsLeaveUnchanged(t *testing.T) {
	p := kitchenSinkPolicy(t)

	require.NoError(t, p.Reconfigure(PolicyConfig{Bulkhead: intPtr(1)}))

	assert.Equal(t, int64(1), p.bulkhead.Cap())
	// Untouched patterns keep their original values.
	assert.Equal(t, int64(time.Second), p.timeout.Load())
	assert.Equal(t, 5, p.circuitBreaker.cfg.failureThreshold)
}

func TestPolicyReconfigureAbsentPattern(t *testing.T) {
	bare := NewPolicy[string]("bare", WithRegistry(NewRegistry()))

	tests := map[string]PolicyConfig{
		"timeout":         {Timeout: strPtr("1s")},
		"hedge":           {Hedge: strPtr("1s")},
		"rate_limit":      {RateLimit: f64Ptr(1)},
		"bulkhead":        {Bulkhead: intPtr(1)},
		"circuit_breaker": {CircuitBreaker: &CircuitBreakerConfig{FailureThreshold: intPtr(1)}},
		"retry":           {Retry: &RetryConfig{Backoff: strPtr("constant"), BaseDelay: strPtr("1ms")}},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			err := bare.Reconfigure(cfg)
			require.ErrorIs(t, err, ErrPatternAbsent)
			assert.Contains(t, err.Error(), name)
		})
	}
}

func TestPolicyReconfigureParseErrors(t *testing.T) {
	p := kitchenSinkPolicy(t)

	tests := map[string]PolicyConfig{
		"bad timeout":        {Timeout: strPtr("nope")},
		"bad hedge":          {Hedge: strPtr("nope")},
		"bad cb recovery":    {CircuitBreaker: &CircuitBreakerConfig{RecoveryTimeout: strPtr("nope")}},
		"bad retry strategy": {Retry: &RetryConfig{Backoff: strPtr("weird"), BaseDelay: strPtr("1ms")}},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			require.Error(t, p.Reconfigure(cfg))
		})
	}
}

func TestRateLimiterReconfigureClampsTokens(t *testing.T) {
	rl := NewRateLimiter(100, &stubClock{now: time.Now()}, &Hooks{})
	// The bucket starts full at 100 tokens.

	rl.Reconfigure(1) // shrink capacity below the current token count

	assert.Equal(t, int64(1)*fixedPointScale, rl.capacity.Load())
	assert.LessOrEqual(t, rl.tokens.Load(), int64(1)*fixedPointScale)
}

func TestRegistryReconfigure(t *testing.T) {
	reg := NewRegistry()
	_ = NewPolicy[string]("svc", WithRegistry(reg), WithBulkhead(2))

	require.NoError(t, reg.Reconfigure("svc", PolicyConfig{Bulkhead: intPtr(20)}))

	// Verify via a fresh snapshot that the live bulkhead capacity changed.
	var found bool
	for _, m := range reg.Snapshot() {
		if m.Name == "svc" {
			found = true

			assert.Equal(t, int64(20), m.BulkheadCap)
		}
	}
	require.True(t, found)

	err := reg.Reconfigure("missing", PolicyConfig{Bulkhead: intPtr(1)})
	require.ErrorIs(t, err, ErrPolicyNotRegistered)
}
