package r8e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSheddabilityFromCtxDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, SheddabilityDefault, SheddabilityFromCtx(context.Background()),
		"unstamped context must return SheddabilityDefault")
}

func TestWithSheddabilityRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		level Sheddability
	}{
		{"Never", SheddabilityNever},
		{"Default", SheddabilityDefault},
		{"Always", SheddabilityAlways},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := WithSheddability(context.Background(), tc.level)
			assert.Equal(t, tc.level, SheddabilityFromCtx(ctx))
		})
	}
}

func TestWithSheddabilityChildContextInherits(t *testing.T) {
	t.Parallel()

	parent := WithSheddability(context.Background(), SheddabilityNever)
	child, cancel := context.WithCancel(parent)
	defer cancel()

	assert.Equal(t, SheddabilityNever, SheddabilityFromCtx(child),
		"child context must inherit parent's sheddability")
}

func TestWithSheddabilityOverrideIsScoped(t *testing.T) {
	t.Parallel()

	parent := WithSheddability(context.Background(), SheddabilityNever)
	// Derive a child that overrides to Sheddable.
	child := WithSheddability(parent, SheddabilityAlways)

	assert.Equal(t, SheddabilityNever, SheddabilityFromCtx(parent), "parent must be unchanged")
	assert.Equal(t, SheddabilityAlways, SheddabilityFromCtx(child), "child must carry override")
}
