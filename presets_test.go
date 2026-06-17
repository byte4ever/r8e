package r8e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestStandardHTTPClient — returns correct number of options (3)
// ---------------------------------------------------------------------------

func TestStandardHTTPClient(t *testing.T) {
	opts := StandardHTTPClient()

	require.Len(t, opts, 3)

	// Verify a policy can be created from the preset (no panic).
	p := NewPolicy[string]("std-http-test", opts...)
	require.NotNil(t, p)
}

// ---------------------------------------------------------------------------
// TestAggressiveHTTPClient — returns correct number of options (4)
// ---------------------------------------------------------------------------

func TestAggressiveHTTPClient(t *testing.T) {
	opts := AggressiveHTTPClient()

	require.Len(t, opts, 4)

	// Verify a policy can be created from the preset (no panic).
	p := NewPolicy[string]("aggressive-http-test", opts...)
	require.NotNil(t, p)
}

// ---------------------------------------------------------------------------
// TestStandardHTTPClientPolicy — create policy, call Do, verify it works
// ---------------------------------------------------------------------------

func TestStandardHTTPClientPolicy(t *testing.T) {
	clk := newPolicyClock()

	opts := append(StandardHTTPClient(), WithClock(clk))
	p := NewPolicy[string]("std-http-policy", opts...)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "user-data", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "user-data", result)
}

// ---------------------------------------------------------------------------
// TestAggressiveHTTPClientPolicy — create policy, call Do, verify it works
// ---------------------------------------------------------------------------

func TestAggressiveHTTPClientPolicy(t *testing.T) {
	clk := newPolicyClock()

	opts := append(AggressiveHTTPClient(), WithClock(clk))
	p := NewPolicy[string]("aggressive-http-policy", opts...)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "fast-data", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "fast-data", result)
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

	// The policy should still work. The override adds a second timeout
	// middleware,
	// both will be present in the chain.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "overridden", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "overridden", result)
}

// ---------------------------------------------------------------------------
// BenchmarkPresetCreation — benchmark creating a preset
// ---------------------------------------------------------------------------

func BenchmarkPresetCreation(b *testing.B) {
	for b.Loop() {
		_ = StandardHTTPClient()
	}
}
