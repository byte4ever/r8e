package httpx_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// trackedBody is a response body that counts its reads and records whether it
// was closed.
type trackedBody struct {
	r      io.Reader
	reads  atomic.Int64
	closed atomic.Bool
}

func newTrackedBody(content string) *trackedBody {
	return &trackedBody{r: strings.NewReader(content)}
}

func (b *trackedBody) Read(p []byte) (int, error) {
	b.reads.Add(1)

	return b.r.Read(p)
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
		case resp.StatusCode < http.StatusMultipleChoices:
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

// For a status of 300 or above, the body is handed to the classifier up to
// MaxClassifiedBody, and the caller still reads the whole body from the start.
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

	body := newTrackedBody("streamed")

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
	assert.Zero(t, body.reads.Load(), "Do does not read a 2xx body")

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.True(t, sawNil, "the classifier gets no body for a 2xx")
	assert.Equal(t, "streamed", string(got))
	assert.False(t, body.closed.Load())
}

// permanentStatusError marks resp's status as a permanent failure that
// carries resp, as the README's classifier does.
func permanentStatusError(resp *http.Response) error {
	return r8e.Permanent(&httpx.StatusError{Response: resp, StatusCode: resp.StatusCode})
}

// constErr returns a classifier outcome that ignores the response.
func constErr(err error) func(*http.Response) error {
	return func(*http.Response) error { return err }
}

// On an error, the body stays open only on a permanent error that carries the
// response in a StatusError. A retriable outcome, for a 2xx as for an error
// status, a permanent error without a StatusError, and one whose StatusError
// does not carry this response close it.
func TestResponseClassifierClosesAnUnreachableBody(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		classify   func(*http.Response) error
		status     int
		wantClosed bool
	}{
		"retriable error status":     {status: http.StatusForbidden, classify: constErr(errTokenExpired), wantClosed: true},
		"retriable 2xx":              {status: http.StatusOK, classify: constErr(errTokenExpired), wantClosed: true},
		"permanent, no StatusError":  {status: http.StatusForbidden, classify: constErr(r8e.Permanent(errMoved)), wantClosed: true},
		"permanent with StatusError": {status: http.StatusForbidden, classify: permanentStatusError},
		"transient with StatusError": {
			status: http.StatusServiceUnavailable,
			classify: func(resp *http.Response) error {
				return &httpx.StatusError{Response: resp, StatusCode: resp.StatusCode}
			},
			wantClosed: true,
		},
		"permanent StatusError without the response": {
			status: http.StatusForbidden,
			classify: func(resp *http.Response) error {
				return r8e.Permanent(&httpx.StatusError{StatusCode: resp.StatusCode})
			},
			wantClosed: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := newTrackedBody("error body")

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return respond(tt.status, body), nil
			})}

			cl := httpx.NewResponseClient("", hc, func(resp *http.Response, _ []byte) error { return tt.classify(resp) })

			_, err := cl.Do(t.Context(), getRequest(t))
			require.Error(t, err)
			assert.Equal(t, tt.wantClosed, body.closed.Load(), "body closed")

			if tt.wantClosed {
				return
			}

			var statusErr *httpx.StatusError

			require.ErrorAs(t, err, &statusErr)

			got, err := io.ReadAll(statusErr.Response.Body)
			require.NoError(t, err)
			assert.Equal(t, "error body", string(got), "the body is whole")
		})
	}
}

// A WithFallbackFunc receives the permanent StatusError, so it can close the
// body of the response it drops.
func TestResponseClassifierFallbackFuncClosesTheBody(t *testing.T) {
	t.Parallel()

	body := newTrackedBody("error body")

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusForbidden, body), nil
	})}

	var seen *httpx.StatusError

	cl := httpx.NewResponseClient("", hc,
		func(resp *http.Response, _ []byte) error { return permanentStatusError(resp) },
		r8e.WithFallbackFunc(func(err error) (*http.Response, error) {
			if errors.As(err, &seen) {
				_ = seen.Response.Body.Close()
			}

			return nil, err
		}))

	_, err := cl.Do(t.Context(), getRequest(t))
	require.Error(t, err)
	require.NotNil(t, seen, "the fallback gets the StatusError")
	assert.True(t, body.closed.Load(), "the fallback closed the body")
}

// serveBody answers every request with status and payload.
func serveBody(t *testing.T, status int, payload []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// keepBody returns nil below 300 and a permanent StatusError carrying the
// response above, so the adapter leaves the body open in both cases.
func keepBody(resp *http.Response, _ []byte) error {
	if resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	return permanentStatusError(resp)
}

// openBody returns the body Do left open: resp's, or the StatusError's.
func openBody(t *testing.T, resp *http.Response, err error) io.ReadCloser {
	t.Helper()

	if err == nil {
		require.NotNil(t, resp)

		return resp.Body
	}

	var statusErr *httpx.StatusError

	require.ErrorAs(t, err, &statusErr)

	return statusErr.Response.Body
}

// With no option that ends the attempt's context, the body Do leaves open
// reads to its end after Do, for a success and for a permanent StatusError,
// on a real transport.
func TestResponseClassifierBodyOutlivesDo(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("x"), 4*httpx.MaxClassifiedBody)

	tests := map[string]int{
		"2xx":           http.StatusOK,
		"permanent 403": http.StatusForbidden,
	}

	for name, status := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := serveBody(t, status, payload)
			cl := httpx.NewResponseClient("", srv.Client(), keepBody,
				r8e.WithRetry(2, r8e.ConstantBackoff(time.Millisecond)))

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
			require.NoError(t, err)

			resp, err := cl.Do(t.Context(), req)
			body := openBody(t, resp, err)

			got, readErr := io.ReadAll(body)
			require.NoError(t, readErr)
			require.NoError(t, body.Close())
			assert.Len(t, got, len(payload), "the whole body")
		})
	}
}

// Under WithTimeout the attempt's context ends when Do returns, so a body
// larger than the transport's buffers breaks off with context.Canceled.
func TestResponseClassifierBodyCutByWithTimeout(t *testing.T) {
	t.Parallel()

	srv := serveBody(t, http.StatusOK, bytes.Repeat([]byte("x"), 8<<20))
	cl := httpx.NewResponseClient("", srv.Client(), keepBody, r8e.WithTimeout(time.Minute))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := cl.Do(t.Context(), req)
	require.NoError(t, err)

	defer resp.Body.Close()

	_, readErr := io.ReadAll(resp.Body)
	require.ErrorIs(t, readErr, context.Canceled)
}

// Under a retrying policy, Do returns a nil response on a permanent error, so
// only a StatusError keeps the body reachable; without one the adapter closes
// the body instead of leaking it.
func TestResponseClassifierPermanentBodyUnderRetry(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		classify  func(*http.Response) error
		wantOpen  bool
		wantCause error
	}{
		"no StatusError":   {classify: constErr(r8e.Permanent(errMoved)), wantCause: errMoved},
		"with StatusError": {classify: permanentStatusError, wantOpen: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := newTrackedBody(strings.Repeat("x", httpx.MaxClassifiedBody+1))

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return respond(http.StatusForbidden, body), nil
			})}

			cl := httpx.NewResponseClient("", hc,
				func(resp *http.Response, _ []byte) error { return tt.classify(resp) },
				r8e.WithClock(clocktest.New()),
				r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond)))

			resp, err := cl.Do(t.Context(), getRequest(t))
			require.Error(t, err)
			assert.Nil(t, resp, "a retrying policy returns no response with an error")

			if !tt.wantOpen {
				require.ErrorIs(t, err, tt.wantCause)
				assert.True(t, body.closed.Load(), "the unreachable body is closed")

				return
			}

			assert.False(t, body.closed.Load(), "the reachable body stays open")

			var statusErr *httpx.StatusError

			require.ErrorAs(t, err, &statusErr)

			got, err := io.ReadAll(statusErr.Response.Body)
			require.NoError(t, err)
			require.NoError(t, statusErr.Response.Body.Close())
			assert.Len(t, got, httpx.MaxClassifiedBody+1, "the body is whole")
		})
	}
}

// The policy may wrap the classifier's error: once retries run out it adds
// ErrRetriesExhausted, so the caller matches the error with errors.Is.
func TestResponseClassifierErrorUnderRetry(t *testing.T) {
	t.Parallel()

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusForbidden, newTrackedBody("token")), nil
	})}

	cl := httpx.NewResponseClient("", hc,
		func(*http.Response, []byte) error { return errTokenExpired },
		r8e.WithClock(clocktest.New()),
		r8e.WithRetry(2, r8e.ConstantBackoff(time.Millisecond)))

	_, err := cl.Do(t.Context(), getRequest(t))
	require.ErrorIs(t, err, errTokenExpired)
	require.ErrorIs(t, err, r8e.ErrRetriesExhausted)
	assert.NotEqual(t, errTokenExpired, err, "the error is wrapped, not returned as is")
}

// The classifier sees the body only through its body argument: reading or
// closing resp.Body during the call does not touch the body the caller gets.
func TestResponseClassifierHidesTheBody(t *testing.T) {
	t.Parallel()

	tests := map[string]int{
		"2xx":          http.StatusOK,
		"error status": http.StatusNotFound,
	}

	for name, status := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := newTrackedBody("payload")

			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return respond(status, body), nil
			})}

			var stolen int

			cl := httpx.NewResponseClient("", hc, func(resp *http.Response, _ []byte) error {
				data, _ := io.ReadAll(resp.Body)
				stolen = len(data)
				_ = resp.Body.Close()

				return nil
			})

			resp, err := cl.Do(t.Context(), getRequest(t))
			require.NoError(t, err)

			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Zero(t, stolen, "resp.Body is empty during the call")
			assert.False(t, body.closed.Load(), "the classifier's Close does not reach the body")
			assert.Equal(t, "payload", string(got), "the caller reads the whole body")
		})
	}
}

// A classifier that returns a StatusError gets the Retry-After hint back, and
// the caller finds the StatusError with errors.As.
func TestResponseClassifierStatusErrorKeepsRetryAfter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int64

		hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)

			resp := respond(http.StatusServiceUnavailable, io.NopCloser(strings.NewReader("busy")))
			resp.Header.Set("Retry-After", "2")

			return resp, nil
		})}

		cl := httpx.NewResponseClient("", hc, func(resp *http.Response, _ []byte) error {
			return &httpx.StatusError{Response: resp, StatusCode: resp.StatusCode}
		}, r8e.WithRetry(2, r8e.ConstantBackoff(time.Millisecond)))

		start := time.Now()

		_, err := cl.Do(t.Context(), getRequest(t))

		var statusErr *httpx.StatusError

		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusServiceUnavailable, statusErr.StatusCode)
		assert.Equal(t, int64(2), attempts.Load())
		waited := time.Since(start)
		assert.GreaterOrEqual(t, waited, 2*time.Second, "the hint replaces the 1ms backoff")
		assert.Less(t, waited, 3*time.Second)
	})
}

// newEchoUpgradeServer answers every request with a 101 upgrade, then echoes
// one line back prefixed with "echo:". It gives up on a connection after 3s, so
// a client that waits on the upgraded connection fails its test instead of
// hanging it.
func newEchoUpgradeServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}

		defer conn.Close()

		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()

		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}

		_, _ = rw.WriteString("echo:" + line)
		_ = rw.Flush()
	}))
	t.Cleanup(srv.Close)

	return srv
}

// upgradeRequest builds a request to url that asks for the echo protocol.
func upgradeRequest(t *testing.T, ctx context.Context, url string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")

	return req
}

// doWithin runs cl.Do and fails the test when Do has not returned within 2s.
func doWithin(t *testing.T, cl *httpx.Client, req *http.Request) (*http.Response, error) {
	t.Helper()

	type result struct {
		resp *http.Response
		err  error
	}

	done := make(chan result, 1)

	go func() {
		resp, err := cl.Do(req.Context(), req)
		done <- result{resp: resp, err: err}
	}()

	select {
	case res := <-done:
		return res.resp, res.err
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Do waits on the upgraded connection")

		return nil, nil
	}
}

// A 101 upgrade is not read: Do returns at once and the caller writes on the
// upgraded connection.
func TestResponseClassifierLeavesAnUpgradeAlone(t *testing.T) {
	t.Parallel()

	srv := newEchoUpgradeServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var sawNil atomic.Bool

	cl := httpx.NewResponseClient("", srv.Client(), func(_ *http.Response, body []byte) error {
		sawNil.Store(body == nil)

		return nil
	})

	resp, err := doWithin(t, cl, upgradeRequest(t, ctx, srv.URL))
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	conn, ok := resp.Body.(io.ReadWriteCloser)
	require.True(t, ok, "the upgraded body can still be written to")

	defer conn.Close()

	_, err = io.WriteString(conn, "ping\n")
	require.NoError(t, err)

	got, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)

	assert.Equal(t, "echo:ping\n", got)
	assert.True(t, sawNil.Load(), "the classifier gets no body for a 1xx")
}

// A 101 whose outcome may be retried is closed, not drained: draining an
// upgraded connection would wait on the peer. Both constructors return at
// once.
func TestRetriableUpgradeIsNotDrained(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		client func(hc *http.Client) *httpx.Client
		check  func(t *testing.T, err error)
	}{
		"ResponseClassifier": {
			client: func(hc *http.Client) *httpx.Client {
				return httpx.NewResponseClient("", hc, func(*http.Response, []byte) error { return errTokenExpired })
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				require.ErrorIs(t, err, errTokenExpired)
			},
		},
		"Classifier": {
			client: func(hc *http.Client) *httpx.Client {
				return httpx.NewClient("", hc, func(int) httpx.ErrorClass { return httpx.Transient })
			},
			check: func(t *testing.T, err error) {
				t.Helper()

				var statusErr *httpx.StatusError

				require.ErrorAs(t, err, &statusErr)
				assert.Equal(t, http.StatusSwitchingProtocols, statusErr.StatusCode)
				assert.True(t, r8e.IsTransient(err))
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := newEchoUpgradeServer(t)

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			resp, err := doWithin(t, tt.client(srv.Client()), upgradeRequest(t, ctx, srv.URL))
			tt.check(t, err)
			require.NotNil(t, resp, "the 101 comes back with its error")

			conn, ok := resp.Body.(io.Writer)
			require.True(t, ok, "the upgraded body is writable")

			_, err = io.WriteString(conn, "ping\n")
			require.Error(t, err, "the upgraded connection is closed")
		})
	}
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// A failure reading the body of an error status is transient and the
// classifier is not called: the retry tries again.
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

		if resp.StatusCode >= http.StatusMultipleChoices {
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

	_, err := cl.Do(t.Context(), getRequest(t))

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.ErrorContains(t, err, "read response body")
	assert.True(t, r8e.IsTransient(err))
}
