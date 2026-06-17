// Package r8ehttp provides the HTTP edge adapters for the r8e resilience
// library, keeping net/http out of the core policy package.
package r8ehttp

import (
	"encoding/json"
	"net/http"

	"github.com/byte4ever/r8e"
)

// ReadinessHandler returns an [http.Handler] that reports the readiness of
// all policies registered with reg. It responds with 200 OK when all critical
// policies are healthy, and 503 Service Unavailable otherwise. The response
// body is always a JSON-encoded [r8e.ReadinessStatus].
func ReadinessHandler(reg *r8e.Registry) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := reg.CheckReadiness()

		writer.Header().Set("Content-Type", "application/json")

		if status.Ready {
			writer.WriteHeader(http.StatusOK)
		} else {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}

		//nolint:errcheck // best-effort JSON encoding to HTTP response
		_ = json.NewEncoder(writer).Encode(status)
	})
}
