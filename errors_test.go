package r8e

import (
	"errors"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Transient wrapping and detection
// ---------------------------------------------------------------------------

func TestTransientWrapsError(t *testing.T) {
	cause := errors.New("connection reset")
	err := Transient(cause)

	if err == nil {
		t.Fatal("Transient(non-nil) returned nil")
	}
	if got := err.Error(); got != "transient: connection reset" {
		t.Fatalf("Error() = %q, want %q", got, "transient: connection reset")
	}
}

func TestTransientNilReturnsNil(t *testing.T) {
	if err := Transient(nil); err != nil {
		t.Fatalf("Transient(nil) = %v, want nil", err)
	}
}

func TestIsTransientDetectsTransient(t *testing.T) {
	err := Transient(errors.New("oops"))
	if !IsTransient(err) {
		t.Fatal("IsTransient(Transient(err)) = false, want true")
	}
}

func TestIsTransientUnclassifiedTreatedAsTransient(t *testing.T) {
	err := errors.New("some random error")
	if !IsTransient(err) {
		t.Fatal("IsTransient(unclassified) = false, want true")
	}
}

func TestIsTransientNilReturnsFalse(t *testing.T) {
	if IsTransient(nil) {
		t.Fatal("IsTransient(nil) = true, want false")
	}
}

func TestIsTransientPermanentReturnsFalse(t *testing.T) {
	err := Permanent(errors.New("bad request"))
	if IsTransient(err) {
		t.Fatal("IsTransient(Permanent(err)) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Permanent wrapping and detection
// ---------------------------------------------------------------------------

func TestPermanentWrapsError(t *testing.T) {
	cause := errors.New("invalid argument")
	err := Permanent(cause)

	if err == nil {
		t.Fatal("Permanent(non-nil) returned nil")
	}
	if got := err.Error(); got != "permanent: invalid argument" {
		t.Fatalf("Error() = %q, want %q", got, "permanent: invalid argument")
	}
}

func TestPermanentNilReturnsNil(t *testing.T) {
	if err := Permanent(nil); err != nil {
		t.Fatalf("Permanent(nil) = %v, want nil", err)
	}
}

func TestIsPermanentDetectsPermanent(t *testing.T) {
	err := Permanent(errors.New("oops"))
	if !IsPermanent(err) {
		t.Fatal("IsPermanent(Permanent(err)) = false, want true")
	}
}

func TestIsPermanentUnclassifiedReturnsFalse(t *testing.T) {
	err := errors.New("some random error")
	if IsPermanent(err) {
		t.Fatal("IsPermanent(unclassified) = true, want false")
	}
}

func TestIsPermanentNilReturnsFalse(t *testing.T) {
	if IsPermanent(nil) {
		t.Fatal("IsPermanent(nil) = true, want false")
	}
}

func TestIsPermanentTransientReturnsFalse(t *testing.T) {
	err := Transient(errors.New("timeout"))
	if IsPermanent(err) {
		t.Fatal("IsPermanent(Transient(err)) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Unwrap / errors.Is / errors.As support
// ---------------------------------------------------------------------------

func TestTransientUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := Transient(cause)

	if !errors.Is(err, cause) {
		t.Fatal("errors.Is(Transient(cause), cause) = false, want true")
	}
}

func TestPermanentUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := Permanent(cause)

	if !errors.Is(err, cause) {
		t.Fatal("errors.Is(Permanent(cause), cause) = false, want true")
	}
}

// Use a proper custom error for errors.As testing.
type codedError struct {
	code int
	msg  string
}

func (e *codedError) Error() string { return e.msg }

func TestTransientErrorsAsCustomType(t *testing.T) {
	cause := &codedError{code: 42, msg: "bad thing"}
	err := Transient(cause)

	var target *codedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As(Transient(cause), &codedError) = false, want true")
	}
	if target.code != 42 {
		t.Fatalf("target.code = %d, want 42", target.code)
	}
}

func TestPermanentErrorsAsCustomType(t *testing.T) {
	cause := &codedError{code: 99, msg: "really bad"}
	err := Permanent(cause)

	var target *codedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As(Permanent(cause), &codedError) = false, want true")
	}
	if target.code != 99 {
		t.Fatalf("target.code = %d, want 99", target.code)
	}
}

// ---------------------------------------------------------------------------
// Wrapping wrapped transient/permanent with fmt.Errorf should still be
// detectable.
// ---------------------------------------------------------------------------

func TestIsTransientDetectsWrappedTransient(t *testing.T) {
	inner := Transient(errors.New("timeout"))
	wrapped := fmt.Errorf("layer: %w", inner)

	if !IsTransient(wrapped) {
		t.Fatal("IsTransient on wrapped transient = false, want true")
	}
}

func TestIsPermanentDetectsWrappedPermanent(t *testing.T) {
	inner := Permanent(errors.New("bad input"))
	wrapped := fmt.Errorf("layer: %w", inner)

	if !IsPermanent(wrapped) {
		t.Fatal("IsPermanent on wrapped permanent = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

func TestSentinelErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrCircuitOpen, "circuit breaker is open"},
		{ErrRateLimited, "rate limited"},
		{ErrBulkheadFull, "bulkhead full"},
		{ErrTimeout, "timeout"},
		{ErrRetriesExhausted, "retries exhausted"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("%T.Error() = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestSentinelErrorsImplementResilienceError(t *testing.T) {
	sentinels := []error{
		ErrCircuitOpen,
		ErrRateLimited,
		ErrBulkheadFull,
		ErrTimeout,
		ErrRetriesExhausted,
	}
	for _, sentinel := range sentinels {
		var re ResilienceError
		if !errors.As(sentinel, &re) {
			t.Errorf("errors.As(%T, &ResilienceError) = false", sentinel)
			continue
		}
		if !re.IsResilience() {
			t.Errorf("%T.IsResilience() = false, want true", sentinel)
		}
	}
}

func TestSentinelErrorsDetectableViaErrorsIsWhenWrapped(t *testing.T) {
	sentinels := []error{
		ErrCircuitOpen,
		ErrRateLimited,
		ErrBulkheadFull,
		ErrTimeout,
		ErrRetriesExhausted,
	}
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("context: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is(wrapped, %T) = false, want true", sentinel)
		}
	}
}

func TestSentinelResilienceErrorDetectableWhenWrapped(t *testing.T) {
	sentinels := []error{
		ErrCircuitOpen,
		ErrRateLimited,
		ErrBulkheadFull,
		ErrTimeout,
		ErrRetriesExhausted,
	}
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("context: %w", sentinel)
		var re ResilienceError
		if !errors.As(wrapped, &re) {
			t.Errorf("errors.As(wrapped %T, &ResilienceError) = false", sentinel)
		}
	}
}
