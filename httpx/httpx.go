package httpx

import (
	"net/http"
	"strconv"

	"github.com/byte4ever/r8e"
)

// ErrorClass tells the resilience layer how to treat an HTTP
// status code.
type ErrorClass int

const (
	// Success means the request succeeded (e.g. 2xx).
	Success ErrorClass = iota
	// Transient means the error is retriable (e.g. 429, 503).
	Transient
	// Permanent means the error is non-retriable (e.g. 400).
	Permanent
)

// Classifier maps an HTTP status code to an ErrorClass.
//
// Pattern: Strategy — caller injects classification logic
// without modifying the adapter.
type Classifier func(statusCode int) ErrorClass

// StatusError is returned when the Classifier marks a status
// code as Transient or Permanent. The original response
// remains accessible for header/body inspection.
type StatusError struct {
	// Response is the original HTTP response that triggered
	// the error. The body has not been read or closed.
	Response   *http.Response
	StatusCode int
}

// Error returns a human-readable description of the status
// error.
func (e *StatusError) Error() string {
	return "http status " + strconv.Itoa(e.StatusCode)
}

// Client wraps an http.Client with an r8e resilience policy
// and HTTP status code classification.
//
// Pattern: Adapter — bridges net/http and r8e's resilience
// policy by translating HTTP status codes into r8e error
// classification.
type Client struct {
	hc *http.Client
	p  *r8e.Policy[*http.Response]
	cl Classifier
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
