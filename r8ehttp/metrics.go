package r8ehttp

import (
	"encoding/json"
	"net/http"

	"github.com/byte4ever/r8e"
)

// MetricsHandler returns an [http.Handler] that serves a JSON array of
// [r8e.PolicyMetrics] — one entry per registered policy — from reg. It is a
// lightweight debug/observability endpoint; for a metrics pipeline use the
// r8eotel package instead.
func MetricsHandler(reg *r8e.Registry) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		//nolint:errcheck // best-effort JSON encoding to HTTP response
		_ = json.NewEncoder(writer).Encode(reg.Snapshot())
	})
}
