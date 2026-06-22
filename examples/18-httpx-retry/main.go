// Example 18-httpx-retry: Demonstrates httpx with retry, showing recovery
// from transient failures and retries-exhausted behavior.
//
// Building on example 17, this adds WithRetry so the policy actually acts on the
// classifier's verdict. The four scenarios cover the decisions that matter in
// production: transient failures are retried until they recover; a permanently
// failing dependency exhausts the budget and surfaces ErrRetriesExhausted; a
// permanent 4xx short-circuits after one attempt instead of wasting retries; and
// a 429 with Retry-After is treated as transient and recovered. Retry hooks let
// you observe each attempt.
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
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// classifier maps HTTP status codes to error classes — the same policy as
// example 17. It is what drives every retry decision below: only Transient
// codes (429, 502, 503, 504) get another attempt; Permanent codes stop cold.
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

	// Scenario 1: the happy path for retry. The server is briefly unhealthy —
	// 503 on the first two hits, then 200. This is exactly the kind of blip
	// retry exists to paper over: with enough attempts the client rides out
	// the outage and the caller never sees a failure.
	fmt.Println("=== Transient Recovery (503 → 503 → 200) ===")

	// atomic because httptest serves each request on its own goroutine; the
	// handler increments this counter to decide what to return per attempt.
	var attempt atomic.Int32

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				cur := attempt.Add(1)
				fmt.Printf("  server: attempt %d\n", cur)

				if cur <= 2 {
					writer.WriteHeader(
						http.StatusServiceUnavailable,
					)

					return
				}

				writer.WriteHeader(http.StatusOK)
				fmt.Fprint(writer, `{"result":"recovered"}`)
			},
		),
	)

	client := httpx.NewClient("retry-recover",
		srv.Client(),
		classifier,
		// Up to 5 attempts with exponential backoff: each retry waits longer
		// than the last, giving a struggling server room to recover instead
		// of being hammered at a fixed cadence.
		r8e.WithRetry(
			5,
			r8e.ExponentialBackoff(10*time.Millisecond),
		),
		// The OnRetry hook fires once per retry — handy for logging/metrics so
		// the retries aren't invisible. It is purely observational and does
		// not change the outcome.
		r8e.WithHooks(&r8e.Hooks{
			OnRetry: func(num int, err error) {
				fmt.Printf(
					"  [hook] retry #%d: %v\n", num, err,
				)
			},
		}),
	)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, srv.URL, http.NoBody,
	)
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Do(ctx, req)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  success! status: %d\n", resp.StatusCode)
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()

	// Scenario 2: retry is not magic. When the server is 503 on every attempt,
	// the budget runs out and the call fails. The contract here is that the
	// error wraps the ErrRetriesExhausted sentinel (checkable with errors.Is)
	// AND still carries the last StatusError, so callers learn both that we
	// gave up and what the final response was.
	fmt.Println("\n=== Retries Exhausted (always 503) ===")

	srv = httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(
					http.StatusServiceUnavailable,
				)
			},
		),
	)

	client = httpx.NewClient("retry-exhausted",
		srv.Client(),
		classifier,
		r8e.WithRetry(
			3,
			r8e.ConstantBackoff(10*time.Millisecond),
		),
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
		// errors.Is sees through the wrapping to the sentinel: this is how a
		// caller distinguishes "gave up after retrying" from any other failure.
		fmt.Printf("  retries exhausted: %v\n",
			errors.Is(err, r8e.ErrRetriesExhausted))

		// The last attempt's StatusError survives alongside the sentinel, so
		// we can still report the final status code that caused the give-up.
		var statusErr *httpx.StatusError
		if errors.As(err, &statusErr) {
			fmt.Printf("  last status code: %d\n",
				statusErr.StatusCode)
		}
	}

	if resp != nil {
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()

	// Scenario 3: the classifier earning its keep. A 400 is a client mistake —
	// the request is wrong, so retrying it would just fail five times and add
	// latency for nothing. Marked Permanent, it short-circuits after a single
	// attempt; the server-side counter proves only one request was ever sent.
	fmt.Println("\n=== Permanent Stops Retries (400 on first attempt) ===")

	// Reset the shared counter so this scenario's attempt count starts at 1.
	attempt.Store(0)

	srv = httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				cur := attempt.Add(1)
				fmt.Printf("  server: attempt %d\n", cur)

				writer.WriteHeader(http.StatusBadRequest)
			},
		),
	)

	client = httpx.NewClient("retry-permanent",
		srv.Client(),
		classifier,
		r8e.WithRetry(
			5,
			r8e.ConstantBackoff(10*time.Millisecond),
		),
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
		fmt.Printf("  is permanent: %v\n", r8e.IsPermanent(err))
		fmt.Println("  only 1 attempt (retries skipped)")
	}

	if resp != nil {
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()

	// Scenario 4: 429 Too Many Requests. The classifier treats it as transient,
	// so the client backs off and retries rather than failing. The server also
	// sets a Retry-After header on the 429; httpx's StatusError exposes that to
	// retry, which honors the server-requested delay over its own backoff —
	// cooperative rate-limit handling for free.
	fmt.Println("\n=== Rate-Limited Recovery (429 → 200) ===")

	// Reset the shared counter again for a clean per-scenario attempt count.
	attempt.Store(0)

	srv = httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				cur := attempt.Add(1)
				fmt.Printf("  server: attempt %d\n", cur)

				if cur == 1 {
					writer.Header().Set("Retry-After", "1")
					writer.WriteHeader(
						http.StatusTooManyRequests,
					)

					return
				}

				writer.WriteHeader(http.StatusOK)
				fmt.Fprint(writer, `{"status":"ok"}`)
			},
		),
	)

	client = httpx.NewClient("retry-ratelimit",
		srv.Client(),
		classifier,
		r8e.WithRetry(
			3,
			r8e.ConstantBackoff(50*time.Millisecond),
		),
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
	} else {
		fmt.Printf("  success! status: %d\n", resp.StatusCode)
		resp.Body.Close() //nolint:errcheck // example program
	}

	srv.Close()
}
