package r8ehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8ehttp"
)

// TestReadinessHandlerAllHealthy verifies that when all registered policies
// are healthy the handler returns 200 OK with Ready=true.
func TestReadinessHandlerAllHealthy(t *testing.T) {
	reg := r8e.NewRegistry()

	// Register a healthy policy.
	_ = r8e.NewPolicy[string]("api-1",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var status r8e.ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !status.Ready {
		t.Fatal("Ready = false, want true")
	}
	if len(status.Policies) != 1 {
		t.Fatalf("len(Policies) = %d, want 1", len(status.Policies))
	}
	if status.Policies[0].Name != "api-1" {
		t.Fatalf(
			"Policies[0].Name = %q, want %q",
			status.Policies[0].Name,
			"api-1",
		)
	}
}

// TestReadinessHandlerOneCritical verifies that when one policy has an open
// circuit breaker the handler returns 503 with Ready=false.
func TestReadinessHandlerOneCritical(t *testing.T) {
	reg := r8e.NewRegistry()

	policy := r8e.NewPolicy[string]("api-down",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Hour),
		),
	)

	// Drive circuit to open.
	ctx := context.Background()
	for range 2 {
		_, _ = policy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var status r8e.ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if status.Ready {
		t.Fatal("Ready = true, want false")
	}
}

// TestReadinessHandlerEmptyRegistry verifies that an empty registry
// yields 200 OK with Ready=true and an empty Policies slice.
func TestReadinessHandlerEmptyRegistry(t *testing.T) {
	reg := r8e.NewRegistry()

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var status r8e.ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !status.Ready {
		t.Fatal("Ready = false, want true")
	}
	if len(status.Policies) != 0 {
		t.Fatalf("len(Policies) = %d, want 0", len(status.Policies))
	}
}

// TestReadinessHandlerJSONStructure verifies the JSON body contains the
// expected fields: name, healthy, criticality, and state.
func TestReadinessHandlerJSONStructure(t *testing.T) {
	reg := r8e.NewRegistry()

	_ = r8e.NewPolicy[string]("svc-a",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Top-level keys.
	for _, key := range []string{"ready", "policies"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing top-level key %q", key)
		}
	}

	// Policy-level keys.
	var policies []map[string]json.RawMessage
	if err := json.Unmarshal(raw["policies"], &policies); err != nil {
		t.Fatalf("unmarshal policies: %v", err)
	}
	if len(policies) == 0 {
		t.Fatal("expected at least one policy in JSON")
	}
	for _, key := range []string{"name", "healthy", "criticality", "state"} {
		if _, ok := policies[0][key]; !ok {
			t.Fatalf("missing policy key %q", key)
		}
	}
}

// TestReadinessHandlerContentType verifies the Content-Type header is
// application/json.
func TestReadinessHandlerContentType(t *testing.T) {
	reg := r8e.NewRegistry()

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

// TestReadinessHandlerServerEndToEnd drives the handler through a real HTTP
// server: healthy → 200, then an opened circuit → 503.
func TestReadinessHandlerServerEndToEnd(t *testing.T) {
	reg := r8e.NewRegistry()

	healthy := r8e.NewPolicy[string]("healthy-service",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(5),
			r8e.RecoveryTimeout(time.Hour),
		),
	)
	unhealthy := r8e.NewPolicy[string]("unhealthy-service",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Hour),
		),
	)

	srv := httptest.NewServer(r8ehttp.ReadinessHandler(reg))
	defer srv.Close()

	ctx := context.Background()

	// All healthy → 200.
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Drive the unhealthy service's circuit open; keep the other one healthy.
	for range 2 {
		_, _ = unhealthy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("failure")
		})
	}
	_, _ = healthy.Do(ctx, func(_ context.Context) (string, error) {
		return "ok", nil
	})

	// One critical circuit open → 503.
	resp2, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp2.StatusCode)
	}

	var status r8e.ReadinessStatus
	if err := json.NewDecoder(resp2.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if status.Ready {
		t.Fatal("Ready = true, want false when a circuit is open")
	}
}

// BenchmarkReadinessHandler benchmarks the readiness handler with a single
// registered policy.
func BenchmarkReadinessHandler(b *testing.B) {
	reg := r8e.NewRegistry()

	_ = r8e.NewPolicy[string]("bench-policy",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
