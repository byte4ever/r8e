// Package httpx provides a resilient HTTP client adapter
// for the r8e library.
//
// Client wraps a standard http.Client with an r8e resilience
// policy and a user-provided status code classifier that maps
// HTTP response codes to transient or permanent errors.
package httpx
