package r8e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestReadinessHandlerAllHealthy verifies that when all registered policies
// are healthy the handler returns 200 OK with Ready=true.
func TestReadinessHandlerAllHealthy(t *testing.T) {
	reg := NewRegistry()
	clk := newPolicyClock()

	// Register a healthy policy.
	_ = NewPolicy[string]("api-1",
		WithRegistry(reg),
		WithClock(clk),
		WithCircuitBreaker(),
	)

	handler := ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var rs ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&rs); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !rs.Ready {
		t.Fatal("Ready = false, want true")
	}
	if len(rs.Policies) != 1 {
		t.Fatalf("len(Policies) = %d, want 1", len(rs.Policies))
	}
	if rs.Policies[0].Name != "api-1" {
		t.Fatalf("Policies[0].Name = %q, want %q", rs.Policies[0].Name, "api-1")
	}
}

// TestReadinessHandlerOneCritical verifies that when one policy has an open
// circuit breaker the handler returns 503 with Ready=false.
func TestReadinessHandlerOneCritical(t *testing.T) {
	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("api-down",
		WithRegistry(reg),
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Drive circuit to open.
	ctx := context.Background()
	for range 2 {
		_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	handler := ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var rs ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&rs); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if rs.Ready {
		t.Fatal("Ready = true, want false")
	}
}

// TestReadinessHandlerEmptyRegistry verifies that an empty registry
// yields 200 OK with Ready=true and an empty Policies slice.
func TestReadinessHandlerEmptyRegistry(t *testing.T) {
	reg := NewRegistry()

	handler := ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var rs ReadinessStatus
	if err := json.NewDecoder(rec.Body).Decode(&rs); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !rs.Ready {
		t.Fatal("Ready = false, want true")
	}
	if len(rs.Policies) != 0 {
		t.Fatalf("len(Policies) = %d, want 0", len(rs.Policies))
	}
}

// TestReadinessHandlerJSONStructure verifies the JSON body contains the
// expected fields: name, healthy, criticality, and state.
func TestReadinessHandlerJSONStructure(t *testing.T) {
	reg := NewRegistry()
	clk := newPolicyClock()

	_ = NewPolicy[string]("svc-a",
		WithRegistry(reg),
		WithClock(clk),
		WithCircuitBreaker(),
	)

	handler := ReadinessHandler(reg)
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
	reg := NewRegistry()

	handler := ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
	}
}

// BenchmarkReadinessHandler benchmarks the readiness handler with a single
// registered policy.
func BenchmarkReadinessHandler(b *testing.B) {
	reg := NewRegistry()
	clk := newPolicyClock()

	_ = NewPolicy[string]("bench-policy",
		WithRegistry(reg),
		WithClock(clk),
		WithCircuitBreaker(),
	)

	handler := ReadinessHandler(reg)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
