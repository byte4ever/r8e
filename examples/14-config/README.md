*[Lire en Francais](README.fr.md)*

# Example 14 — JSON Configuration

Demonstrates loading policy configuration from a JSON file and retrieving
typed policies at runtime with `GetPolicy`.

## What it demonstrates

### Loading configuration

`LoadConfig("config.json")` reads and validates a JSON configuration file.
All policies are validated eagerly at load time — invalid durations, unknown
backoff strategies, or malformed JSON produce immediate errors.

The included `config.json` defines two policies:

- **payment-api** — timeout, circuit breaker, retry (exponential backoff),
  rate limiter, and bulkhead
- **notification-api** — timeout and retry (constant backoff)

### Retrieving typed policies

`GetPolicy[string](reg, "payment-api", ...)` retrieves the named
configuration and builds a `Policy[string]`. Additional code-level options
(like `WithFallback`) can augment the config-loaded settings. Code-level
options are applied after config options, so they take precedence.

### Unknown policy names

If `GetPolicy` is called with a name that doesn't exist in the config, it
creates a bare policy with only the options provided in code. This allows
gradual migration from code-only to config-driven policies.

## Configuration format

```json
{
  "policies": {
    "policy-name": {
      "timeout": "2s",
      "circuit_breaker": {
        "failure_threshold": 5,
        "recovery_timeout": "30s"
      },
      "retry": {
        "max_attempts": 3,
        "backoff": "exponential",
        "base_delay": "100ms",
        "max_delay": "5s"
      },
      "rate_limit": 100,
      "bulkhead": 10
    }
  }
}
```

Supported backoff strategies: `"constant"`, `"exponential"`, `"linear"`,
`"exponential_jitter"`.

## Key concepts

| Concept | Detail |
|---|---|
| `LoadConfig(path)` | Reads and validates a JSON config file, returns a `*Registry` |
| `GetPolicy[T](reg, name, opts...)` | Retrieves a typed policy by name with optional overrides |
| Eager validation | All policies are validated at load time |
| Option precedence | Code-level options override config options |

## Run

```bash
go run ./examples/14-config/
```
