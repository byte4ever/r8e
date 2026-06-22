*[Lire en Français](README.fr.md)*

# Example 22 — Time Budget

Demonstrates `WithTimeBudget`, one **total** time budget shared across the whole
retry chain that stops retrying early once continuing could no longer finish in
time.

## What it demonstrates

A per-attempt timeout bounds each attempt in isolation, so five retries can still
sum to five times the timeout. A time budget instead caps the **sum** of all that
work. The example runs the same always-failing operation twice:

1. **Without a budget** — `WithRetry(5, ExponentialBackoff(100ms))` runs the full
   chain, sleeping 100, 200, 400, 800ms between attempts (~1.5s total) before
   giving up.
2. **With a 350ms budget** — after the 100ms and 200ms backoffs (~300ms spent),
   the next 400ms sleep would overrun the remaining budget. Retry stops early and
   returns `ErrTimeBudgetExceeded` (wrapping the real downstream error) instead of
   waiting out a doomed attempt.

Observe the attempt count and elapsed time printed for each: the budgeted run does
fewer attempts and returns far sooner.

## Key concepts

| Concept | Detail |
|---|---|
| `WithTimeBudget(d)` | Sets one total budget across the call, shared by retry and hedge |
| Early stop | Before each retry, if the backoff alone would overrun the remaining budget, retry stops with `ErrTimeBudgetExceeded` rather than sleeping |
| Tighter than per-attempt timeout | A per-attempt timeout bounds each attempt; the budget bounds their sum |
| Cooperative | The budget gates whether more work *starts*; it does not cancel an in-flight attempt (pair with `WithTimeout` for a hard deadline) |
| Requires a consumer | Only retry and hedge consume the budget, so it requires `WithRetry` or `WithHedge` — neither configured panics at `NewPolicy` |

## When to use

- Calls with a real upstream deadline (an inbound request SLA) where the total
  time across retries must be bounded, not just each attempt.
- Layering on top of exponential backoff, where the later sleeps dominate and you
  would rather fail fast than wait one out.
- Pair with `WithTimeout` when you also need to bound a single stuck attempt — the
  budget alone does not cancel in-flight work.

## Run

```bash
go run ./examples/22-time-budget/
```

## Expected output

Two runs of the same failing operation. The unbudgeted run completes all attempts
over ~1.5s; the 350ms-budgeted run does fewer attempts and returns around 300ms
with `ErrTimeBudgetExceeded`. Exact timings vary slightly with scheduling.
