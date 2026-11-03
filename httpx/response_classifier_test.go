package httpx_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
	"github.com/byte4ever/r8e/internal/clocktest"
)

var (
	errTokenExpired = errors.New("token expired")
	errMoved        = errors.New("resource moved")
)

// roundTripFunc is a transport built from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// trackedBody is a response body that records whether it was closed.
type trackedBody struct {
	io.Reader

	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)

	return nil
}

func respond(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: body}
}

func getRequest(t *testing.T) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test/", http.NoBody)
	require.NoError(t, err)

	return req
}

// The classifier tells two 403s apart from their body: a token to renew is
// retried, a moved resource stops the retry.
func TestResponseClassifierReadsTheBody(t *testing.T) {
	t.Parallel()

	classify := func(resp *http.Response, body []byte) error {
		switch {
		case resp.StatusCode/100 == 2:
			return nil
		case bytes.Contains(body, []byte("token")):
			return errTokenExpired
		default:
			return r8e.Permanent(errMoved)
		}
	}

	tests := map[string]struct {
		body      string
		wantErr   error
		wantCalls int64
		permanent bool
	}{
		"token to renew": {body: `{"error":"token"}`, wantErr: errTokenExpired, wantCalls: 3},
		"moved resource": {body: `{"error":"moved"}`, wantErr: errMoved, wantCalls: 1, permanent: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int64

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)

				return respond(http.StatusForbidden, io.NopCloser(strings.NewReader(tt.body))), nil
			})}

			cl := httpx.NewResponseClient("", hc, classify,
				r8e.WithClock(clocktest.New()),
				r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond)))

			_, err := cl.Do(t.Context(), getRequest(t))

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.permanent, r8e.IsPermanent(err))
			assert.Equal(t, tt.wantCalls, calls.Load())
		})
	}
}

// A non-2xx body is handed to the classifier up to MaxClassifiedBody, and the
// caller still reads the whole body from the start.
func TestResponseClassifierPutsTheBodyBack(t *testing.T) {
	t.Parallel()

	tests := map[string]int{
		"small body":                  100,
		"body over MaxClassifiedBody": httpx.MaxClassifiedBody + 1000,
	}

	for name, size := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			full := bytes.Repeat([]byte("x"), size)

			var seen int

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return respond(http.StatusNotFound, io.NopCloser(bytes.NewReader(full))), nil
			})}

			cl := httpx.NewResponseClient("", hc, func(_ *http.Response, body []byte) error {
				seen = len(body)

				return nil
			})

			resp, err := cl.Do(t.Context(), getRequest(t))
			require.NoError(t, err)

			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			assert.Equal(t, min(size, httpx.MaxClassifiedBody), seen)
			assert.Equal(t, full, got, "the caller reads the whole body")
		})
	}
}

// A 2xx body is not read: the classifier gets nil and the caller reads the
// body as the server sent it.
func TestResponseClassifierLeavesA2xxBodyUnread(t *testing.T) {
	t.Parallel()

	body := &trackedBody{Reader: strings.NewReader("streamed")}

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, body), nil
	})}

	var sawNil bool

	cl := httpx.NewResponseClient("", hc, func(_ *http.Response, b []byte) error {
		sawNil = b == nil

		return nil
	})

	resp, err := cl.Do(t.Context(), getRequest(t))
	require.NoError(t, err)

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.True(t, sawNil)
	assert.Equal(t, "streamed", string(got))
	assert.False(t, body.closed.Load())
}

// A retriable outcome drains and closes the body so the connection can be
// reused; a permanent one leaves it open for the caller.
func TestResponseClassifierClosesARetriableBody(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err        error
		wantClosed bool
	}{
		"retriable": {err: errTokenExpired, wantClosed: true},
		"permanent": {err: r8e.Permanent(errMoved), wantClosed: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := &trackedBody{Reader: strings.NewReader("error body")}

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return respond(http.StatusForbidden, body), nil
			})}

			cl := httpx.NewResponseClient("", hc, func(*http.Response, []byte) error { return tt.err })

			_, err := cl.Do(t.Context(), getRequest(t))
			require.ErrorIs(t, err, tt.err)

			assert.Equal(t, tt.wantClosed, body.closed.Load())
		})
	}
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// A failure reading a non-2xx body is transient and the classifier is not
// called: the retry tries again.
func TestResponseClassifierBodyReadErrorIsTransient(t *testing.T) {
	t.Parallel()

	var (
		attempts   atomic.Int64
		classified atomic.Int64
	)

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return respond(http.StatusBadGateway, io.NopCloser(errReader{})), nil
		}

		return respond(http.StatusOK, io.NopCloser(strings.NewReader("ok"))), nil
	})}

	cl := httpx.NewResponseClient("", hc, func(resp *http.Response, _ []byte) error {
		classified.Add(1)

		if resp.StatusCode/100 != 2 {
			return r8e.Permanent(errMoved)
		}

		return nil
	}, r8e.WithClock(clocktest.New()), r8e.WithRetry(2, r8e.ConstantBackoff(time.Millisecond)))

	resp, err := cl.Do(t.Context(), getRequest(t))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, int64(2), attempts.Load())
	assert.Equal(t, int64(1), classified.Load(), "only the readable response is classified")
}

// The read error itself carries the cause and is not permanent.
func TestResponseClassifierBodyReadErrorCause(t *testing.T) {
	t.Parallel()

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusBadGateway, io.NopCloser(errReader{})), nil
	})}

	cl := httpx.NewResponseClient("", hc, func(*http.Response, []byte) error {
		t.Error("the classifier must not be called")

		return nil
	})

	_, err := cl.Do(context.Background(), getRequest(t))

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.False(t, r8e.IsPermanent(err))
}
