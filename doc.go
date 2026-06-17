// Package r8e provides composable resilience patterns for Go applications.
//
// The central type is Policy[T], which wraps function calls with patterns
// like retry, circuit breaker, timeout, rate limiting, and more. Policies
// automatically report health status for Kubernetes readiness probes.
//
// This package depends only on the standard library. File-based configuration
// loading lives in the r8econf subpackage (os + JSON), and the readiness HTTP
// handler lives in the r8ehttp subpackage (net/http), keeping infrastructure
// out of the core.
package r8e
