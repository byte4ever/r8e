# httpx — Resilient HTTP Client Adapter

## Problem

Using r8e with HTTP calls requires boilerplate: wrapping `http.Client.Do` into a
`func(context.Context) (T, error)`, classifying HTTP status codes as transient or
permanent errors manually, and feeding everything to `policy.Do`. This is
repetitive and error-prone.

## Solution

A new subpackage `r8e/httpx` that provides a thin, reusable `Client` struct
combining an `*http.Client`, an `r8e.Policy[*http.Response]`, and a user-provided
status code classifier.

## Design Decisions

- **Subpackage `r8e/httpx`** — keeps `net/http` out of the core package,
  preserving r8e's zero-dependency guarantee.
- **Reusable struct, not a one-shot function** — configure once (policy +
  classifier), call `Do` repeatedly.
- **Returns `*http.Response`** — no body reading or decoding; the caller stays in
  full control.
- **Returns both response and error** — mirrors `http.Client.Do` semantics. On
  transient/permanent status codes, the response is accessible via the error.
- **User-provided classifier, no defaults** — the user must be explicit about
  what's transient for their API. No hidden assumptions.
- **No convenience methods** (`Get`, `Post`, etc.) — the caller builds their own
  `*http.Request`.

## Types

```go
package httpx

import (
    "context"
    "fmt"
    "net/http"

    "github.com/byte4ever/r8e"
)

// ErrorClass tells the resilience layer how to treat an HTTP status code.
type ErrorClass int

const (
    Success   ErrorClass = iota // 2xx-like: no error
    Transient                   // retriable (e.g. 429, 502, 503)
    Permanent                   // non-retriable (e.g. 400, 401, 404)
)

// Classifier maps an HTTP status code to an ErrorClass.
type Classifier func(statusCode int) ErrorClass

// StatusError is returned when the classifier marks a status code
// as Transient or Permanent. The original response is still accessible.
type StatusError struct {
    Response   *http.Response
    StatusCode int
}

func (e *StatusError) Error() string {
    return fmt.Sprintf("http status %d", e.StatusCode)
}

// Client wraps an http.Client with an r8e resilience policy
// and HTTP status code classification.
type Client struct {
    http       *http.Client
    policy     *r8e.Policy[*http.Response]
    classifier Classifier
}
```

## Constructor

```go
func NewClient(
    name string,
    httpClient *http.Client,
    classifier Classifier,
    opts ...any,
) *Client {
    return &Client{
        http:       httpClient,
        policy:     r8e.NewPolicy[*http.Response](name, opts...),
        classifier: classifier,
    }
}
```

## Do Method

```go
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
    return c.policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
        resp, err := c.http.Do(req.WithContext(ctx))
        if err != nil {
            return nil, err
        }

        switch c.classifier(resp.StatusCode) {
        case Transient:
            return resp, r8e.Transient(&StatusError{
                Response:   resp,
                StatusCode: resp.StatusCode,
            })
        case Permanent:
            return resp, r8e.Permanent(&StatusError{
                Response:   resp,
                StatusCode: resp.StatusCode,
            })
        default:
            return resp, nil
        }
    })
}
```

### Behavior

- **Transport errors** (DNS, TLS, connection refused) pass through as-is.
  Unclassified errors are transient by default in r8e's retry logic.
- **Transient status codes** (e.g. 429, 503) trigger retry via
  `r8e.Transient()`.
- **Permanent status codes** (e.g. 400, 404) stop retry immediately via
  `r8e.Permanent()`.
- **Response on error paths** — the caller can use
  `errors.As(err, &statusErr)` to access `statusErr.Response` for headers, body,
  etc.
- **Body ownership** — the helper never reads or closes the body. The caller owns
  it entirely.

## Usage Example

```go
classifier := func(code int) httpx.ErrorClass {
    switch {
    case code >= 200 && code < 300:
        return httpx.Success
    case code == 429, code == 502, code == 503, code == 504:
        return httpx.Transient
    default:
        return httpx.Permanent
    }
}

client := httpx.NewClient("payment-api",
    http.DefaultClient,
    classifier,
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
)

resp, err := client.Do(ctx, req)
if err != nil {
    var statusErr *httpx.StatusError
    if errors.As(err, &statusErr) {
        // can inspect statusErr.Response.Header, read body, etc.
    }
    return err
}
defer resp.Body.Close()
```

## Package Scope

The full public API of `r8e/httpx`:

| Export       | Kind   | Description                                  |
|--------------|--------|----------------------------------------------|
| `Client`     | struct | Wraps http.Client + policy + classifier      |
| `NewClient`  | func   | Constructor                                  |
| `Client.Do`  | method | Execute request through resilience policy    |
| `Classifier` | type   | `func(int) ErrorClass`                       |
| `ErrorClass` | type   | Enum: `Success`, `Transient`, `Permanent`    |
| `StatusError`| struct | Error carrying the original `*http.Response` |

No default classifier. No convenience methods. No additional dependencies beyond
`r8e` and `net/http`.
