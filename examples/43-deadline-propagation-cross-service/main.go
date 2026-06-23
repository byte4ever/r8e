// Example 43-deadline-propagation-cross-service: Demonstrates cross-service
// deadline propagation — the ingress half that completes example 28's egress.
//
// Example 28 showed a gateway turning its time budget into a real ctx.Deadline()
// with r8e.PropagateDeadline(). This example carries that deadline ACROSS a
// service boundary and has the receiving service honor it:
//
//   - Egress (the gateway): httpx.InjectDeadline stamps the remaining budget onto
//     an outgoing request as a relative millisecond header (clock-skew-safe,
//     mirroring gRPC's grpc-timeout).
//   - Ingress (the service): httpx.ExtractDeadline reconstructs a local deadline
//     from that header, and r8e.WithTimeBudget(..., r8e.RespectInboundDeadline())
//     tightens the service's own budget down to it ("the smallest deadline
//     wins").
//
// The payoff: a deadline-aware service stops retrying a doomed dependency the
// moment the upstream budget runs out, instead of burning its own generous local
// budget on work whose result the caller has already given up waiting for.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/httpx"
)

// serveRetrying retries a permanently-failing downstream dependency every 50ms
// under policy, counting attempts, and writes the outcome — a 504 once the
// policy gives up. The ctx governs the work: when it carries a deadline (and the
// policy honors it) the retries stop early.
func serveRetrying(
	ctx context.Context,
	writer http.ResponseWriter,
	policy *r8e.Policy[string],
	attempts *atomic.Int64,
) {
	attempts.Store(0)

	_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempts.Add(1)

		return "", r8e.Transient(errors.New("dependency down"))
	})
	if err != nil {
		http.Error(writer, err.Error(), http.StatusGatewayTimeout)

		return
	}

	fmt.Fprintln(writer, "ok")
}

// newNaiveService builds a service that never reads the inbound deadline header:
// it retries under its own generous 10s budget, oblivious that the gateway has
// already given up.
func newNaiveService() (*httptest.Server, *atomic.Int64) {
	var attempts atomic.Int64

	handler := func(writer http.ResponseWriter, req *http.Request) {
		policy := r8e.NewPolicy[string]("naive",
			r8e.WithRetry(20, r8e.ConstantBackoff(50*time.Millisecond)),
			r8e.WithTimeBudget(10*time.Second),
		)

		serveRetrying(req.Context(), writer, policy, &attempts)
	}

	return httptest.NewServer(http.HandlerFunc(handler)), &attempts
}

// newAwareService builds a deadline-aware service: httpx.ExtractDeadline adopts
// the caller's propagated deadline (ingress) and r8e.RespectInboundDeadline
// tightens the generous 10s local budget down to it, so the retries stop the
// moment the UPSTREAM budget runs out.
func newAwareService() (*httptest.Server, *atomic.Int64) {
	var attempts atomic.Int64

	handler := func(writer http.ResponseWriter, req *http.Request) {
		ctx, cancel := httpx.ExtractDeadline(req.Context(), req)
		defer cancel()

		policy := r8e.NewPolicy[string]("aware",
			r8e.WithRetry(20, r8e.ConstantBackoff(50*time.Millisecond)),
			r8e.WithTimeBudget(10*time.Second, r8e.RespectInboundDeadline()),
		)

		serveRetrying(ctx, writer, policy, &attempts)
	}

	return httptest.NewServer(http.HandlerFunc(handler)), &attempts
}

// callWithBudget plays the gateway: it stamps its remaining budget onto the wire
// and calls the service, then reports how long the service actually ran and how
// many downstream attempts it made. The HTTP call itself runs on a non-cancelling
// context so we can OBSERVE the service's own deadline handling; a production
// gateway would also cancel at its own deadline.
func callWithBudget(label, url string, budget time.Duration, attempts *atomic.Int64) {
	// A request whose context carries the gateway's budget, so InjectDeadline can
	// read it and write the relative wire header.
	budgetCtx, cancel := context.WithDeadline(
		context.Background(), time.Now().Add(budget),
	)
	defer cancel()

	req, err := http.NewRequestWithContext(
		budgetCtx, http.MethodGet, url, http.NoBody,
	)
	if err != nil {
		fmt.Printf("    request build failed: %v\n", err)

		return
	}

	httpx.InjectDeadline(req, r8e.RealClock{})

	// Send on a fresh, non-cancelling context: the header still travels, but the
	// client no longer cuts the call off, so the service's own handling is visible.
	req = req.WithContext(context.Background())

	start := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("    call failed: %v\n", err)

		return
	}

	_ = resp.Body.Close()

	fmt.Printf("    %-16s ran %5v, %2d downstream attempts (gateway budget: %v)\n",
		label, time.Since(start).Round(10*time.Millisecond), attempts.Load(), budget)
}

func main() {
	const gatewayBudget = 300 * time.Millisecond

	naive, naiveAttempts := newNaiveService()
	defer naive.Close()

	aware, awareAttempts := newAwareService()
	defer aware.Close()

	fmt.Printf("Gateway propagates a %v budget on the wire (header %q).\n\n",
		gatewayBudget, httpx.DeadlineHeader)

	fmt.Println("Naive service — ignores the inbound deadline:")
	callWithBudget("naive", naive.URL, gatewayBudget, naiveAttempts)

	fmt.Println("\nDeadline-aware service — honors it via RespectInboundDeadline:")
	callWithBudget("deadline-aware", aware.URL, gatewayBudget, awareAttempts)

	fmt.Println("\nThe aware service stops as soon as the gateway's budget runs out;")
	fmt.Println("the naive one keeps retrying long after the caller has given up.")
}
