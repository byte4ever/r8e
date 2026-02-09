// Example 18-httpx-retry: Demonstrates httpx with retry, showing recovery
// from transient failures and retries-exhausted behavior.
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

// classifier maps HTTP status codes to error classes.
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

	// --- Transient failure recovers after 2 retries ---
	fmt.Println("=== Transient Recovery (503 → 503 → 200) ===")

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
		r8e.WithRetry(
			5,
			r8e.ExponentialBackoff(10*time.Millisecond),
		),
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

	// --- Retries exhausted ---
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
		fmt.Printf("  retries exhausted: %v\n",
			errors.Is(err, r8e.ErrRetriesExhausted))

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

	// --- Permanent error stops retries immediately ---
	fmt.Println("\n=== Permanent Stops Retries (400 on first attempt) ===")

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

	// --- Rate-limited (429) is transient ---
	fmt.Println("\n=== Rate-Limited Recovery (429 → 200) ===")

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
