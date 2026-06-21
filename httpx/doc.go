// Package httpx provides a resilient HTTP client adapter
// for the r8e library.
//
// Client wraps a standard http.Client with an r8e resilience
// policy and a user-provided status code classifier that maps
// HTTP response codes to transient or permanent errors.
//
// Each retry attempt replays the request body via req.GetBody, so a
// retried request carrying a body (POST/PUT) resends it correctly; a
// body without GetBody cannot be replayed.
package httpx
