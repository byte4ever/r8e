package r8ehttp

import (
	"encoding/json"
	"net/http"

	"github.com/byte4ever/r8e"
)

// HealthHandler returns an [http.Handler] that serves the aggregate health of
// all policies registered with reg as JSON ([r8e.HealthReport]).
//
// Unlike [ReadinessHandler] it always responds 200 OK — it is an informational
// endpoint for dashboards and monitoring. Do NOT wire it to the Kubernetes
// readiness probe; use [ReadinessHandler] for that, and gate individual
// policies with r8e.WithReadinessImpact.
func HealthHandler(reg *r8e.Registry) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		//nolint:errcheck // best-effort JSON encoding to HTTP response
		_ = json.NewEncoder(writer).Encode(reg.Health())
	})
}
