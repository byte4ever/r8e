// Package r8e provides composable resilience patterns for Go applications.
//
// The central type is Policy[T], which wraps function calls with patterns
// like retry, circuit breaker, timeout, rate limiting, and more. Policies
// automatically report health status for Kubernetes readiness probes.
//
// This package depends only on the standard library and imports no transport
// or persistence machinery: file loading lives in the r8econf subpackage and
// the HTTP probe handlers live in the r8ehttp subpackage (net/http). The core
// status types do carry json struct tags — JSON is the canonical wire format
// for the Kubernetes probes this library exists to feed — but the core itself
// performs no serialization and starts no server.
package r8e
