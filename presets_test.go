package r8e

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestStandardHTTPClient — returns correct number of options (3)
// ---------------------------------------------------------------------------

func TestStandardHTTPClient(t *testing.T) {
	opts := StandardHTTPClient()

	if got := len(opts); got != 3 {
		t.Fatalf("StandardHTTPClient() returned %d options, want 3", got)
	}

	// Verify a policy can be created from the preset (no panic).
	p := NewPolicy[string]("std-http-test", opts...)
	if p == nil {
		t.Fatal("NewPolicy returned nil")
	}
}

// ---------------------------------------------------------------------------
// TestAggressiveHTTPClient — returns correct number of options (4)
// ---------------------------------------------------------------------------

func TestAggressiveHTTPClient(t *testing.T) {
	opts := AggressiveHTTPClient()

	if got := len(opts); got != 4 {
		t.Fatalf("AggressiveHTTPClient() returned %d options, want 4", got)
	}

	// Verify a policy can be created from the preset (no panic).
	p := NewPolicy[string]("aggressive-http-test", opts...)
	if p == nil {
		t.Fatal("NewPolicy returned nil")
	}
}

// ---------------------------------------------------------------------------
// TestCachedClient — returns StandardHTTPClient + stale cache (4 options)
// ---------------------------------------------------------------------------

func TestCachedClient(t *testing.T) {
	opts := CachedClient()

	if got := len(opts); got != 4 {
		t.Fatalf("CachedClient() returned %d options, want 4", got)
	}

	// Verify a policy can be created from the preset (no panic).
	p := NewPolicy[string]("cached-client-test", opts...)
	if p == nil {
		t.Fatal("NewPolicy returned nil")
	}
}

// ---------------------------------------------------------------------------
// TestStandardHTTPClientPolicy — create policy, call Do, verify it works
// ---------------------------------------------------------------------------

func TestStandardHTTPClientPolicy(t *testing.T) {
	clk := newPolicyClock()

	opts := append(StandardHTTPClient(), WithClock(clk))
	p := NewPolicy[string]("std-http-policy", opts...)

	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "user-data", nil
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "user-data" {
		t.Fatalf("Do() = %q, want %q", result, "user-data")
	}
}

// ---------------------------------------------------------------------------
// TestAggressiveHTTPClientPolicy — create policy, call Do, verify it works
// ---------------------------------------------------------------------------

func TestAggressiveHTTPClientPolicy(t *testing.T) {
	clk := newPolicyClock()

	opts := append(AggressiveHTTPClient(), WithClock(clk))
	p := NewPolicy[string]("aggressive-http-policy", opts...)

	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "fast-data", nil
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "fast-data" {
		t.Fatalf("Do() = %q, want %q", result, "fast-data")
	}
}

// ---------------------------------------------------------------------------
// TestCachedClientPolicy — create policy, call Do, verify stale cache works
// ---------------------------------------------------------------------------

func TestCachedClientPolicy(t *testing.T) {
	clk := newPolicyClock()

	opts := append(CachedClient(), WithClock(clk))
	p := NewPolicy[string]("cached-client-policy", opts...)

	// First call succeeds, populating the stale cache.
	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "cached-value", nil
	})
	if err != nil {
		t.Fatalf("first Do() error = %v, want nil", err)
	}
	if result != "cached-value" {
		t.Fatalf("first Do() = %q, want %q", result, "cached-value")
	}

	// Advance time within the 5-minute stale cache TTL.
	clk.advance(2 * time.Minute)

	// Second call fails; stale cache should serve the cached value.
	result, err = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("temporary failure")
	})
	if err != nil {
		t.Fatalf("second Do() error = %v, want nil (stale served)", err)
	}
	if result != "cached-value" {
		t.Fatalf("second Do() = %q, want %q", result, "cached-value")
	}
}

// ---------------------------------------------------------------------------
// TestPresetWithOverride — use preset + additional option, verify both apply
// ---------------------------------------------------------------------------

func TestPresetWithOverride(t *testing.T) {
	clk := newPolicyClock()

	// Start with standard preset and override the timeout to 10s.
	// Also inject a clock for deterministic testing.
	opts := append(StandardHTTPClient(),
		WithTimeout(10*time.Second),
		WithClock(clk),
	)
	p := NewPolicy[string]("override-test", opts...)

	// The policy should still work. The override adds a second timeout middleware,
	// both will be present in the chain.
	result, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "overridden", nil
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "overridden" {
		t.Fatalf("Do() = %q, want %q", result, "overridden")
	}

	// Verify the policy has patterns from both preset and override.
	// StandardHTTPClient has 3 options (timeout, retry, circuit breaker).
	// We added timeout + clock = 2 more. Clock is a policyOptionFunc, not a pattern.
	// So we should have 4 pattern entries: timeout (preset) + retry + circuit breaker + timeout (override).
	if got := len(p.entries); got != 4 {
		t.Fatalf("policy has %d pattern entries, want 4 (3 from preset + 1 override timeout)", got)
	}
}

// ---------------------------------------------------------------------------
// BenchmarkPresetCreation — benchmark creating a preset
// ---------------------------------------------------------------------------

func BenchmarkPresetCreation(b *testing.B) {
	for b.Loop() {
		_ = StandardHTTPClient()
	}
}
