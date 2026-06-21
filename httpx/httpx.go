package httpx

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

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
		httpClient *http.Client
		policy     *r8e.Policy[*http.Response]
		classifier Classifier
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

// Compile-time check that StatusError supplies a retry-after
// hint to r8e's retry; a signature drift on either side fails
// the build here rather than silently disabling the feature.
//
//nolint:errcheck // interface satisfaction assertion, not a discarded error
var _ r8e.RetryAfterProvider = (*StatusError)(nil)

// Error returns a human-readable description of the status
// error.
func (e *StatusError) Error() string {
	return "http status " + strconv.Itoa(e.StatusCode)
}

// RetryAfter reports the delay requested by the response's
// Retry-After header, parsing either the delay-seconds form
// ("120") or the HTTP-date form ("Wed, 21 Oct 2026 07:28:00
// GMT"). The second result is true only for a strictly positive
// delay; an absent, unparseable, zero, negative, or already-past
// value yields (0, false) so retry falls back to its configured
// backoff. r8e's retry honors a positive hint over that backoff,
// so an HTTP 429/503 with Retry-After is respected automatically.
func (e *StatusError) RetryAfter() (time.Duration, bool) {
	if e.Response == nil {
		return 0, false
	}

	value := e.Response.Header.Get("Retry-After")
	if value == "" {
		return 0, false
	}

	// Delay-seconds form.
	if secs, err := strconv.Atoi(value); err == nil {
		return positiveDelay(time.Duration(secs) * time.Second)
	}

	// HTTP-date form: wait until the given instant.
	if when, err := http.ParseTime(value); err == nil {
		return positiveDelay(time.Until(when))
	}

	return 0, false
}

// positiveDelay reports d as a present hint only when it is
// strictly positive; a non-positive delay carries no useful wait
// and is reported as absent.
func positiveDelay(d time.Duration) (time.Duration, bool) {
	if d <= 0 {
		return 0, false
	}

	return d, true
}

// NewClient creates a Client that executes HTTP requests
// through the given r8e policy options. The classifier
// determines how HTTP status codes map to transient or
// permanent errors for retry decisions.
func NewClient(
	name string,
	hc *http.Client,
	cl Classifier,
	opts ...r8e.Option,
) *Client {
	return &Client{
		httpClient: hc,
		policy:     r8e.NewPolicy[*http.Response](name, opts...),
		classifier: cl,
	}
}

// Do executes the HTTP request through the resilience
// policy. Like http.Client.Do, it may return both a
// non-nil response and a non-nil error. When the
// Classifier returns Transient or Permanent, the response
// is wrapped in a StatusError accessible via errors.As.
//
// Each attempt rewinds the request body via req.GetBody, so a retried
// request with a body (POST/PUT) replays correctly. Requests built with
// http.NewRequest/NewRequestWithContext from a bytes/strings reader get
// GetBody automatically; a body without GetBody cannot be replayed and
// later attempts would send it empty.
func (c *Client) Do(
	ctx context.Context,
	req *http.Request,
) (*http.Response, error) {
	//nolint:wrapcheck // policy returns caller's error as-is
	return c.policy.Do(
		ctx,
		func(ctx context.Context) (*http.Response, error) {
			attempt := req.Clone(ctx)
			if req.GetBody != nil {
				body, gbErr := req.GetBody()
				if gbErr != nil {
					return nil, gbErr
				}

				attempt.Body = body
			}

			resp, err := c.httpClient.Do(attempt)
			if err != nil {
				return nil, err
			}

			switch c.classifier(resp.StatusCode) {
			case Success:
				return resp, nil
			case Transient:
				// Drain and close body so the underlying
				// TCP connection can be reused on retry.
				//nolint:errcheck // best-effort drain
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

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
				// An out-of-range ErrorClass from a custom
				// classifier is passed through unchanged rather
				// than silently retried.
				return resp, nil
			}
		},
	)
}
