# httpx Adapter Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> to implement this plan task-by-task.

**Goal:** Add an `r8e/httpx` subpackage that wraps `http.Client` with
r8e resilience policies and user-provided HTTP status code
classification.

**Architecture:** Thin adapter — a `Client` struct holds an
`*http.Client`, an `r8e.Policy[*http.Response]`, and a `Classifier`
function. The `Do` method delegates to the policy, which wraps the
HTTP call and classifies status codes as `Success`, `Transient`, or
`Permanent` using r8e's existing error classification.

**Tech Stack:** Go stdlib `net/http`, `net/http/httptest`;
`github.com/byte4ever/r8e`; `github.com/stretchr/testify`.

**Skills:** `golang` (coding standards, lint, test workflow),
`go-design-patterns` (Adapter pattern annotation, Strategy for
classifier).

**Worktree:** `~/.config/superpowers/worktrees/r8e/feature-httpx`
(branch `feature/httpx`).

**Design doc:** `docs/plans/2026-02-09-httpx-adapter-design.md`

---

### Task 1: Create the httpx package with types and doc.go

**Files:**
- Create: `httpx/doc.go`
- Create: `httpx/httpx.go`

**Step 1: Create `httpx/doc.go`**

```go
// Package httpx provides a resilient HTTP client adapter
// for the r8e library.
//
// Client wraps a standard http.Client with an r8e resilience
// policy and a user-provided status code classifier that maps
// HTTP response codes to transient or permanent errors.
package httpx
```

**Step 2: Create `httpx/httpx.go` with types only (no methods yet)**

```go
package httpx

import (
	"net/http"
	"strconv"

	"github.com/byte4ever/r8e"
)

// ErrorClass tells the resilience layer how to treat an HTTP
// status code.
type ErrorClass int

const (
	// Success means the request succeeded (e.g. 2xx).
	Success ErrorClass = iota
	// Transient means the error is retriable (e.g. 429, 503).
	Transient
	// Permanent means the error is non-retriable (e.g. 400).
	Permanent
)

// Classifier maps an HTTP status code to an ErrorClass.
//
// Pattern: Strategy — caller injects classification logic
// without modifying the adapter.
type Classifier func(statusCode int) ErrorClass

// StatusError is returned when the Classifier marks a status
// code as Transient or Permanent. The original response
// remains accessible for header/body inspection.
type StatusError struct {
	// Response is the original HTTP response that triggered
	// the error. The body has not been read or closed.
	Response   *http.Response
	StatusCode int
}

// Error returns a human-readable description of the status
// error.
func (e *StatusError) Error() string {
	return "http status " + strconv.Itoa(e.StatusCode)
}

// Client wraps an http.Client with an r8e resilience policy
// and HTTP status code classification.
//
// Pattern: Adapter — bridges net/http and r8e's resilience
// policy by translating HTTP status codes into r8e error
// classification.
type Client struct {
	hc *http.Client
	p  *r8e.Policy[*http.Response]
	cl Classifier
}
```

**Step 3: Verify it compiles**

Run: `cd ~/.config/superpowers/worktrees/r8e/feature-httpx && go build ./httpx/...`
Expected: no errors.

**Step 4: Commit**

```bash
git add httpx/doc.go httpx/httpx.go
git commit -m "feat(httpx): add package with types and error classification"
```

---

### Task 2: Write failing tests for NewClient

**Files:**
- Create: `httpx/httpx_test.go`

**Step 1: Write the failing test**

```go
package httpx_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e/httpx"
)

// successClassifier classifies all codes as Success.
func successClassifier(_ int) httpx.ErrorClass {
	return httpx.Success
}

func TestNewClientReturnsNonNil(t *testing.T) {
	t.Parallel()

	cl := httpx.NewClient(
		"test",
		http.DefaultClient,
		successClassifier,
	)

	require.NotNil(t, cl)
}

func TestNewClientWithEmptyName(t *testing.T) {
	t.Parallel()

	cl := httpx.NewClient(
		"",
		http.DefaultClient,
		successClassifier,
	)

	require.NotNil(t, cl)
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/.config/superpowers/worktrees/r8e/feature-httpx && go test ./httpx/...`
Expected: FAIL — `NewClient` not defined.

**Step 3: Commit the failing test**

```bash
git add httpx/httpx_test.go
git commit -m "test(httpx): add failing tests for NewClient"
```

---

### Task 3: Implement NewClient

**Files:**
- Modify: `httpx/httpx.go`

**Step 1: Add the constructor to `httpx/httpx.go`**

Append after the `Client` struct:

```go
// NewClient creates a Client that executes HTTP requests
// through the given r8e policy options. The classifier
// determines how HTTP status codes map to transient or
// permanent errors for retry decisions.
func NewClient(
	name string,
	hc *http.Client,
	cl Classifier,
	opts ...any,
) *Client {
	return &Client{
		hc: hc,
		p:  r8e.NewPolicy[*http.Response](name, opts...),
		cl: cl,
	}
}
```

**Step 2: Run tests to verify they pass**

Run: `cd ~/.config/superpowers/worktrees/r8e/feature-httpx && go test ./httpx/...`
Expected: PASS.

**Step 3: Commit**

```bash
git add httpx/httpx.go
git commit -m "feat(httpx): implement NewClient constructor"
```

---

### Task 4: Write failing tests for Client.Do

**Files:**
- Modify: `httpx/httpx_test.go`

**Step 1: Write failing tests covering all classification paths**

Append to `httpx/httpx_test.go`:

```go
func testClassifier(code int) httpx.ErrorClass {
	switch {
	case code >= 200 && code < 300:
		return httpx.Success
	case code == 429, code == 502, code == 503, code == 504:
		return httpx.Transient
	default:
		return httpx.Permanent
	}
}

func TestDoSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
		),
	)

	defer srv.Close()

	cl := httpx.NewClient(
		"test-ok",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := cl.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
}

func TestDoPermanentError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			},
		),
	)

	defer srv.Close()

	cl := httpx.NewClient(
		"test-perm",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, doErr := cl.Do(context.Background(), req)

	// Both response and error should be non-nil.
	require.Error(t, doErr)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Error should be permanent.
	assert.True(t, r8e.IsPermanent(doErr))

	// StatusError should be extractable.
	var statusErr *httpx.StatusError
	require.ErrorAs(t, doErr, &statusErr)
	assert.Equal(
		t,
		http.StatusBadRequest,
		statusErr.StatusCode,
	)
	assert.Equal(t, resp, statusErr.Response)

	defer resp.Body.Close()
}

func TestDoTransientNoRetry(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		),
	)

	defer srv.Close()

	// No retry configured — transient error returned directly.
	cl := httpx.NewClient(
		"test-transient",
		srv.Client(),
		testClassifier,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, doErr := cl.Do(context.Background(), req)
	require.Error(t, doErr)
	require.NotNil(t, resp)

	assert.True(t, r8e.IsTransient(doErr))

	var statusErr *httpx.StatusError
	require.ErrorAs(t, doErr, &statusErr)
	assert.Equal(
		t,
		http.StatusServiceUnavailable,
		statusErr.StatusCode,
	)

	defer resp.Body.Close()
}

func TestDoTransientWithRetryRecovers(t *testing.T) {
	t.Parallel()

	var attempt int

	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				attempt++

				if attempt < 3 {
					w.WriteHeader(
						http.StatusServiceUnavailable,
					)
					return
				}

				w.WriteHeader(http.StatusOK)
			},
		),
	)

	defer srv.Close()

	cl := httpx.NewClient(
		"test-retry",
		srv.Client(),
		testClassifier,
		r8e.WithRetry(
			5,
			r8e.ConstantBackoff(time.Millisecond),
		),
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		srv.URL,
		nil,
	)
	require.NoError(t, err)

	resp, doErr := cl.Do(context.Background(), req)
	require.NoError(t, doErr)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, attempt)

	defer resp.Body.Close()
}

func TestDoTransportError(t *testing.T) {
	t.Parallel()

	cl := httpx.NewClient(
		"test-transport",
		http.DefaultClient,
		testClassifier,
	)

	// Request to a closed server — transport-level error.
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://127.0.0.1:1",
		nil,
	)
	require.NoError(t, err)

	resp, doErr := cl.Do(context.Background(), req)
	require.Error(t, doErr)
	assert.Nil(t, resp)

	// Should NOT be a StatusError.
	var statusErr *httpx.StatusError
	assert.False(t, errors.As(doErr, &statusErr))
}

func TestStatusErrorMessage(t *testing.T) {
	t.Parallel()

	se := &httpx.StatusError{StatusCode: 503}
	assert.Equal(t, "http status 503", se.Error())
}
```

Add the necessary imports at the top of the test file:
`"context"`, `"errors"`, `"net/http/httptest"`, `"time"`,
`"github.com/byte4ever/r8e"`.

**Step 2: Run tests to verify they fail**

Run: `cd ~/.config/superpowers/worktrees/r8e/feature-httpx && go test ./httpx/...`
Expected: FAIL — `Do` method not defined.

**Step 3: Commit the failing tests**

```bash
git add httpx/httpx_test.go
git commit -m "test(httpx): add failing tests for Client.Do"
```

---

### Task 5: Implement Client.Do

**Files:**
- Modify: `httpx/httpx.go`

**Step 1: Add the Do method to `httpx/httpx.go`**

Append after `NewClient`:

```go
// Do executes the HTTP request through the resilience policy.
// Like http.Client.Do, it may return both a non-nil response
// and a non-nil error. When the Classifier returns Transient
// or Permanent, the response is wrapped in a StatusError
// accessible via errors.As.
func (c *Client) Do(
	ctx context.Context,
	req *http.Request,
) (*http.Response, error) {
	//nolint:wrapcheck // policy returns caller's error as-is
	return c.p.Do(
		ctx,
		func(ctx context.Context) (*http.Response, error) {
			resp, err := c.hc.Do(
				req.WithContext(ctx),
			)
			if err != nil {
				return nil, err
			}

			switch c.cl(resp.StatusCode) {
			case Transient:
				return resp, r8e.Transient(
					&StatusError{
						Response:   resp,
						StatusCode: resp.StatusCode,
					},
				)
			case Permanent:
				return resp, r8e.Permanent(
					&StatusError{
						Response:   resp,
						StatusCode: resp.StatusCode,
					},
				)
			default:
				return resp, nil
			}
		},
	)
}
```

Add `"context"` to the import block.

**Step 2: Run tests to verify they pass**

Run: `cd ~/.config/superpowers/worktrees/r8e/feature-httpx && go test ./httpx/... -v`
Expected: all PASS.

**Step 3: Commit**

```bash
git add httpx/httpx.go
git commit -m "feat(httpx): implement Client.Do with status classification"
```

---

### Task 6: Lint, format, and fix

**Files:**
- Modify: `httpx/httpx.go`, `httpx/httpx_test.go` (as needed)

**Step 1: Run betteralign**

```bash
cd ~/.config/superpowers/worktrees/r8e/feature-httpx
betteralign -apply ./httpx/...
```

**Step 2: Run golines**

```bash
golines --shorten-comments --chain-split-dots \
  --max-len=80 --base-formatter=gofumpt -w ./httpx/
```

**Step 3: Run golangci-lint with autofix**

```bash
golangci-lint run --fix ./httpx/...
```

**Step 4: Run golangci-lint to check remaining issues**

```bash
golangci-lint run ./httpx/...
```

Fix any remaining lint issues manually.

**Step 5: Run tests again**

```bash
go test ./httpx/... -v -race
```

Expected: all PASS, no race conditions.

**Step 6: Run full project tests**

```bash
go test ./... -race
```

Expected: all PASS (no regressions).

**Step 7: Commit fixes**

```bash
git add -A
git commit -m "style(httpx): apply lint and format fixes"
```

---

### Task 7: Finalize

**Step 1: Review all commits on the branch**

```bash
git log --oneline main..HEAD
```

Verify commit history is clean.

**Step 2: Use `superpowers:finishing-a-development-branch`**

Follow the skill to decide: merge, PR, or cleanup.
