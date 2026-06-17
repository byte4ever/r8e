package r8e

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// TestParseBackoffStrategyGuards covers the validation branches of
// parseBackoffStrategy, including the two nil-pointer guards that were
// previously untested.
func TestParseBackoffStrategyGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		backoff   *string
		baseDelay *string
		wantErr   string
	}{
		{"nil backoff", nil, strPtr("100ms"), "backoff is required"},
		{"nil base_delay", strPtr("constant"), nil, "base_delay is required"},
		{"invalid base_delay", strPtr("constant"), strPtr("nope"), "base_delay"},
		{"unknown strategy", strPtr("weird"), strPtr("100ms"), "unknown backoff strategy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseBackoffStrategy(tt.backoff, tt.baseDelay)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseBackoffStrategyValid(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"constant", "exponential", "linear", "exponential_jitter"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			strategy, err := parseBackoffStrategy(strPtr(name), strPtr("100ms"))
			require.NoError(t, err)
			require.NotNil(t, strategy)
		})
	}
}
