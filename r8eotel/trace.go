package r8eotel

import (
	"context"
	"errors"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/byte4ever/r8e"
)

// TracedPolicy wraps an r8e.Policy[T] with OpenTelemetry trace spans.
// Each Do call opens a root span named after the policy; every actual fn
// invocation inside the policy (initial attempt, each retry, each hedge fork)
// opens a child span so individual attempt latencies are visible in the trace.
//
// TracedPolicy forwards all methods of *r8e.Policy[T] (Do, Name, Reconfigure,
// Metrics, HealthStatus) so it can be used as a drop-in decorator.
//
// Pattern: Decorator — adds trace instrumentation without altering policy behaviour.
type TracedPolicy[T any] struct {
	policy *r8e.Policy[T]
	tracer trace.Tracer
}

// tracerName is the instrumentation scope used when obtaining a tracer from
// a TracerProvider.
const tracerName = "github.com/byte4ever/r8e/r8eotel"

// Trace returns a TracedPolicy that wraps policy with OpenTelemetry spans.
// Pass otel.GetTracerProvider() to use the globally configured provider, or
// supply a custom TracerProvider for testing or multi-tenant setups.
func Trace[T any](policy *r8e.Policy[T], tp trace.TracerProvider) *TracedPolicy[T] {
	return &TracedPolicy[T]{
		policy: policy,
		tracer: tp.Tracer(tracerName),
	}
}

// Do executes fn through the wrapped policy. It records the overall call as a
// root span (named after the policy) and each fn invocation as a child span so
// retries and hedge forks appear as individual, timed children in the trace.
//
// The root span carries:
//   - r8e.policy   — the policy name
//   - r8e.attempts — number of fn invocations counted before Do returns. With
//     hedge policies the losing goroutine may call fn after Do returns; its
//     invocation is not reflected in this counter, though its child span is
//     still correctly exported and visible in the trace.
//   - r8e.rejection_reason — present on error; classifies the sentinel
//     (circuit_open, retries_exhausted, timeout, …) or "error" for a plain
//     fn error.
//
// Each child span carries r8e.attempt.number (1-indexed).
//
//nolint:ireturn // generic T is the intended return; ireturn fires on any generic func
func (p *TracedPolicy[T]) Do(
	ctx context.Context,
	fn func(context.Context) (T, error),
) (T, error) {
	name := p.policy.Name()
	childName := name + "/attempt"

	spanCtx, span := p.tracer.Start(ctx, name)
	defer span.End()

	var attempts atomic.Int64

	wrappedFn := func(callCtx context.Context) (T, error) {
		n := attempts.Add(1)

		callCtx, child := p.tracer.Start(callCtx, childName,
			trace.WithAttributes(attribute.Int64("r8e.attempt.number", n)))
		defer child.End()

		result, err := fn(callCtx)
		if err != nil {
			child.RecordError(err)
			child.SetStatus(codes.Error, err.Error())
		} else {
			child.SetStatus(codes.Ok, "")
		}

		return result, err
	}

	result, err := p.policy.Do(spanCtx, wrappedFn)

	span.SetAttributes(
		attribute.String("r8e.policy", name),
		attribute.Int64("r8e.attempts", attempts.Load()),
	)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("r8e.rejection_reason", rejectionReason(err)))
	} else {
		span.SetStatus(codes.Ok, "")
	}

	//nolint:wrapcheck // thin decorator; the policy error is the caller's contract
	return result, err
}

// Name returns the name of the wrapped policy.
func (p *TracedPolicy[T]) Name() string { return p.policy.Name() }

// Reconfigure delegates to the wrapped policy's Reconfigure.
//
//nolint:wrapcheck,gocritic // forwarding method; value-passed API mirrors Policy[T].Reconfigure
func (p *TracedPolicy[T]) Reconfigure(cfg r8e.PolicyConfig) error {
	return p.policy.Reconfigure(cfg)
}

// Metrics delegates to the wrapped policy's Metrics.
func (p *TracedPolicy[T]) Metrics() r8e.PolicyMetrics { return p.policy.Metrics() }

// HealthStatus delegates to the wrapped policy's HealthStatus.
func (p *TracedPolicy[T]) HealthStatus() r8e.PolicyStatus { return p.policy.HealthStatus() }

// rejectionReason maps known r8e sentinel errors to a short classification
// string suitable for dashboard filtering on the r8e.rejection_reason attribute.
func rejectionReason(err error) string {
	switch {
	case errors.Is(err, r8e.ErrCircuitOpen):
		return "circuit_open"
	case errors.Is(err, r8e.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, r8e.ErrBulkheadFull):
		return "bulkhead_full"
	case errors.Is(err, r8e.ErrBulkheadTimeout):
		return "bulkhead_timeout"
	case errors.Is(err, r8e.ErrConcurrencyLimited):
		return "concurrency_limited"
	case errors.Is(err, r8e.ErrThrottled):
		return "throttled"
	case errors.Is(err, r8e.ErrSLOShed):
		return "slo_shed"
	case errors.Is(err, r8e.ErrTimeout):
		return "timeout"
	case errors.Is(err, r8e.ErrTimeBudgetExceeded):
		return "time_budget_exceeded"
	case errors.Is(err, r8e.ErrConcurrencyBudgetExceeded):
		return "concurrency_budget_exceeded"
	case errors.Is(err, r8e.ErrRetriesExhausted):
		return "retries_exhausted"
	case errors.Is(err, r8e.ErrPanic):
		return "panic_recovered"
	default:
		return "error"
	}
}
