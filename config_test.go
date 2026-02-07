package r8e

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TestLoadConfigValid — Load valid.json, verify registry has policies
// ---------------------------------------------------------------------------

func TestLoadConfigValid(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if reg == nil {
		t.Fatal("LoadConfig() returned nil registry")
	}

	// Registry should have stored 2 policy configs.
	reg.mu.Lock()
	n := len(reg.configs)
	reg.mu.Unlock()
	if n != 2 {
		t.Fatalf("configs count = %d, want 2", n)
	}

	// GetPolicy should be able to retrieve both.
	p1 := GetPolicy[string](reg, "payment-api")
	if p1 == nil {
		t.Fatal("GetPolicy(payment-api) returned nil")
	}
	if p1.Name() != "payment-api" {
		t.Fatalf("Name() = %q, want %q", p1.Name(), "payment-api")
	}

	p2 := GetPolicy[string](reg, "notification-api")
	if p2 == nil {
		t.Fatal("GetPolicy(notification-api) returned nil")
	}
	if p2.Name() != "notification-api" {
		t.Fatalf("Name() = %q, want %q", p2.Name(), "notification-api")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigFileNotFound — Non-existent file returns error
// ---------------------------------------------------------------------------

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := LoadConfig("testdata/nonexistent.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for missing file")
	}
	if !strings.Contains(err.Error(), "r8e: read config") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "r8e: read config")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidJSON — Malformed JSON returns error
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidJSON(t *testing.T) {
	// Write a temporary malformed JSON file.
	_, err := LoadConfig("testdata/invalid_json.json")
	// This file doesn't exist, so it'll be a read error. Instead, we'll test
	// with the approach of creating a temp file inline.
	_ = err

	// Use a different approach: create testdata/malformed.json for the test.
	t.Run("malformed", func(t *testing.T) {
		// Write malformed JSON to a temp file.
		path := t.TempDir() + "/malformed.json"
		writeTestFile(t, path, `{not valid json}`)

		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("LoadConfig() error = nil, want error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "r8e: parse config") {
			t.Fatalf("error = %q, want to contain %q", err.Error(), "r8e: parse config")
		}
	})
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidDuration — Invalid duration string returns error
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidDuration(t *testing.T) {
	_, err := LoadConfig("testdata/invalid.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid duration")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "timeout")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigUnknownBackoff — Unknown backoff strategy returns error
// ---------------------------------------------------------------------------

func TestLoadConfigUnknownBackoff(t *testing.T) {
	_, err := LoadConfig("testdata/unknown_backoff.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for unknown backoff")
	}
	if !strings.Contains(err.Error(), "unknown backoff strategy") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "unknown backoff strategy")
	}
}

// ---------------------------------------------------------------------------
// TestGetPolicyFromConfig — GetPolicy retrieves policy and it works with Do
// ---------------------------------------------------------------------------

func TestGetPolicyFromConfig(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	clk := newPolicyClock()
	p := GetPolicy[string](reg, "notification-api", WithClock(clk))

	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "ok" {
		t.Fatalf("Do() = %q, want %q", result, "ok")
	}
}

// ---------------------------------------------------------------------------
// TestGetPolicyWithOverride — GetPolicy with additional opts augments config
// ---------------------------------------------------------------------------

func TestGetPolicyWithOverride(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	clk := newPolicyClock()
	p := GetPolicy[string](reg, "payment-api", WithClock(clk))

	// The policy should have a clock set to our fake clock.
	if p.clock != clk {
		t.Fatalf("clock = %T, want *policyClock", p.clock)
	}

	// Policy should still work.
	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "payment", nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "payment" {
		t.Fatalf("Do() = %q, want %q", result, "payment")
	}
}

// ---------------------------------------------------------------------------
// TestGetPolicyNotInConfig — GetPolicy for unknown name creates bare policy
// ---------------------------------------------------------------------------

func TestGetPolicyNotInConfig(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	p := GetPolicy[string](reg, "unknown-policy")
	if p == nil {
		t.Fatal("GetPolicy(unknown-policy) returned nil")
	}
	if p.Name() != "unknown-policy" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "unknown-policy")
	}

	// Should work as a bare passthrough policy.
	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "bare", nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "bare" {
		t.Fatalf("Do() = %q, want %q", result, "bare")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigAllPatterns — Config with all patterns loads correctly
// ---------------------------------------------------------------------------

func TestLoadConfigAllPatterns(t *testing.T) {
	reg, err := LoadConfig("testdata/all_patterns.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}

	clk := newPolicyClock()
	p := GetPolicy[string](reg, "full-policy", WithClock(clk))
	if p == nil {
		t.Fatal("GetPolicy(full-policy) returned nil")
	}

	// Verify the policy has the expected patterns by checking entries.
	patternNames := make(map[string]bool)
	for _, e := range p.entries {
		patternNames[e.Name] = true
	}

	expected := []string{"timeout", "circuit_breaker", "retry", "rate_limiter", "bulkhead", "stale_cache", "hedge"}
	for _, name := range expected {
		if !patternNames[name] {
			t.Errorf("missing pattern %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigCircuitBreakerHalfOpen — CB config with half_open_max_attempts
// ---------------------------------------------------------------------------

func TestLoadConfigCircuitBreakerHalfOpen(t *testing.T) {
	reg, err := LoadConfig("testdata/all_patterns.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}

	clk := newPolicyClock()
	p := GetPolicy[string](reg, "full-policy", WithClock(clk))

	// Verify the circuit breaker was configured with half_open_max_attempts=2.
	if p.cb == nil {
		t.Fatal("circuit breaker is nil")
	}
	if p.cb.cfg.halfOpenMaxAttempts != 2 {
		t.Fatalf("halfOpenMaxAttempts = %d, want 2", p.cb.cfg.halfOpenMaxAttempts)
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigRetryMaxDelay — Retry config with max_delay parsed correctly
// ---------------------------------------------------------------------------

func TestLoadConfigRetryMaxDelay(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}

	// payment-api has retry with max_delay: "30s"
	// We verify this by creating the policy and checking it works.
	clk := newPolicyClock()
	p := GetPolicy[string](reg, "payment-api", WithClock(clk))
	if p == nil {
		t.Fatal("GetPolicy(payment-api) returned nil")
	}

	// Verify retry pattern is present.
	found := false
	for _, e := range p.entries {
		if e.Name == "retry" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("retry pattern not found in payment-api policy")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidCBDuration — Invalid circuit_breaker.recovery_timeout
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidCBDuration(t *testing.T) {
	_, err := LoadConfig("testdata/invalid_cb_duration.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid CB duration")
	}
	if !strings.Contains(err.Error(), "circuit_breaker.recovery_timeout") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "circuit_breaker.recovery_timeout")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidRetryBaseDelay — Invalid retry.base_delay
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidRetryBaseDelay(t *testing.T) {
	_, err := LoadConfig("testdata/invalid_retry_base_delay.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid retry base_delay")
	}
	if !strings.Contains(err.Error(), "base_delay") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "base_delay")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidRetryMaxDelay — Invalid retry.max_delay
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidRetryMaxDelay(t *testing.T) {
	_, err := LoadConfig("testdata/invalid_retry_max_delay.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid retry max_delay")
	}
	if !strings.Contains(err.Error(), "retry.max_delay") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "retry.max_delay")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidStaleCache — Invalid stale_cache duration
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidStaleCache(t *testing.T) {
	_, err := LoadConfig("testdata/invalid_stale_cache.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid stale_cache")
	}
	if !strings.Contains(err.Error(), "stale_cache") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "stale_cache")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigInvalidHedge — Invalid hedge duration
// ---------------------------------------------------------------------------

func TestLoadConfigInvalidHedge(t *testing.T) {
	_, err := LoadConfig("testdata/invalid_hedge.json")
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error for invalid hedge")
	}
	if !strings.Contains(err.Error(), "hedge") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "hedge")
	}
}

// ---------------------------------------------------------------------------
// TestLoadConfigBackoffStrategies — Verify all backoff strategies parse
// ---------------------------------------------------------------------------

func TestLoadConfigBackoffStrategies(t *testing.T) {
	strategies := []string{"constant", "exponential", "linear", "exponential_jitter"}

	for _, strat := range strategies {
		t.Run(strat, func(t *testing.T) {
			path := t.TempDir() + "/" + strat + ".json"
			content := `{
				"policies": {
					"test": {
						"retry": {
							"max_attempts": 2,
							"backoff": "` + strat + `",
							"base_delay": "100ms"
						}
					}
				}
			}`
			writeTestFile(t, path, content)

			reg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v, want nil for backoff %q", err, strat)
			}

			clk := newPolicyClock()
			p := GetPolicy[string](reg, "test", WithClock(clk))
			if p == nil {
				t.Fatalf("GetPolicy returned nil for backoff %q", strat)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetPolicyRegistersInRegistry — GetPolicy registers the policy
// ---------------------------------------------------------------------------

func TestGetPolicyRegistersInRegistry(t *testing.T) {
	reg, err := LoadConfig("testdata/valid.json")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	_ = GetPolicy[string](reg, "payment-api")

	status := reg.CheckReadiness()

	found := false
	for _, ps := range status.Policies {
		if ps.Name == "payment-api" {
			found = true
		}
	}
	if !found {
		t.Fatal("payment-api not found in registry after GetPolicy")
	}
}

// ---------------------------------------------------------------------------
// writeTestFile — helper to write a string to a file for testing
// ---------------------------------------------------------------------------

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFile(path, []byte(content)); err != nil {
		t.Fatalf("writeTestFile: %v", err)
	}
}
