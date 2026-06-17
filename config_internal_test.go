package r8e

import (
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

// TestParseBackoffStrategyGuards covers the validation branches of
// parseBackoffStrategy, including the two nil-pointer guards that were
// previously untested.
func TestParseBackoffStrategyGuards(t *testing.T) {
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
			_, err := parseBackoffStrategy(tt.backoff, tt.baseDelay)
			if err == nil {
				t.Fatalf("parseBackoffStrategy() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseBackoffStrategyValid(t *testing.T) {
	for _, name := range []string{"constant", "exponential", "linear", "exponential_jitter"} {
		t.Run(name, func(t *testing.T) {
			strategy, err := parseBackoffStrategy(strPtr(name), strPtr("100ms"))
			if err != nil {
				t.Fatalf("parseBackoffStrategy(%q) error = %v", name, err)
			}
			if strategy == nil {
				t.Fatalf("parseBackoffStrategy(%q) returned nil strategy", name)
			}
		})
	}
}
