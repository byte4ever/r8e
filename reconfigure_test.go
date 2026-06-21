package r8e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int         { return &i }
func f64Ptr(f float64) *float64 { return &f }

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
	t.Parallel()

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

// TestPolicyReconfigureSlowCall reloads the slow-call-rate parameters from
// config and asserts they land on the breaker's config.
func TestPolicyReconfigureSlowCall(t *testing.T) {
	t.Parallel()

	p := kitchenSinkPolicy(t)

	err := p.Reconfigure(PolicyConfig{
		CircuitBreaker: &CircuitBreakerConfig{
			SlowCallDuration:      strPtr("2s"),
			SlowCallRateThreshold: f64Ptr(0.6),
			SlowCallWindow:        intPtr(50),
			SlowCallMinCalls:      intPtr(20),
		},
	})
	require.NoError(t, err)

	assert.Equal(t, 2*time.Second, p.circuitBreaker.cfg.slowCallDuration)
	assert.InDelta(t, 0.6, p.circuitBreaker.cfg.slowCallRateThreshold, 1e-9)
	assert.Equal(t, 50, p.circuitBreaker.cfg.slowCallWindow)
	assert.Equal(t, 20, p.circuitBreaker.cfg.slowCallMinCalls)
}

// TestSlowCallConfigIncomplete checks that supplying only one of the paired
// slow-call fields is rejected with ErrSlowCallConfigIncomplete.
func TestSlowCallConfigIncomplete(t *testing.T) {
	t.Parallel()

	_, err := BuildOptions(&PolicyConfig{
		CircuitBreaker: &CircuitBreakerConfig{SlowCallDuration: strPtr("2s")},
	})
	require.ErrorIs(t, err, ErrSlowCallConfigIncomplete)

	_, err = BuildOptions(&PolicyConfig{
		CircuitBreaker: &CircuitBreakerConfig{SlowCallRateThreshold: f64Ptr(0.5)},
	})
	require.ErrorIs(t, err, ErrSlowCallConfigIncomplete)
}

// TestSlowCallConfigBadDuration checks the duration parse error is surfaced.
func TestSlowCallConfigBadDuration(t *testing.T) {
	t.Parallel()

	_, err := BuildOptions(&PolicyConfig{
		CircuitBreaker: &CircuitBreakerConfig{
			SlowCallDuration:      strPtr("nope"),
			SlowCallRateThreshold: f64Ptr(0.5),
		},
	})
	require.Error(t, err)
	// The error must name the offending field and preserve the parse cause so an
	// operator can diagnose the bad config value.
	assert.ErrorContains(t, err, "slow_call_duration")
	assert.ErrorContains(t, err, "invalid duration")
}

// TestSlowCallConfigRoundTrip checks both paired fields plus tuners build a
// working policy whose breaker has slow-call detection enabled.
func TestSlowCallConfigRoundTrip(t *testing.T) {
	t.Parallel()

	opts, err := BuildOptions(&PolicyConfig{
		CircuitBreaker: &CircuitBreakerConfig{
			SlowCallDuration:      strPtr("150ms"),
			SlowCallRateThreshold: f64Ptr(0.5),
			SlowCallWindow:        intPtr(30),
			SlowCallMinCalls:      intPtr(5),
		},
	})
	require.NoError(t, err)

	p := NewPolicy[string]("from-config", opts...)
	assert.True(t, p.circuitBreaker.slowCallEnabled())
	assert.Equal(t, 150*time.Millisecond, p.circuitBreaker.cfg.slowCallDuration)
	assert.Equal(t, 30, p.circuitBreaker.cfg.slowCallWindow)
	assert.Equal(t, 5, p.circuitBreaker.cfg.slowCallMinCalls)
}

func TestPolicyReconfigureNilFieldsLeaveUnchanged(t *testing.T) {
	t.Parallel()

	p := kitchenSinkPolicy(t)

	require.NoError(t, p.Reconfigure(PolicyConfig{Bulkhead: intPtr(1)}))

	assert.Equal(t, int64(1), p.bulkhead.Cap())
	// Untouched patterns keep their original values.
	assert.Equal(t, int64(time.Second), p.timeout.Load())
	assert.Equal(t, 5, p.circuitBreaker.cfg.failureThreshold)
}

// TestPolicyReconfigureTransactional verifies that a validation failure on one
// field leaves every pattern unchanged (no partial application).
func TestPolicyReconfigureTransactional(t *testing.T) {
	t.Parallel()

	p := kitchenSinkPolicy(t)
	beforeTimeout := p.timeout.Load()
	beforeBulkhead := p.bulkhead.Cap()

	// A valid timeout/bulkhead alongside an invalid retry strategy: the whole
	// call must fail and nothing must change.
	err := p.Reconfigure(PolicyConfig{
		Timeout:  strPtr("9s"),
		Bulkhead: intPtr(77),
		Retry:    &RetryConfig{Backoff: strPtr("bogus"), BaseDelay: strPtr("1ms")},
	})
	require.Error(t, err)

	assert.Equal(t, beforeTimeout, p.timeout.Load(), "timeout must be unchanged")
	assert.Equal(t, beforeBulkhead, p.bulkhead.Cap(), "bulkhead must be unchanged")
}

func TestPolicyReconfigureAbsentPattern(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			err := bare.Reconfigure(cfg)
			require.ErrorIs(t, err, ErrPatternAbsent)
			assert.ErrorContains(t, err, name)
		})
	}
}

func TestPolicyReconfigureParseErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cfg     PolicyConfig
		wantSub string
	}{
		"bad timeout": {
			PolicyConfig{Timeout: strPtr("nope")},
			"reconfigure timeout",
		},
		"bad hedge": {
			PolicyConfig{Hedge: strPtr("nope")},
			"reconfigure hedge",
		},
		"bad cb recovery": {
			PolicyConfig{CircuitBreaker: &CircuitBreakerConfig{RecoveryTimeout: strPtr("nope")}},
			"circuit_breaker.recovery_timeout",
		},
		"bad retry strategy": {
			PolicyConfig{Retry: &RetryConfig{Backoff: strPtr("weird"), BaseDelay: strPtr("1ms")}},
			"unknown backoff strategy",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := kitchenSinkPolicy(t).Reconfigure(tt.cfg)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantSub)
		})
	}
}

func TestRateLimiterReconfigureClampsTokens(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(100, &stubClock{now: time.Now()}, &Hooks{})
	// The bucket starts full at 100 tokens.

	rl.Reconfigure(1) // shrink capacity below the current token count

	assert.Equal(t, int64(1)*fixedPointScale, rl.capacity.Load())
	assert.LessOrEqual(t, rl.tokens.Load(), int64(1)*fixedPointScale)
}

func TestRegistryReconfigure(t *testing.T) {
	t.Parallel()

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

// TestPolicyReconfigureConcurrentWithCalls hammers Reconfigure, Metrics, and
// Do/Snapshot from many goroutines under -race to lock in the lock-free /
// mutex-guarded reconfiguration guarantees against regression.
func TestPolicyReconfigureConcurrentWithCalls(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string]("concurrent",
		WithRegistry(reg),
		WithClock(newPolicyClock()),
		WithTimeout(time.Second),
		WithRateLimit(1000),
		WithBulkhead(100),
		WithCircuitBreaker(FailureThreshold(1000)),
		WithRetry(2, ConstantBackoff(time.Millisecond)),
	)

	const callers = 8

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range callers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				select {
				case <-stop:
					return
				default:
					_, _ = p.Do(
						context.Background(),
						func(_ context.Context) (string, error) { return "ok", nil },
					)
					_ = p.Metrics()
					_ = reg.Snapshot()
				}
			}
		}()
	}

	for i := range 200 {
		require.NoError(t, p.Reconfigure(PolicyConfig{
			Timeout:   strPtr("2s"),
			RateLimit: f64Ptr(float64(i%50 + 1)),
			Bulkhead:  intPtr(i%20 + 1),
			CircuitBreaker: &CircuitBreakerConfig{
				FailureThreshold: intPtr(i%10 + 1),
			},
		}))
	}

	close(stop)
	wg.Wait()
}
