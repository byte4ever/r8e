package r8e

import "time"

// Pattern: Factory Function — each preset produces a ready-made option bundle
// for a common use case, avoiding boilerplate configuration.

// StandardHTTPClient returns options suitable for a typical HTTP client:
// 5s timeout, retry 3 times with 100ms exponential backoff, and a circuit
// breaker with 5-failure threshold and 30s recovery.
func StandardHTTPClient() []any {
	return []any{
		WithTimeout(5 * time.Second),
		WithRetry(3, ExponentialBackoff(100*time.Millisecond)),
		WithCircuitBreaker(
			FailureThreshold(5),
			RecoveryTimeout(30*time.Second),
		),
	}
}

// AggressiveHTTPClient returns options for latency-sensitive HTTP clients:
// 2s timeout, retry 5 times with 50ms exponential backoff capped at 5s,
// circuit breaker with 3-failure threshold and 15s recovery, and a bulkhead
// of 20 concurrent calls.
func AggressiveHTTPClient() []any {
	return []any{
		WithTimeout(2 * time.Second),
		WithRetry(
			5,
			ExponentialBackoff(50*time.Millisecond),
			MaxDelay(5*time.Second),
		),
		WithCircuitBreaker(
			FailureThreshold(3),
			RecoveryTimeout(15*time.Second),
		),
		WithBulkhead(20),
	}
}
