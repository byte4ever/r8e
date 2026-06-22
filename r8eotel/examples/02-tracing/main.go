// Example 02-tracing: wrap an r8e policy with OpenTelemetry trace spans. Trace
// opens one root span per Do call (named after the policy) and one child span per
// fn invocation (the initial attempt and each retry / hedge fork), so retry
// chains and hedge races are visible in any OTel backend (Jaeger, Tempo, …).
// Here an in-memory exporter captures the finished spans so we can print them.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8eotel"
)

func main() {
	ctx := context.Background()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	defer tp.Shutdown(ctx) //nolint:errcheck // example cleanup

	policy := r8e.NewPolicy[string]("checkout",
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
	)

	// Trace decorates the policy as a drop-in: same Do signature.
	traced := r8eotel.Trace(policy, tp)

	// Fail twice, then succeed — so Do makes three attempts.
	attempts := 0

	//nolint:errcheck // example: the final attempt succeeds
	result, _ := traced.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		if attempts < 3 {
			return "", errors.New("transient")
		}

		return "ok", nil
	})

	fmt.Printf("result: %q after %d attempts\n\n", result, attempts)
	fmt.Println("=== captured spans (children finish first, then the root) ===")

	spans := exporter.GetSpans()
	for i := range spans {
		fmt.Printf("  %-18s", spans[i].Name)

		for _, attr := range spans[i].Attributes {
			fmt.Printf("  %s=%v", attr.Key, attr.Value.AsInterface())
		}

		fmt.Println()
	}
}
