package httpx_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestDoRetryReplaysRequestBody verifies that a request body is resent in full
// on every retry attempt rather than being consumed on the first try.
func TestDoRetryReplaysRequestBody(t *testing.T) {
	t.Parallel()

	const payload = "payload-must-replay"

	var (
		mu     sync.Mutex
		bodies []string
	)

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)

				mu.Lock()
				bodies = append(bodies, string(b))
				n := len(bodies)
				mu.Unlock()

				if n <= 2 {
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
		"do-retry-body",
		srv.Client(),
		testClassifier,
		r8e.WithRetry(
			5,
			r8e.ConstantBackoff(time.Millisecond),
		),
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		srv.URL,
		strings.NewReader(payload),
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 3)

	for i, b := range bodies {
		assert.Equalf(
			t, payload, b,
			"attempt %d received a wrong/empty body", i+1,
		)
	}
}

// TestDoGetBodyErrorPropagates verifies a GetBody failure surfaces as an error
// rather than silently sending an empty body on the retried request.
func TestDoGetBodyErrorPropagates(t *testing.T) {
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
		"do-getbody-error",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		srv.URL,
		strings.NewReader("x"),
	)
	require.NoError(t, err)

	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("getbody boom")
	}

	_, err = cl.Do(context.Background(), req)
	require.ErrorContains(t, err, "getbody boom")
}

// TestDoUnknownClassPassesThrough verifies an out-of-range ErrorClass from a
// custom classifier passes the response through unchanged (the switch default).
func TestDoUnknownClassPassesThrough(t *testing.T) {
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
		"do-unknown-class",
		srv.Client(),
		func(int) httpx.ErrorClass { return httpx.ErrorClass(99) },
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

func TestDoTransientRetriesExhausted(t *testing.T) {
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
		"test-exhausted",
		srv.Client(),
		testClassifier,
		r8e.WithRetry(
			2,
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

	resp, doErr := cl.Do(context.Background(), req)
	require.Error(t, doErr)

	// Retries exhausted wraps the underlying error.
	assert.ErrorIs(t, doErr, r8e.ErrRetriesExhausted)

	// StatusError should be extractable.
	var statusErr *httpx.StatusError
	require.ErrorAs(t, doErr, &statusErr)
	assert.Equal(
		t,
		http.StatusServiceUnavailable,
		statusErr.StatusCode,
	)

	// Response from Do itself may be nil (DoRetry
	// returns zero value on exhaustion).
	_ = resp
}

func TestStatusErrorMessage(t *testing.T) {
	t.Parallel()

	se := &httpx.StatusError{StatusCode: 503}
	assert.Equal(t, "http status 503", se.Error())
}

// statusErrorWithRetryAfter builds a StatusError whose response carries the
// given Retry-After header value (omitted when empty).
func statusErrorWithRetryAfter(value string) *httpx.StatusError {
	header := http.Header{}
	if value != "" {
		header.Set("Retry-After", value)
	}

	return &httpx.StatusError{
		Response:   &http.Response{Header: header},
		StatusCode: http.StatusTooManyRequests,
	}
}

func TestStatusErrorRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value   string
		want    time.Duration
		present bool
	}{
		"seconds":          {"120", 120 * time.Second, true},
		"zero":             {"0", 0, false}, // non-positive -> no useful hint
		"negative seconds": {"-5", 0, false},
		"unparseable":      {"soon", 0, false},
		"absent":           {"", 0, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := statusErrorWithRetryAfter(tc.value).RetryAfter()
			assert.Equal(t, tc.present, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStatusErrorRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	// A future HTTP-date yields a positive, roughly-correct delay.
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	got, ok := statusErrorWithRetryAfter(future).RetryAfter()
	require.True(t, ok)
	assert.Greater(t, got, 55*time.Minute)
	assert.LessOrEqual(t, got, time.Hour)

	// A past HTTP-date carries no useful wait, so it is reported as absent.
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	got, ok = statusErrorWithRetryAfter(past).RetryAfter()
	assert.False(t, ok)
	assert.Equal(t, time.Duration(0), got)
}

func TestStatusErrorRetryAfterNilResponse(t *testing.T) {
	t.Parallel()

	_, ok := (&httpx.StatusError{StatusCode: 429}).RetryAfter()
	assert.False(t, ok)
}

// FuzzStatusErrorRetryAfter throws arbitrary header strings at the Retry-After
// parser, asserting it never panics and upholds its contract: a present hint is
// always strictly positive, and an absent hint is zero.
func FuzzStatusErrorRetryAfter(f *testing.F) {
	for _, value := range []string{
		"", "120", "0", "-5", "soon", "Wed, 21 Oct 2026 07:28:00 GMT",
		"99999999999999999999", "9999999999", "Mon, 02 Jan 2006 15:04:05 GMT",
		"  ", "+10", "1.5", "0x10",
	} {
		f.Add(value)
	}

	f.Fuzz(func(t *testing.T, header string) {
		got, ok := statusErrorWithRetryAfter(header).RetryAfter() // must not panic

		if ok && got <= 0 {
			t.Errorf("RetryAfter(%q) = (%v, true), want a strictly positive hint", header, got)
		}

		if !ok && got != 0 {
			t.Errorf("RetryAfter(%q) = (%v, false), want zero when absent", header, got)
		}
	})
}
