// Example 17-httpx-basic: Demonstrates the httpx adapter for wrapping
// an HTTP client with a resilience policy and status code classification.
//
// r8e's resilience reasons in terms of Transient vs Permanent errors, but an
// HTTP call's outcome is a status code, not an error. The httpx adapter bridges
// that gap: you supply a Classifier that maps each status code to Success,
// Transient, or Permanent, and the adapter turns the response into the right
// error class so retry/timeout/etc. behave correctly. This example exercises
// all three paths (200, 400, 503) and shows how to recover the original
// response from the error chain via errors.As + StatusError.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// classifier maps HTTP status codes to error classes. This is the policy that
// decides what is worth retrying: 2xx is success; the standard "try again later"
// codes (429 rate-limit, 502/503/504 gateway/unavailable) are transient; and
// everything else — chiefly 4xx client errors — is permanent, because retrying a
// malformed request will only fail the same way and waste the retry budget.
func classifier(code int) httpx.ErrorClass {
	switch {
	case code >= 200 && code < 300:
		return httpx.Success
	case code == 429, code == 502, code == 503, code == 504:
		return httpx.Transient
	default:
		return httpx.Permanent
	}
}

func main() {
	ctx := context.Background()

	// Path 1: a clean 200. We spin up a throwaway test server so the example
	// is self-contained (no external dependency to flake on). The classifier
	// maps 200 to Success, so client.Do returns the response with a nil error
	// and the policy stays out of the way.
	fmt.Println("=== Success (200 OK) ===")

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusOK)
				fmt.Fprint(writer, `{"status":"ok"}`)
			},
		),
	)

	// NewClient binds three things together: the underlying http.Client
	// (here the test server's, so TLS trust is set up for us), the classifier,
	// and the r8e options. The timeout bounds how long any single attempt may
	// run before the policy gives up.
	client := httpx.NewClient("example-api",
		srv.Client(),
		classifier,
		r8e.WithTimeout(2*time.Second),
	)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, srv.URL, http.NoBody,
	)
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Do(ctx, req)
	if err != nil {
		fmt.Printf("  unexpected error: %v\n", err)
	} else {
		fmt.Printf("  status: %d\n", resp.StatusCode)
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()

	// Path 2: a 400. The classifier marks it Permanent, so client.Do returns a
	// non-nil error wrapping a StatusError. The point of this section is the
	// errors.As below: even though the error has passed through the policy, the
	// original *http.Response is still reachable for inspecting status/headers.
	fmt.Println("\n=== Permanent Error (400 Bad Request) ===")

	srv = httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(writer, `{"error":"invalid input"}`)
			},
		),
	)

	client = httpx.NewClient("example-api-perm",
		srv.Client(),
		classifier,
		r8e.WithTimeout(2*time.Second),
	)

	req, err = http.NewRequestWithContext(
		ctx, http.MethodGet, srv.URL, http.NoBody,
	)
	if err != nil {
		log.Fatal(err)
	}

	resp, err = client.Do(ctx, req)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		// IsPermanent confirms the error class without unwrapping anything.
		fmt.Printf("  is permanent: %v\n", r8e.IsPermanent(err))

		// errors.As digs the StatusError out of the wrapped error chain.
		// On the permanent path the body is left unread, so Response is
		// still usable to read the error payload or headers.
		var statusErr *httpx.StatusError
		if errors.As(err, &statusErr) {
			fmt.Printf("  status code: %d\n", statusErr.StatusCode)
			fmt.Printf(
				"  response available: %v\n",
				statusErr.Response != nil,
			)
		}
	}

	if resp != nil {
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()

	// Path 3: a 503. The classifier marks it Transient. Here there is no retry
	// option configured, so a single attempt fails and surfaces the transient
	// error — IsTransient reports true, telling a caller the request is worth
	// retrying. (Example 18 adds WithRetry to actually act on that.)
	fmt.Println("\n=== Transient Error (503 Service Unavailable) ===")

	srv = httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusServiceUnavailable)
			},
		),
	)

	client = httpx.NewClient("example-api-transient",
		srv.Client(),
		classifier,
		r8e.WithTimeout(2*time.Second),
	)

	req, err = http.NewRequestWithContext(
		ctx, http.MethodGet, srv.URL, http.NoBody,
	)
	if err != nil {
		log.Fatal(err)
	}

	resp, err = client.Do(ctx, req)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		fmt.Printf("  is transient: %v\n", r8e.IsTransient(err))

		var statusErr *httpx.StatusError
		if errors.As(err, &statusErr) {
			fmt.Printf("  status code: %d\n", statusErr.StatusCode)
		}
	}

	if resp != nil {
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()
}
