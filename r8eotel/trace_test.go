package r8eotel_test

import (
	"context"
	"errors"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8eotel"
)

// recorder builds a TracerProvider backed by an in-memory exporter.
// Spans are exported synchronously (WithSyncer), so exp.GetSpans() is
// accurate immediately after Do() returns.
func recorder() (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))

	return tp, exp
}

// spanNames returns the names of all recorded spans in export order.
func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}

	return names
}

// int64Attr returns the int64 value of an attribute by key, or -1 if absent.
func int64Attr(span tracetest.SpanStub, key string) int64 {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsInt64()
		}
	}

	return -1
}

// strAttr returns the string value of an attribute by key, or "" if absent.
func strAttr(span tracetest.SpanStub, key string) string {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}

	return ""
}

func TestTracedPolicyDo_SuccessSpans(t *testing.T) {
	t.Parallel()

	tp, exp := recorder()
	policy := r8e.NewPolicy[string]("svc-call")
	traced := r8eotel.Trace(policy, tp)

	result, err := traced.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	spans := exp.GetSpans()
	require.Len(t, spans, 2, "expected 1 child + 1 root")

	child, root := spans[0], spans[1]

	// child span
	assert.Equal(t, "svc-call/attempt", child.Name)
	assert.Equal(t, int64(1), int64Attr(child, "r8e.attempt.number"))

	// root span
	assert.Equal(t, "svc-call", root.Name)
	assert.Equal(t, "svc-call", strAttr(root, "r8e.policy"))
	assert.Equal(t, int64(1), int64Attr(root, "r8e.attempts"))
	assert.Equal(t, "", strAttr(root, "r8e.rejection_reason"), "no rejection on success")
}

func TestTracedPolicyDo_RetryCreatesChildSpans(t *testing.T) {
	t.Parallel()

	tp, exp := recorder()
	policy := r8e.NewPolicy[string]("retry-svc",
		r8e.WithRetry(5, r8e.ConstantBackoff(0)),
	)
	traced := r8eotel.Trace(policy, tp)

	call := 0
	result, err := traced.Do(context.Background(), func(_ context.Context) (string, error) {
		call++
		if call < 3 {
			return "", r8e.Transient(errors.New("flaky"))
		}

		return "done", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result)

	spans := exp.GetSpans()
	require.Len(t, spans, 4, "expected 3 child spans + 1 root")

	// Children 0, 1 failed; child 2 succeeded.
	assert.Equal(t, "retry-svc/attempt", spans[0].Name)
	assert.Equal(t, int64(1), int64Attr(spans[0], "r8e.attempt.number"))

	assert.Equal(t, "retry-svc/attempt", spans[1].Name)
	assert.Equal(t, int64(2), int64Attr(spans[1], "r8e.attempt.number"))

	assert.Equal(t, "retry-svc/attempt", spans[2].Name)
	assert.Equal(t, int64(3), int64Attr(spans[2], "r8e.attempt.number"))

	// Root span
	root := spans[3]
	assert.Equal(t, "retry-svc", root.Name)
	assert.Equal(t, int64(3), int64Attr(root, "r8e.attempts"))
	assert.Equal(t, "", strAttr(root, "r8e.rejection_reason"))
}

func TestTracedPolicyDo_RetriesExhaustedSetsRejectionReason(t *testing.T) {
	t.Parallel()

	tp, exp := recorder()
	policy := r8e.NewPolicy[string]("exhausted-svc",
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
	)
	traced := r8eotel.Trace(policy, tp)

	boom := errors.New("boom")
	_, err := traced.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", r8e.Transient(boom)
	})
	require.ErrorIs(t, err, r8e.ErrRetriesExhausted)

	spans := exp.GetSpans()
	// WithRetry(3) = 3 total attempts = 3 child spans + 1 root.
	require.Len(t, spans, 4, "expected 3 child spans + 1 root")

	root := spans[3]
	assert.Equal(t, int64(3), int64Attr(root, "r8e.attempts"))
	assert.Equal(t, "retries_exhausted", strAttr(root, "r8e.rejection_reason"))
}

func TestTracedPolicyDo_ZeroAttemptsOnPolicyRejection(t *testing.T) {
	t.Parallel()

	// Rate limiter with 1 token/s: first call consumes the token, second is
	// rejected at the policy layer before fn is ever called.
	tp, exp := recorder()
	policy := r8e.NewPolicy[string]("rl-svc",
		r8e.WithRateLimit(1),
	)
	traced := r8eotel.Trace(policy, tp)

	ctx := context.Background()

	// First call — admitted.
	_, err := traced.Do(ctx, func(_ context.Context) (string, error) { return "ok", nil })
	require.NoError(t, err)
	exp.Reset()

	// Second call immediately after — rate limited; fn never called.
	_, err = traced.Do(ctx, func(_ context.Context) (string, error) { return "ok", nil })
	require.ErrorIs(t, err, r8e.ErrRateLimited)

	spans := exp.GetSpans()
	require.Len(t, spans, 1, "only root span; fn was never invoked")

	root := spans[0]
	assert.Equal(t, "rl-svc", root.Name)
	assert.Equal(t, int64(0), int64Attr(root, "r8e.attempts"))
	assert.Equal(t, "rate_limited", strAttr(root, "r8e.rejection_reason"))
}

func TestTracedPolicyDo_SpanNamesMatchPolicyName(t *testing.T) {
	t.Parallel()

	tp, exp := recorder()
	policy := r8e.NewPolicy[int]("my-custom-name")
	traced := r8eotel.Trace(policy, tp)

	_, _ = traced.Do(context.Background(), func(_ context.Context) (int, error) {
		return 42, nil
	})

	spans := exp.GetSpans()
	require.Len(t, spans, 2)

	assert.Equal(t, "my-custom-name/attempt", spanNames(spans)[0])
	assert.Equal(t, "my-custom-name", spanNames(spans)[1])
}

func TestTracedPolicyDo_ContextPropagation(t *testing.T) {
	t.Parallel()

	// Verifies that fn receives a context that carries the active child span,
	// so fn can start grandchild spans and they nest correctly.
	tp, exp := recorder()
	policy := r8e.NewPolicy[string]("propagation-svc")
	traced := r8eotel.Trace(policy, tp)

	var grandchildTraceID trace.TraceID

	_, err := traced.Do(context.Background(), func(ctx context.Context) (string, error) {
		_, grandchild := tp.Tracer("test").Start(ctx, "grandchild")
		grandchildTraceID = grandchild.SpanContext().TraceID()
		grandchild.End()

		return "ok", nil
	})
	require.NoError(t, err)

	spans := exp.GetSpans()
	// child span + grandchild + root = 3 spans.
	require.GreaterOrEqual(t, len(spans), 3)

	// All spans share the same trace ID (they are in the same trace).
	traceID := spans[0].SpanContext.TraceID()
	assert.NotZero(t, traceID)

	for _, s := range spans {
		assert.Equal(t, traceID, s.SpanContext.TraceID(), "span %q has wrong trace ID", s.Name)
	}

	// The grandchild's trace ID matches (ctx with active span was propagated into fn).
	assert.Equal(t, traceID, grandchildTraceID)
}

func TestTracedPolicyForwarding(t *testing.T) {
	t.Parallel()

	tp, _ := recorder()
	policy := r8e.NewPolicy[string]("forward-svc",
		r8e.WithRetry(2, r8e.ConstantBackoff(0)),
	)
	traced := r8eotel.Trace(policy, tp)

	// Name forwards correctly.
	assert.Equal(t, "forward-svc", traced.Name())

	// Metrics returns a non-zero snapshot after a call.
	_, _ = traced.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", r8e.Transient(errors.New("x"))
	})
	m := traced.Metrics()
	assert.Equal(t, "forward-svc", m.Name)

	// HealthStatus returns a valid status.
	status := traced.HealthStatus()
	assert.NotEmpty(t, status.Name)

	// Reconfigure delegates: an empty config is a valid no-op.
	err := traced.Reconfigure(r8e.PolicyConfig{})
	assert.NoError(t, err)
}
