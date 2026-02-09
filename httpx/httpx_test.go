package httpx_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e/httpx"
)

// successClassifier classifies all codes as Success.
func successClassifier(_ int) httpx.ErrorClass {
	return httpx.Success
}

func TestNewClientReturnsNonNil(t *testing.T) {
	t.Parallel()

	cl := httpx.NewClient(
		"test",
		http.DefaultClient,
		successClassifier,
	)

	require.NotNil(t, cl)
}

func TestNewClientWithEmptyName(t *testing.T) {
	t.Parallel()

	cl := httpx.NewClient(
		"",
		http.DefaultClient,
		successClassifier,
	)

	require.NotNil(t, cl)
}
