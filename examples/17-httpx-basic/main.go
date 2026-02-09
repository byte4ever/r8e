// Example 17-httpx-basic: Demonstrates the httpx adapter for wrapping
// an HTTP client with a resilience policy and status code classification.
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

	// --- Success (200 OK) ---
	fmt.Println("=== Success (200 OK) ===")

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusOK)
				fmt.Fprint(writer, `{"status":"ok"}`)
			},
		),
	)

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

	// --- Permanent error (400 Bad Request) ---
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
		fmt.Printf("  is permanent: %v\n", r8e.IsPermanent(err))

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

	// --- Transient error (503 Service Unavailable) ---
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
