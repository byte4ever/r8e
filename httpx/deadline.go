package httpx

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/byte4ever/r8e"
)

const (
	// DeadlineHeader is the HTTP header that carries a request's remaining time
	// budget across a service boundary. Its value is a relative timeout in
	// integer milliseconds, not an absolute timestamp: a relative value is
	// immune to clock skew between the caller and the callee, mirroring gRPC's
	// grpc-timeout convention. The caller writes it with [InjectDeadline]
	// (egress) and the callee reconstructs a local deadline from it with
	// [ExtractDeadline] (ingress), so a deadline set at the edge tightens every
	// hop in the chain.
	DeadlineHeader = "X-R8e-Timeout-Ms"

	// maxTimeoutMillis is the largest millisecond budget that still fits in a
	// [time.Duration] (~292 years). [ExtractDeadline] clamps to it so a hostile
	// or corrupt header cannot overflow the duration multiplication into a
	// negative value (which a context would treat as an instant, already-expired
	// deadline).
	maxTimeoutMillis = math.MaxInt64 / int64(time.Millisecond)
)

// InjectDeadline encodes the deadline of req's context onto req as a relative
// millisecond budget header (egress). It is the outbound half of cross-service
// deadline propagation: pair it with [r8e.PropagateDeadline] so a policy's time
// budget becomes a real context deadline, then call InjectDeadline on the
// outgoing request so a downstream service receives the remaining time.
//
// remaining is measured against clock (pass [r8e.RealClock] in production); a
// spent or sub-millisecond budget is floored to 1ms so it still propagates as
// "almost no time left" rather than vanishing. It reports whether a header was
// written: false means req's context carries no deadline, so there is nothing
// to propagate.
func InjectDeadline(req *http.Request, clock r8e.Clock) bool {
	deadline, ok := req.Context().Deadline()
	if !ok {
		return false
	}

	ms := deadline.Sub(clock.Now()).Milliseconds()
	if ms < 1 {
		ms = 1
	}

	req.Header.Set(DeadlineHeader, strconv.FormatInt(ms, 10))

	return true
}

// ExtractDeadline reads the relative millisecond budget header from req and
// returns a child of parent bounded by it (ingress), reconstructing the
// deadline locally as now+remaining so it is immune to clock skew. It is the
// inbound half of cross-service deadline propagation: a server reads the
// upstream budget and runs its own work — and any [r8e.WithTimeBudget] with
// [r8e.RespectInboundDeadline] layered on the returned context — under it.
//
// With no header, or an absent, non-numeric, or non-positive value, it returns
// parent unchanged with a no-op cancel. The caller must always invoke the
// returned cancel to release the timer.
func ExtractDeadline(
	parent context.Context,
	req *http.Request,
) (context.Context, context.CancelFunc) {
	value := req.Header.Get(DeadlineHeader)
	if value == "" {
		return parent, func() {}
	}

	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ms < 1 {
		return parent, func() {}
	}

	if ms > maxTimeoutMillis {
		ms = maxTimeoutMillis
	}

	return context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
}
