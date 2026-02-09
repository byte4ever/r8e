# Example 06 — Bulkhead

Demonstrates concurrency limiting using the bulkhead (semaphore) pattern.

## What it demonstrates

A policy is configured with `WithBulkhead(3)`, limiting concurrent calls to 3.
The example launches 6 goroutines that all attempt to call `policy.Do`
simultaneously. Each call simulates 200ms of work.

- The first 3 goroutines acquire a bulkhead slot and complete their work.
- The remaining 3 goroutines are immediately rejected with `ErrBulkheadFull`
  because all slots are occupied.

A small stagger (10ms) between goroutine launches ensures that the first 3
calls are actively holding slots when the remaining 3 arrive.

## Key concepts

| Concept | Detail |
|---|---|
| `WithBulkhead(n)` | Limits concurrent executions to `n` using a semaphore |
| `ErrBulkheadFull` | Sentinel error returned when all bulkhead slots are occupied |
| Non-blocking | The bulkhead does not queue requests — excess calls are rejected immediately |
| Isolation | Prevents a slow dependency from consuming all goroutines or connections |

## When to use

- Protecting shared resources (database connection pools, external APIs) from
  being overwhelmed by concurrent requests.
- Isolating failure domains so a slow downstream doesn't cascade into resource
  exhaustion.
- Complementing rate limiting: rate limiting controls throughput over time,
  bulkhead controls concurrent access at any instant.

## Run

```bash
go run ./examples/06-bulkhead/
```

## Expected output

Three workers complete successfully; three are rejected with
`REJECTED (bulkhead full)`. The exact order depends on goroutine scheduling.
