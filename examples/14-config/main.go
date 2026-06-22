// Example 14-config: Demonstrates loading policy configuration from a JSON file
// and retrieving typed policies with GetPolicy.
//
// The problem: hard-coding resilience knobs (timeouts, retry counts, breaker
// thresholds) forces a rebuild and redeploy to tune them. r8econf moves those
// values into a JSON file that ops can edit per environment, while the core
// r8e package stays dependency-free — the file-loading concern lives entirely
// in the r8econf edge package. Config is validated eagerly at load time, so a
// typo fails fast at startup instead of at the first request.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8econf"
)

func main() {
	ctx := context.Background()

	// Resolve config.json next to this source file rather than relative to the
	// process working directory, so `go run` works no matter where it's
	// launched from.
	configPath := configDir("config.json")

	// Load and validate the whole file up front. Any malformed duration,
	// unknown backoff strategy, or bad JSON surfaces here at startup — we'd
	// rather crash on boot than discover a typo when the first payment fails.
	store, err := r8econf.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Config loaded successfully ===")

	// --- Get a typed policy from config ---
	// GetPolicy materializes the named config block into a typed Policy[string].
	// The generic parameter is a compile-time choice the JSON can't express, so
	// it lives in code; the timeout/retry/breaker values come from the file.
	fmt.Println("\n=== Payment API Policy ===")

	// Code-level options are applied AFTER the config options, so they win on
	// conflict. Here WithFallback isn't in the JSON at all — it augments the
	// loaded policy with a last-resort value to return when everything else
	// fails, keeping environment-specific behaviour out of the shared config.
	paymentPolicy, err := r8econf.GetPolicy[string](store, "payment-api",
		// Additional code-level options can augment the config.
		r8e.WithFallback("payment service unavailable"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build payment policy: %v\n", err)
		os.Exit(1)
	}

	// Happy path: the work succeeds, so none of the resilience layers do
	// anything visible and we just get the result back.
	result, err := paymentPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "payment processed: $42.00", nil
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)

	// Failure path: the work returns an error, so the WithFallback option we
	// added in code substitutes its stand-in value instead of propagating the
	// failure to the caller. Note err is nil here — the fallback "handled" it.
	result, err = paymentPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("payment gateway down")
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)

	// --- Notification API policy ---
	// A second policy from the same store, this one configured purely by the
	// file (no code-level options). It uses retry with constant backoff per
	// the JSON.
	fmt.Println("\n=== Notification API Policy ===")

	notifyPolicy, err := r8econf.GetPolicy[string](store, "notification-api")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build notify policy: %v\n", err)
		os.Exit(1)
	}

	// Fail the first two attempts with Transient errors. Marking them Transient
	// is what tells the retry layer they're worth re-attempting — a plain error
	// would be treated as permanent and not retried. The closure recovers on
	// the third try, so the retry config (max_attempts: 3) is exactly enough.
	attempt := 0
	result, err = notifyPolicy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		if attempt < 3 {
			return "", r8e.Transient(errors.New("notification service busy"))
		}

		return "notification sent", nil
	})
	fmt.Printf(
		"  result: %q, err: %v (succeeded on attempt %d)\n",
		result,
		err,
		attempt,
	)

	// --- Unknown policy name: creates a bare policy ---
	// Asking for a name the config doesn't define is deliberately NOT an error.
	// You get a bare policy built only from whatever code options you pass
	// (none here). This lets a service migrate gradually: code asks for every
	// policy it wants, and ops fills in real config for them one at a time.
	fmt.Println("\n=== Unknown Policy (bare, no config) ===")

	barePolicy, err := r8econf.GetPolicy[string](store, "unknown-service")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build bare policy: %v\n", err)
		os.Exit(1)
	}

	result, err = barePolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "bare policy works", nil
	})
	fmt.Printf("  result: %q, err: %v\n", result, err)
}

// configDir returns the absolute path to a file in the same directory as this
// source file. We derive it from runtime.Caller rather than os.Getwd so the
// example finds config.json regardless of the caller's working directory.
func configDir(filename string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), filename)
}
