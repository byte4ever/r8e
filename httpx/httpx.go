package httpx

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/byte4ever/r8e"
)

type (
	// ErrorClass tells the resilience layer how to treat
	// an HTTP status code.
	ErrorClass int

	// Classifier maps an HTTP status code to an ErrorClass.
	//
	// Pattern: Strategy — caller injects classification
	// logic without modifying the adapter.
	Classifier func(statusCode int) ErrorClass

	// StatusError is returned when the Classifier marks a
	// status code as Transient or Permanent. The original
	// response remains accessible for header inspection.
	// The body is drained and closed on transient errors
	// during retries; only the permanent error path
	// preserves an unread body.
	StatusError struct {
		Response   *http.Response
		StatusCode int
	}

	// Client wraps an http.Client with an r8e resilience
	// policy and HTTP status code classification.
	//
	// Pattern: Adapter — bridges net/http and r8e's
	// resilience policy by translating HTTP status codes
	// into r8e error classification.
	Client struct {
		hc *http.Client
		p  *r8e.Policy[*http.Response]
		cl Classifier
	}
)

const (
	// Success means the request succeeded (e.g. 2xx).
	Success ErrorClass = iota
	// Transient means the error is retriable (e.g. 429,
	// 503).
	Transient
	// Permanent means the error is non-retriable
	// (e.g. 400).
	Permanent
)

// Error returns a human-readable description of the status
// error.
func (e *StatusError) Error() string {
	return "http status " + strconv.Itoa(e.StatusCode)
}

// NewClient creates a Client that executes HTTP requests
// through the given r8e policy options. The classifier
// determines how HTTP status codes map to transient or
// permanent errors for retry decisions.
func NewClient(
	name string,
	hc *http.Client,
	cl Classifier,
	opts ...any,
) *Client {
	return &Client{
		hc: hc,
		p:  r8e.NewPolicy[*http.Response](name, opts...),
		cl: cl,
	}
}

// Do executes the HTTP request through the resilience
// policy. Like http.Client.Do, it may return both a
// non-nil response and a non-nil error. When the
// Classifier returns Transient or Permanent, the response
// is wrapped in a StatusError accessible via errors.As.
func (c *Client) Do(
	ctx context.Context,
	req *http.Request,
) (*http.Response, error) {
	//nolint:wrapcheck // policy returns caller's error as-is
	return c.p.Do(
		ctx,
		func(ctx context.Context) (*http.Response, error) {
			resp, err := c.hc.Do(
				req.WithContext(ctx),
			)
			if err != nil {
				return nil, err
			}

			switch c.cl(resp.StatusCode) {
			case Transient:
				// Drain and close body so the underlying
				// TCP connection can be reused on retry.
				io.Copy(io.Discard, resp.Body)  //nolint:errcheck // best-effort drain before retry
				resp.Body.Close()               //nolint:errcheck // best-effort close before retry

				return resp, r8e.Transient(
					&StatusError{
						Response:   resp,
						StatusCode: resp.StatusCode,
					},
				)
			case Permanent:
				return resp, r8e.Permanent(
					&StatusError{
						Response:   resp,
						StatusCode: resp.StatusCode,
					},
				)
			default:
				return resp, nil
			}
		},
	)
}
