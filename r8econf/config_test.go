package r8econf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/internal/clocktest"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func TestLoadValid(t *testing.T) {
	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.Len(t, store.configs, 2)

	payment, err := GetPolicy[string](store, "payment-api")
	require.NoError(t, err)
	assert.Equal(t, "payment-api", payment.Name())

	notify, err := GetPolicy[string](store, "notification-api")
	require.NoError(t, err)
	assert.Equal(t, "notification-api", notify.Name())
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("../testdata/nonexistent.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "r8e: read config")
}

func TestLoadInvalidJSON(t *testing.T) {
	path := writeTempFile(t, `{not valid json}`)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "r8e: parse config")
}

func TestLoadErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantSub string
	}{
		{"invalid timeout duration", "../testdata/invalid.json", "timeout"},
		{"unknown backoff", "../testdata/unknown_backoff.json", "unknown backoff strategy"},
		{"invalid cb duration", "../testdata/invalid_cb_duration.json", "circuit_breaker.recovery_timeout"},
		{"invalid retry base_delay", "../testdata/invalid_retry_base_delay.json", "base_delay"},
		{"invalid retry max_delay", "../testdata/invalid_retry_max_delay.json", "retry.max_delay"},
		{"invalid hedge", "../testdata/invalid_hedge.json", "hedge"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.file)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestGetPolicyFromConfigRuns(t *testing.T) {
	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)

	policy, err := GetPolicy[string](
		store,
		"notification-api",
		r8e.WithClock(clocktest.New()),
	)
	require.NoError(t, err)

	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestGetPolicyWithCodeOverride(t *testing.T) {
	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)

	policy, err := GetPolicy[string](
		store,
		"payment-api",
		r8e.WithClock(clocktest.New()),
	)
	require.NoError(t, err)

	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "payment", nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "payment", result)
}

func TestGetPolicyNotInConfig(t *testing.T) {
	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)

	policy, err := GetPolicy[string](store, "unknown-policy")
	require.NoError(t, err)
	assert.Equal(t, "unknown-policy", policy.Name())

	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "bare", nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "bare", result)
}

func TestLoadAllPatternsRuns(t *testing.T) {
	store, err := Load("../testdata/all_patterns.json")
	require.NoError(t, err)

	policy, err := GetPolicy[string](
		store,
		"full-policy",
		r8e.WithClock(clocktest.New()),
	)
	require.NoError(t, err)

	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "full", nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "full", result)
}

func TestGetPolicyRegistersInRegistry(t *testing.T) {
	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)

	_, err = GetPolicy[string](store, "payment-api")
	require.NoError(t, err)

	status := store.Registry().CheckReadiness()

	var found bool
	for _, ps := range status.Policies {
		if ps.Name == "payment-api" {
			found = true
		}
	}
	assert.True(t, found, "payment-api should be registered after GetPolicy")
}

func TestLoadBackoffStrategies(t *testing.T) {
	strategies := []string{"constant", "exponential", "linear", "exponential_jitter"}

	for _, strat := range strategies {
		t.Run(strat, func(t *testing.T) {
			path := writeTempFile(t, `{
				"policies": {
					"test": {
						"retry": {
							"max_attempts": 2,
							"backoff": "`+strat+`",
							"base_delay": "100ms"
						}
					}
				}
			}`)

			store, err := Load(path)
			require.NoError(t, err)

			policy, err := GetPolicy[string](
				store,
				"test",
				r8e.WithClock(clocktest.New()),
			)
			require.NoError(t, err)
			assert.Equal(t, "test", policy.Name())
		})
	}
}

// TestGetPolicyInvalidStoredConfig exercises the GetPolicy error path: a Store
// holding an invalid config (only reachable by bypassing Load's eager
// validation) must surface the build error rather than swallow it.
func TestGetPolicyInvalidStoredConfig(t *testing.T) {
	badBackoff := "nope"
	store := &Store{
		configs: map[string]r8e.PolicyConfig{
			"broken": {
				Retry: &r8e.RetryConfig{Backoff: &badBackoff},
			},
		},
		registry: r8e.NewRegistry(),
	}

	_, err := GetPolicy[string](store, "broken")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
}

// TestGetPolicyConfigWithCodeOverrides mirrors the former integration test:
// config-loaded retry plus code-level hooks and a health dependency.
func TestGetPolicyConfigWithCodeOverrides(t *testing.T) {
	clk := clocktest.New()

	store, err := Load("../testdata/valid.json")
	require.NoError(t, err)

	child := r8e.NewPolicy[int]("child-service",
		r8e.WithClock(clk),
		r8e.WithRegistry(r8e.NewRegistry()),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Hour),
		),
	)

	var hookCalled atomic.Bool
	hooks := r8e.Hooks{
		OnRetry: func(_ int, _ error) { hookCalled.Store(true) },
	}

	policy, err := GetPolicy[string](store, "notification-api",
		r8e.WithClock(clk),
		r8e.WithHooks(&hooks),
		r8e.DependsOn(child),
	)
	require.NoError(t, err)

	attempt := 0
	result, err := policy.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 2 {
				return "", errors.New("transient")
			}

			return "config-override-success", nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "config-override-success", result)
	assert.True(t, hookCalled.Load(), "OnRetry override should fire")

	status := policy.HealthStatus()
	require.NotEmpty(t, status.Dependencies)
	assert.Equal(t, "child-service", status.Dependencies[0].Name)
}
