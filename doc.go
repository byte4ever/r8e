// Package r8e provides composable resilience patterns for Go applications.
//
// The central type is Policy[T], which wraps function calls with patterns
// like retry, circuit breaker, timeout, rate limiting, and more. Policies
// automatically report health status for Kubernetes readiness probes.
package r8e
