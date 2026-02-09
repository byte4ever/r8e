package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// successClassifier classifies all codes as Success.
func successClassifier(_ int) httpx.ErrorClass {
	return httpx.Success
}

// testClassifier classifies HTTP status codes for testing.
func testClassifier(code int) httpx.ErrorClass {
	switch {
	case code >= 200 && code < 300:
		return httpx.Success
	case code == 429,
		code == 502,
		code == 503,
		code == 504:
		return httpx.Transient
	default:
		return httpx.Permanent
	}
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

func TestDoSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
		),
	)
	defer srv.Close()

	cl := httpx.NewClient(
		"do-success",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDoPermanentError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			},
		),
	)
	defer srv.Close()

	cl := httpx.NewClient(
		"do-permanent",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.True(t, r8e.IsPermanent(err))

	var se *httpx.StatusError
	require.True(t, errors.As(err, &se))
	assert.Equal(
		t,
		http.StatusBadRequest,
		se.StatusCode,
	)
}

func TestDoTransientNoRetry(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		),
	)
	defer srv.Close()

	cl := httpx.NewClient(
		"do-transient-no-retry",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.True(t, r8e.IsTransient(err))

	var se *httpx.StatusError
	require.True(t, errors.As(err, &se))
	assert.Equal(
		t,
		http.StatusServiceUnavailable,
		se.StatusCode,
	)
}

func TestDoTransientWithRetryRecovers(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls <= 2 {
					w.WriteHeader(
						http.StatusServiceUnavailable,
					)
					return
				}
				w.WriteHeader(http.StatusOK)
			},
		),
	)
	defer srv.Close()

	cl := httpx.NewClient(
		"do-transient-retry",
		srv.Client(),
		testClassifier,
		r8e.WithRetry(
			5,
			r8e.ConstantBackoff(time.Millisecond),
		),
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDoTransportError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
		),
	)

	srvURL := srv.URL
	srv.Close()

	cl := httpx.NewClient(
		"do-transport-error",
		&http.Client{},
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srvURL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.Error(t, err)
	assert.Nil(t, resp)

	var se *httpx.StatusError
	assert.False(t, errors.As(err, &se))
}

func TestStatusErrorMessage(t *testing.T) {
	t.Parallel()

	se := &httpx.StatusError{StatusCode: 503}
	assert.Equal(t, "http status 503", se.Error())
}
