// Example 23-retry-after: Demonstrates honoring an HTTP Retry-After header. When
// a server answers 429 with "Retry-After: 1", the httpx client waits the second
// the server asked for (±10% jitter) instead of its own configured backoff.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

func main() {
	// The server rate-limits the first call with 429 + Retry-After: 1, then
	// succeeds.
	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				writer.Header().Set("Retry-After", "1")
				writer.WriteHeader(http.StatusTooManyRequests)

				return
			}

			writer.WriteHeader(http.StatusOK)
		},
	))
	defer server.Close()

	// Classify 429 as transient (retriable); everything 2xx is success.
	classify := func(code int) httpx.ErrorClass {
		switch {
		case code == http.StatusTooManyRequests:
			return httpx.Transient
		case code >= 400:
			return httpx.Permanent
		default:
			return httpx.Success
		}
	}

	// A deliberately tiny 20ms backoff: if Retry-After were ignored, the retry
	// would fire almost immediately. Because it is honored, the retry waits ~1s.
	client := httpx.NewClient("api", http.DefaultClient, classify,
		r8e.WithRetry(3, r8e.ConstantBackoff(20*time.Millisecond)),
	)

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		fmt.Printf("build request: %v\n", err)

		return
	}

	start := time.Now()

	resp, err := client.Do(context.Background(), req)
	if err != nil {
		fmt.Printf("unexpected error: %v\n", err)

		return
	}

	defer func() { _ = resp.Body.Close() }()

	fmt.Printf("status:        %d after %d call(s)\n", resp.StatusCode, calls.Load())
	fmt.Printf("waited:        %dms (≈ the server's Retry-After: 1s, not the 20ms backoff)\n",
		time.Since(start).Milliseconds())
}
