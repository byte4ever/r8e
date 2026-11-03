package httpx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/byte4ever/r8e"
)

type (
	// ResponseClassifier classifies a response from its status, headers and
	// body, for APIs that answer the same status for opposite cases (a 403
	// for a token to renew or for a moved resource, a 404 for an unknown
	// object or for a route missing during a rollout). It returns nil for a
	// success, or the error the attempt fails with. That error is classified
	// as anywhere in r8e: [r8e.Permanent] stops the retry,
	// [r8e.RetryAfterError] carries a delay hint, and any other error is
	// transient.
	//
	// The adapter adds nothing to that error: no [StatusError], and no
	// Retry-After header is read on this path. Return
	// &StatusError{Response: resp, StatusCode: resp.StatusCode}, wrapped in
	// [r8e.Permanent] or not, to get both back. The policy may still wrap
	// the error ([r8e.ErrRetriesExhausted] once retries run out,
	// [r8e.ErrAttemptTimeout] for a cut attempt), so match it with errors.Is
	// or errors.As, not ==.
	//
	// For a status of 300 or above, body holds the first [MaxClassifiedBody]
	// bytes of the body. Below 300, body is nil and the body is not read, so
	// a streamed success or an upgraded (101) connection stays as it is.
	// body is the classifier's only view of the body: during the call,
	// resp.Body is [http.NoBody]. The classifier must not modify body, since
	// the caller reads the same bytes back.
	//
	// The adapter leaves the body open, to read from the start, in two cases:
	// on a nil error, where Do returns resp, and on a permanent error that
	// carries a [StatusError] for resp, where errors.As finds it even when a
	// retrying policy makes Do return a nil response. Any other error closes
	// the body, after draining it for a status of 200 or above, so the
	// connection can be reused. A static [r8e.WithFallback] drops the error,
	// and that StatusError with it, so it is excluded (see [Client]).
	//
	// An open body is still read on the attempt's context. [r8e.WithTimeout],
	// [r8e.WithHedge], [r8e.PerAttemptTimeout] and [r8e.WithTimeBudget] with
	// [r8e.PropagateDeadline] end that context when they return, whatever the
	// outcome, and net/http then cuts the body: after Do, the caller reads only
	// the bytes already buffered (for a status of 300 or above, at least the
	// start handed to the classifier) and then gets [context.Canceled]. To read
	// a whole body after Do, bound the call with a deadline on the context
	// passed to Do instead (see [Client]).
	//
	// The classifier runs inside the attempt. It may run on a goroutine other
	// than the caller's, concurrently with itself (concurrent Do calls on one
	// Client, the attempts of [r8e.WithHedge]), and after Do has returned (an
	// attempt cut by [r8e.WithTimeout], a losing hedge). Any state it shares
	// must be safe for concurrent use.
	//
	// Pattern: Strategy — the caller injects the classification logic, which
	// sees the status, the headers and the start of the body, without
	// modifying the adapter.
	ResponseClassifier func(resp *http.Response, body []byte) error

	// readCloser pairs a reader with the closer of the body it reads from.
	readCloser struct {
		io.Reader
		io.Closer
	}
)

// MaxClassifiedBody is how many bytes of a response body a
// [ResponseClassifier] receives, for a status of 300 or above.
const MaxClassifiedBody = 64 << 10

// NewResponseClient creates a Client that classifies each response with
// classify, which sees the status, the headers and, for a status of 300 or
// above, the start of the body (see [ResponseClassifier]). It is the variant
// of [NewClient] for APIs whose status code alone does not tell a retriable
// failure from a final one.
//
// A nil error, or a permanent one that carries a [StatusError] for the
// response, leaves the body open, to read from the start, within the limits
// [ResponseClassifier] gives for options that end the attempt's context. Any
// other error drains and closes the body (a 1xx body is only closed), since
// a retry may follow or the policy may drop the response. A failure reading the body is
// transient, and classify is not called. As for any Client, do not pass
// [r8e.WithCoalesce], [r8e.WithCache] or a static [r8e.WithFallback] (see
// [Client]).
func NewResponseClient(
	name string,
	hc *http.Client,
	classify ResponseClassifier,
	opts ...r8e.Option,
) *Client {
	return &Client{
		httpClient:     hc,
		policy:         r8e.NewPolicy[*http.Response](name, opts...),
		respClassifier: classify,
	}
}

// classifyResponse runs classify on resp (see [ResponseClassifier]) and
// returns the error the attempt fails with, nil for a success. For a status
// of 300 or above, the body is read up to MaxClassifiedBody and put back in
// front of the rest, so the caller still reads it whole. settleBody then
// decides whether the body stays open.
func classifyResponse(resp *http.Response, classify ResponseClassifier) error {
	if resp.StatusCode < http.StatusMultipleChoices {
		return settleBody(resp, classifyHidingBody(resp, nil, classify))
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxClassifiedBody))
	if readErr != nil {
		releaseBody(resp)

		return fmt.Errorf("httpx: read response body: %w", readErr)
	}

	resp.Body = readCloser{
		Reader: io.MultiReader(bytes.NewReader(body), resp.Body),
		Closer: resp.Body,
	}

	return settleBody(resp, classifyHidingBody(resp, body, classify))
}

// classifyHidingBody calls classify with resp.Body set to http.NoBody, then
// puts the real body back, so the classifier can neither read, replace nor
// close the body the caller gets.
func classifyHidingBody(resp *http.Response, body []byte, classify ResponseClassifier) error {
	actual := resp.Body
	resp.Body = http.NoBody

	defer func() { resp.Body = actual }()

	return classify(resp, body)
}

// settleBody keeps resp's body open on a nil err, which Do returns with resp,
// and on a permanent err that carries resp in a [StatusError], which the
// caller reaches through the error. Any other err releases the body, since a
// retry may follow or the policy may drop resp. It returns err unchanged.
func settleBody(resp *http.Response, err error) error {
	keepOpen := err == nil || (r8e.IsPermanent(err) && carriesResponse(err, resp))
	if !keepOpen {
		releaseBody(resp)
	}

	return err
}

// carriesResponse reports whether err holds a [StatusError] for resp.
func carriesResponse(err error, resp *http.Response) bool {
	var statusErr *StatusError

	if !errors.As(err, &statusErr) {
		return false
	}

	return statusErr.Response == resp
}

// releaseBody closes resp's body, best effort, after reading it to the end so
// the connection can be reused. An upgraded (1xx) connection cannot be
// reused, and reading it would wait on the peer, so its body is only closed.
func releaseBody(resp *http.Response) {
	if resp.StatusCode >= http.StatusOK {
		//nolint:errcheck // best-effort drain
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	_ = resp.Body.Close()
}
