package r8e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// ---------------------------------------------------------------------------
// TestIntegrationFullChainSuccess — Full policy chain, success on 2nd attempt
// ---------------------------------------------------------------------------

func TestIntegrationFullChainSuccess(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	var (
		retryCount atomic.Int32
		bhAcquired atomic.Int32
		bhReleased atomic.Int32
	)

	hooks := Hooks{
		OnRetry:            func(_ int, _ error) { retryCount.Add(1) },
		OnBulkheadAcquired: func() { bhAcquired.Add(1) },
		OnBulkheadReleased: func() { bhReleased.Add(1) },
	}

	p := NewPolicy[string]("full-chain",
		WithClock(clk),
		WithRegistry(reg),
		WithHooks(&hooks),
		WithTimeout(5*time.Second),
		WithCircuitBreaker(FailureThreshold(10), RecoveryTimeout(time.Hour)),
		WithRetry(3, ConstantBackoff(10*time.Millisecond)),
		WithBulkhead(5),
		WithRateLimit(1000),
	)

	attempt := 0
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 2 {
				return "", errors.New("transient failure")
			}
			return "success", nil
		},
	)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "success" {
		t.Fatalf("Do() = %q, want %q", result, "success")
	}
	if attempt != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempt)
	}
	// Retry hook should have fired once (before the 2nd attempt).
	if rc := retryCount.Load(); rc != 1 {
		t.Fatalf("OnRetry called %d times, want 1", rc)
	}
	// Bulkhead should have been acquired and released.
	if bh := bhAcquired.Load(); bh < 1 {
		t.Fatalf("OnBulkheadAcquired called %d times, want >= 1", bh)
	}
	if bh := bhReleased.Load(); bh < 1 {
		t.Fatalf("OnBulkheadReleased called %d times, want >= 1", bh)
	}

	// Circuit breaker should still be closed (only 1 failure out of threshold
	// 10).
	status := p.HealthStatus()
	if !status.Healthy {
		t.Fatalf("policy should be healthy, got state=%q", status.State)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationConfigWithCodeOverrides — LoadConfig + GetPolicy + hooks
// ---------------------------------------------------------------------------

func TestIntegrationConfigWithCodeOverrides(t *testing.T) {
	clk := newPolicyClock()

	// Load the existing testdata/valid.json config.
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	// Create a child policy to use as a dependency.
	childReg := NewRegistry()
	child := NewPolicy[int]("child-service",
		WithClock(clk),
		WithRegistry(childReg),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	var hookCalled atomic.Bool
	hooks := Hooks{
		OnRetry: func(_ int, _ error) { hookCalled.Store(true) },
	}

	// GetPolicy from config, adding code-level overrides: clock, hooks,
	// dependency.
	p := GetPolicy[string](reg, "notification-api",
		WithClock(clk),
		WithHooks(&hooks),
		DependsOn(child),
	)

	// The policy should work with both config-derived and code-level options.
	attempt := 0
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 2 {
				return "", errors.New("transient")
			}
			return "config-override-success", nil
		},
	)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "config-override-success" {
		t.Fatalf("Do() = %q, want %q", result, "config-override-success")
	}
	if !hookCalled.Load() {
		t.Fatal("OnRetry hook should have been called from code override")
	}

	// Verify DependsOn is wired — health status should list the child
	// dependency.
	status := p.HealthStatus()
	if len(status.Dependencies) == 0 {
		t.Fatal("expected at least one dependency in health status")
	}
	if status.Dependencies[0].Name != "child-service" {
		t.Fatalf(
			"dependency name = %q, want %q",
			status.Dependencies[0].Name,
			"child-service",
		)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationPresetPolicy — Use preset + override
// ---------------------------------------------------------------------------

func TestIntegrationPresetPolicy(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	// Use StandardHTTPClient preset.
	presetOpts := StandardHTTPClient()

	// Append overrides: custom clock, registry, and a fallback.
	opts := append(presetOpts,
		WithClock(clk),
		WithRegistry(reg),
		WithFallback("preset-fallback"),
	)

	p := NewPolicy[string]("preset-policy", opts...)

	// Successful call.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "from-preset", nil
		},
	)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "from-preset" {
		t.Fatalf("Do() = %q, want %q", result, "from-preset")
	}

	// Verify the preset included expected patterns.
	patternNames := make(map[string]bool)
	for _, e := range p.entries {
		patternNames[e.Name] = true
	}
	for _, expected := range []string{"timeout", "retry", "circuit_breaker", "fallback"} {
		if !patternNames[expected] {
			t.Errorf("missing preset pattern %q", expected)
		}
	}

	// Verify the fallback override works: fail enough to exhaust retries +
	// trigger circuit breaker, then fallback catches.
	for range 10 {
		_, _ = p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("always fail")
			},
		)
	}

	result, err = p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("still failing")
		},
	)
	if err != nil {
		t.Fatalf("Do() with fallback error = %v, want nil", err)
	}
	if result != "preset-fallback" {
		t.Fatalf("Do() = %q, want %q (fallback)", result, "preset-fallback")
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationDoConvenience — r8e.Do[T] with retry + timeout
// ---------------------------------------------------------------------------

func TestIntegrationDoConvenience(t *testing.T) {
	clk := newPolicyClock()

	attempt := 0
	result, err := Do[string](context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("transient")
			}
			return "convenience-ok", nil
		},
		WithClock(clk),
		WithRetry(5, ConstantBackoff(10*time.Millisecond)),
		WithTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "convenience-ok" {
		t.Fatalf("Do() = %q, want %q", result, "convenience-ok")
	}
	if attempt != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempt)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationConcurrentPolicy — 100 goroutines, no races, no panics
// ---------------------------------------------------------------------------

func TestIntegrationConcurrentPolicy(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	p := NewPolicy[int]("concurrent-integ",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(200), RecoveryTimeout(time.Hour)),
		WithRetry(2, ConstantBackoff(1*time.Millisecond)),
		WithBulkhead(50),
		WithRateLimit(10000),
	)

	const numGoroutines = 100
	var (
		wg        sync.WaitGroup
		successes atomic.Int64
		failures  atomic.Int64
	)

	wg.Add(numGoroutines)
	for i := range numGoroutines {
		go func(n int) {
			defer wg.Done()
			result, err := p.Do(
				context.Background(),
				func(_ context.Context) (int, error) {
					// Alternate between success and failure to exercise both
					// paths.
					if n%3 == 0 {
						return 0, errors.New("simulated failure")
					}
					return n, nil
				},
			)
			if err != nil {
				failures.Add(1)
			} else {
				_ = result
				successes.Add(1)
			}
		}(i)
	}

	wg.Wait()

	total := successes.Load() + failures.Load()
	if total != numGoroutines {
		t.Fatalf("total outcomes = %d, want %d", total, numGoroutines)
	}

	// Some should have succeeded (2/3 of inputs succeed on first try).
	if successes.Load() == 0 {
		t.Fatal("expected at least some successes")
	}

	t.Logf(
		"concurrent results: %d successes, %d failures",
		successes.Load(),
		failures.Load(),
	)
}

// ---------------------------------------------------------------------------
// TestIntegrationHierarchicalHealth — Parent depends on child, CB opens
// ---------------------------------------------------------------------------

func TestIntegrationHierarchicalHealth(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	// Create a child policy with a circuit breaker.
	child := NewPolicy[string]("child-db",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Create a parent policy that depends on the child.
	parent := NewPolicy[string]("parent-api",
		WithClock(clk),
		WithRegistry(reg),
		DependsOn(child),
	)

	// Initially both should be healthy.
	readiness := reg.CheckReadiness()
	if !readiness.Ready {
		t.Fatal("registry should initially report ready")
	}

	parentHealth := parent.HealthStatus()
	if !parentHealth.Healthy {
		t.Fatal("parent should initially be healthy")
	}

	// Drive the child's circuit breaker to open.
	for range 2 {
		_, _ = child.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("child failure")
			},
		)
	}

	// Verify child is unhealthy.
	childHealth := child.HealthStatus()
	if childHealth.Healthy {
		t.Fatal("child should be unhealthy (circuit open)")
	}
	if childHealth.Criticality != CriticalityCritical {
		t.Fatalf(
			"child criticality = %v, want CriticalityCritical",
			childHealth.Criticality,
		)
	}

	// Verify parent health reports degraded due to dependency.
	parentHealth = parent.HealthStatus()
	if parentHealth.Criticality < CriticalityDegraded {
		t.Fatalf(
			"parent criticality = %v, want >= CriticalityDegraded",
			parentHealth.Criticality,
		)
	}
	if len(parentHealth.Dependencies) == 0 {
		t.Fatal("parent should have dependencies in health status")
	}
	depHealth := parentHealth.Dependencies[0]
	if depHealth.Healthy {
		t.Fatal("dependency health should report unhealthy")
	}

	// Verify registry-level readiness reports not ready (child is
	// critical+unhealthy).
	readiness = reg.CheckReadiness()
	if readiness.Ready {
		t.Fatal("registry should report not ready when child circuit is open")
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationPermanentErrorStopsRetry — Permanent error stops retry
// ---------------------------------------------------------------------------

func TestIntegrationPermanentErrorStopsRetry(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	p := NewPolicy[string]("perm-stop",
		WithClock(clk),
		WithRegistry(reg),
		WithRetry(5, ConstantBackoff(10*time.Millisecond)),
	)

	attempt := 0
	sentinel := errors.New("fatal database error")
	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			// Return a permanent error on the first attempt.
			return "", Permanent(sentinel)
		},
	)

	// Should have been called exactly once — permanent stops retry immediately.
	if attempt != 1 {
		t.Fatalf(
			"expected 1 attempt, got %d (permanent should stop retry)",
			attempt,
		)
	}

	// The error should be the permanent-wrapped sentinel.
	if !IsPermanent(err) {
		t.Fatalf("error should be permanent, got %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error should unwrap to sentinel, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationFallbackCatchesAllErrors — Fallback catches retries + CB
// ---------------------------------------------------------------------------

func TestIntegrationFallbackCatchesAllErrors(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	var fallbackErrors []error
	var mu sync.Mutex

	p := NewPolicy[string]("fallback-all",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(3), RecoveryTimeout(time.Hour)),
		WithRetry(2, ConstantBackoff(10*time.Millisecond)),
		WithFallbackFunc[string](func(err error) (string, error) {
			mu.Lock()
			fallbackErrors = append(fallbackErrors, err)
			mu.Unlock()
			return "fallback-value", nil
		}),
	)

	// Exhaust retries several times to drive circuit to open.
	for range 3 {
		result, err := p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("always fail")
			},
		)
		if err != nil {
			t.Fatalf("Do() error = %v, want nil (fallback should catch)", err)
		}
		if result != "fallback-value" {
			t.Fatalf("Do() = %q, want %q", result, "fallback-value")
		}
	}

	// At this point, the circuit breaker should be open (3 failures recorded
	// because retry exhausts 2 attempts per Do, and the CB sees the final
	// failure
	// from the retry layer).
	// The next call should get ErrCircuitOpen caught by fallback.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when circuit is open")
			return "unreachable", nil
		},
	)
	if err != nil {
		t.Fatalf(
			"Do() error = %v, want nil (fallback should catch circuit open)",
			err,
		)
	}
	if result != "fallback-value" {
		t.Fatalf("Do() = %q, want %q", result, "fallback-value")
	}

	// Verify fallback caught different types of errors.
	mu.Lock()
	defer mu.Unlock()
	if len(fallbackErrors) < 2 {
		t.Fatalf(
			"expected at least 2 fallback calls, got %d",
			len(fallbackErrors),
		)
	}

	// At least one should be ErrRetriesExhausted and at least one
	// ErrCircuitOpen.
	var gotRetries, gotCircuit bool
	for _, e := range fallbackErrors {
		if errors.Is(e, ErrRetriesExhausted) {
			gotRetries = true
		}
		if errors.Is(e, ErrCircuitOpen) {
			gotCircuit = true
		}
	}
	if !gotRetries {
		t.Fatal("fallback should have caught ErrRetriesExhausted")
	}
	if !gotCircuit {
		t.Fatal("fallback should have caught ErrCircuitOpen")
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationReadinessHTTPEndpoint — Full HTTP endpoint test
// ---------------------------------------------------------------------------

func TestIntegrationReadinessHTTPEndpoint(t *testing.T) {
	clk := newPolicyClock()
	reg := NewRegistry()

	// Create two policies in the same registry.
	healthy := NewPolicy[string]("healthy-service",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(5), RecoveryTimeout(time.Hour)),
	)
	unhealthy := NewPolicy[string]("unhealthy-service",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Step 1: All healthy — verify 200 response.
	handler := ReadinessHandler(reg)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	var readiness ReadinessStatus
	if err := json.Unmarshal(body, &readiness); err != nil {
		t.Fatalf("JSON unmarshal error = %v", err)
	}
	if !readiness.Ready {
		t.Fatal("readiness should be true when all policies are healthy")
	}
	if len(readiness.Policies) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(readiness.Policies))
	}

	// Step 2: Drive the unhealthy service's circuit breaker to open.
	for range 2 {
		_, _ = unhealthy.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("failure")
			},
		)
	}

	// Keep the healthy service healthy by making a successful call.
	_, _ = healthy.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
	)

	// Step 3: Hit the endpoint again — should return 503.
	resp2, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d",
			resp2.StatusCode,
			http.StatusServiceUnavailable,
		)
	}

	body2, _ := io.ReadAll(resp2.Body)
	var readiness2 ReadinessStatus
	if err := json.Unmarshal(body2, &readiness2); err != nil {
		t.Fatalf("JSON unmarshal error = %v", err)
	}
	if readiness2.Ready {
		t.Fatal("readiness should be false when a circuit is open")
	}

	// Verify the JSON body contains the unhealthy policy.
	var foundUnhealthy bool
	for _, ps := range readiness2.Policies {
		if ps.Name == "unhealthy-service" && !ps.Healthy {
			foundUnhealthy = true
		}
	}
	if !foundUnhealthy {
		t.Fatal(
			"expected unhealthy-service to appear as unhealthy in JSON body",
		)
	}
}
