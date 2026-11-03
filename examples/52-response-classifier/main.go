// Example 52-response-classifier: Demonstrates httpx.NewResponseClient, whose
// ResponseClassifier reads the start of the body to classify a response.
//
// The problem it solves: some APIs answer the same status for opposite cases.
// Here a 403 means either "rate limited, try again" or "forbidden, stop", and
// only the body tells them apart. A Classifier from NewClient sees the status
// code alone, so it must retry both cases or neither.
//
// A ResponseClassifier receives the response and, for a status of 300 or
// above, the first MaxClassifiedBody bytes of the body. It returns nil for a
// success, a transient error to retry, or r8e.Permanent(err) to stop. Returning
// a *httpx.StatusError for the response, wrapped in r8e.Permanent, keeps the
// body open: under WithRetry, Do returns a nil response, and the caller reaches
// the response, and its body, through errors.As on the StatusError.
//
// The call is bounded with a deadline on the context passed to Do, not with
// r8e.WithTimeout. WithTimeout, WithHedge, PerAttemptTimeout and WithTimeBudget
// with PropagateDeadline end the attempt's context when they return, and
// net/http then cuts the body: after Do, the caller could read only the bytes
// already buffered.
//
// The demo runs a local server with three routes: one rate limited once, one
// forbidden, one always rate limited.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// errRateLimited is the transient error the classifier returns for a 403 whose
// body says the client is rate limited.
var errRateLimited = errors.New("rate limited")

func main() {
	ctx := context.Background()

	srv := httptest.NewServer(newAPI())
	defer srv.Close()

	client := httpx.NewResponseClient("orders", srv.Client(), classify,
		r8e.WithRetry(3, r8e.ConstantBackoff(10*time.Millisecond)),
	)

	fmt.Println("=== 403 rate_limited once, then 200 ===")
	call(ctx, client, srv.URL+"/orders/1")

	fmt.Println("\n=== 403 forbidden ===")
	call(ctx, client, srv.URL+"/orders/2")

	fmt.Println("\n=== 403 rate_limited on every attempt ===")
	call(ctx, client, srv.URL+"/orders/3")
}

// classify tells the two 403 cases apart from the body. A rate-limited 403 is
// transient, so the retry tries again. Any other status of 300 or above is
// final: it returns a StatusError for resp, wrapped in r8e.Permanent, which
// keeps the body open for the caller.
func classify(resp *http.Response, body []byte) error {
	switch {
	case resp.StatusCode < http.StatusMultipleChoices:
		return nil
	case resp.StatusCode == http.StatusForbidden &&
		bytes.Contains(body, []byte(`"code":"rate_limited"`)):
		return errRateLimited
	default:
		//nolint:wrapcheck // the classifier's verdict, read by the adapter and the retry
		return r8e.Permanent(&httpx.StatusError{Response: resp, StatusCode: resp.StatusCode})
	}
}

// call sends one GET through client and prints what the caller gets back.
func call(ctx context.Context, client *httpx.Client, url string) {
	// The deadline bounds the whole call, retries included, and the body can
	// still be read in full after Do.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		fmt.Printf("  build request: %v\n", err)

		return
	}

	resp, err := client.Do(callCtx, req)
	if err == nil {
		fmt.Printf("  success: status=%d body=%s\n", resp.StatusCode, readAndClose(resp))

		return
	}

	// Under WithRetry, Do returns a nil response with any error.
	fmt.Printf("  error: %v (resp is nil: %v)\n", err, resp == nil)
	fmt.Printf("  rate limited: %v, retries exhausted: %v\n",
		errors.Is(err, errRateLimited), errors.Is(err, r8e.ErrRetriesExhausted))

	// A permanent StatusError carries the response, its body still open.
	var statusErr *httpx.StatusError
	if errors.As(err, &statusErr) {
		fmt.Printf("  StatusError: status=%d body=%s\n",
			statusErr.StatusCode, readAndClose(statusErr.Response))
	}
}

// readAndClose reads the whole body of resp, then closes it.
func readAndClose(resp *http.Response) string {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close body: %v", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read error: " + err.Error()
	}

	return string(bytes.TrimSpace(body))
}

// newAPI returns the handler of the test server. It counts the attempts per
// route and prints each one, so the output shows what the retry did.
func newAPI() http.Handler {
	var (
		mu       sync.Mutex
		attempts = make(map[string]int)
	)

	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()

		attempts[path]++

		return attempts[path]
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		attempt := count(req.URL.Path)
		fmt.Printf("  server: %s attempt %d\n", req.URL.Path, attempt)

		writer.Header().Set("Content-Type", "application/json")

		switch {
		case req.URL.Path == "/orders/1" && attempt > 1:
			fmt.Fprintln(writer, `{"id":1,"status":"shipped"}`)
		case req.URL.Path == "/orders/2":
			writer.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(writer, `{"code":"forbidden","message":"order 2 belongs to another account"}`)
		default:
			writer.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(writer, `{"code":"rate_limited","message":"slow down"}`)
		}
	})
}
