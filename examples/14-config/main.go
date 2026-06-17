// Example 14-config: Demonstrates loading policy configuration from a JSON file
// and retrieving typed policies with GetPolicy.
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

	// Resolve config.json relative to this source file's directory.
	configPath := configDir("config.json")

	// Load configuration from JSON.
	store, err := r8econf.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Config loaded successfully ===")

	// --- Get a typed policy from config ---
	fmt.Println("\n=== Payment API Policy ===")

	paymentPolicy, err := r8econf.GetPolicy[string](store, "payment-api",
		// Additional code-level options can augment the config.
		r8e.WithFallback("payment service unavailable"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build payment policy: %v\n", err)
		os.Exit(1)
	}

	// Successful call.
	result, err := paymentPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "payment processed: $42.00", nil
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)

	// Failing call — fallback kicks in.
	result, err = paymentPolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("payment gateway down")
		},
	)
	fmt.Printf("  result: %q, err: %v\n", result, err)

	// --- Notification API policy ---
	fmt.Println("\n=== Notification API Policy ===")

	notifyPolicy, err := r8econf.GetPolicy[string](store, "notification-api")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build notify policy: %v\n", err)
		os.Exit(1)
	}

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
// source file.
func configDir(filename string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), filename)
}
