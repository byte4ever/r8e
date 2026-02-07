package r8e

import (
	"encoding/json"
	"net/http"
)

// ReadinessHandler returns an [http.Handler] that reports the readiness of
// all policies registered with reg. It responds with 200 OK when all critical
// policies are healthy, and 503 Service Unavailable otherwise. The response
// body is always a JSON-encoded [ReadinessStatus].
func ReadinessHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := reg.CheckReadiness()

		w.Header().Set("Content-Type", "application/json")
		if status.Ready {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		_ = json.NewEncoder(w).Encode(status)
	})
}
