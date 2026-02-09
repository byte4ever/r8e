package r8e

import (
	"net/http"

	json "github.com/goccy/go-json"
)

// ReadinessHandler returns an [http.Handler] that reports the readiness of
// all policies registered with reg. It responds with 200 OK when all critical
// policies are healthy, and 503 Service Unavailable otherwise. The response
// body is always a JSON-encoded [ReadinessStatus].
func ReadinessHandler(reg *Registry) http.Handler {
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
