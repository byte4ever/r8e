package httpx

import (
	"bytes"
	"context"
	"fmt"
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

	// ResponseClassifier classifies a response from its status, headers and
	// body, for APIs that answer the same status for opposite cases (a 403
	// for a token to renew or for a moved resource, a 404 for an unknown
	// object or for a route missing during a rollout). It returns nil for a
	// success, or the error the attempt fails with. That error is classified
	// as anywhere in r8e: [r8e.Permanent] stops the retry,
	// [r8e.RetryAfterError] carries a delay hint, and any other error is
	// transient.
	//
	// For a non-2xx response, body holds its first [MaxClassifiedBody]
	// bytes, and resp.Body still reads the whole body from the start. For a
	// 2xx response, body is nil and the body is not read, so a streamed
	// success stays streamed.
	//
	// Pattern: Strategy — caller injects classification logic without
	// modifying the adapter.
	ResponseClassifier func(resp *http.Response, body []byte) error

	// readCloser pairs a reader with the closer of the body it reads from.
	readCloser struct {
		io.Reader
		io.Closer
	}

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
		// respClassifier, when set, replaces classifier (see
		// NewResponseClient).
		respClassifier ResponseClassifier
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

	// MaxClassifiedBody is how many bytes of a non-2xx response body a
	// [ResponseClassifier] receives.
	MaxClassifiedBody = 64 << 10
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

// NewResponseClient creates a Client that classifies each response with rc,
// which sees the status, the headers and, for a non-2xx response, the start
// of the body (see [ResponseClassifier]). It is the variant of [NewClient]
// for APIs whose status code alone does not tell a retriable failure from a
// final one.
//
// When rc's error is not [r8e.Permanent], a retry may follow, so the body is
// drained and closed to let the connection be reused. A permanent error, or
// a nil one, leaves the body readable from the start. A failure reading the
// body is transient, and rc is not called.
func NewResponseClient(
	name string,
	hc *http.Client,
	rc ResponseClassifier,
	opts ...r8e.Option,
) *Client {
	return &Client{
		httpClient:     hc,
		policy:         r8e.NewPolicy[*http.Response](name, opts...),
		respClassifier: rc,
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

			if c.respClassifier != nil {
				return resp, classifyResponse(resp, c.respClassifier)
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

// classifyResponse runs rc on resp (see [ResponseClassifier]) and returns the
// error the attempt fails with, nil for a success. A non-2xx body is read up to
// MaxClassifiedBody and put back in front of the rest, so the caller still
// reads it whole. When the outcome may be retried, the body is drained and
// closed, as for a Transient status.
func classifyResponse(resp *http.Response, rc ResponseClassifier) error {
	if resp.StatusCode/100 == 2 {
		return settleBody(resp, rc(resp, nil))
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxClassifiedBody))
	if readErr != nil {
		drainAndClose(resp)

		return fmt.Errorf("httpx: read response body: %w", readErr)
	}

	resp.Body = readCloser{
		Reader: io.MultiReader(bytes.NewReader(body), resp.Body),
		Closer: resp.Body,
	}

	return settleBody(resp, rc(resp, body))
}

// settleBody drains and closes resp's body when err is non-nil and may be
// retried, so the connection can be reused, and returns err unchanged.
//
//nolint:wrapcheck // the classifier's error is returned as-is
func settleBody(resp *http.Response, err error) error {
	if err != nil && !r8e.IsPermanent(err) {
		drainAndClose(resp)
	}

	return err
}

// drainAndClose reads resp's body to the end and closes it, best effort.
func drainAndClose(resp *http.Response) {
	//nolint:errcheck // best-effort drain
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
