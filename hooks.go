package r8e

// Hooks holds optional callback functions for resilience pattern lifecycle
// events. All fields are nil by default; callers set only the hooks they care
// about. A nil *Hooks is itself valid and behaves as a no-op, so every exported
// entry point may be called with a nil Hooks. Once constructed, a Hooks value
// must not be mutated — emit methods read the function fields without
// synchronisation, which is safe only because the struct is read-only after
// initialisation (there is no runtime subscription, unlike a true Observer; it
// is a plain optional-callback set).
type Hooks struct {
	OnRetry           func(attempt int, err error)
	OnCircuitOpen     func()
	OnCircuitClose    func()
	OnCircuitHalfOpen func()
	// OnCircuitRamping fires when the breaker enters the slow-start ramp state
	// after recovering through half-open: admission then grows from the initial
	// fraction to full over the ramp window (see [RampRecovery]) instead of
	// jumping straight to closed.
	OnCircuitRamping   func()
	OnRateLimited      func()
	OnBulkheadFull     func()
	OnBulkheadAcquired func()
	OnBulkheadReleased func()
	// OnBulkheadQueued fires when a full bulkhead enqueues a caller for the
	// bounded FIFO wait instead of rejecting it (see [BulkheadMaxWait]).
	OnBulkheadQueued func()
	// OnBulkheadTimeout fires when a queued caller gives up after waiting the full
	// max-wait without a slot, returning [ErrBulkheadTimeout].
	OnBulkheadTimeout func()
	// OnCoDelShed fires when the bulkhead's controlled-delay discipline sheds a
	// queued caller because the wait queue was overloaded and the caller had waited
	// past the slough timeout (see [BulkheadCoDel]), returning [ErrCoDelShed].
	OnCoDelShed      func()
	OnTimeout        func()
	OnHedgeTriggered func()
	OnHedgeWon       func()
	OnFallbackUsed   func(err error)

	// OnRetryBudgetExceeded fires when a retry is suppressed because the retry
	// budget is exhausted. The underlying downstream error is still returned by
	// the policy call.
	OnRetryBudgetExceeded func()

	// OnTimeBudgetExceeded fires when retry stops early because the total time
	// budget would be exhausted by the next backoff (see [WithTimeBudget]).
	OnTimeBudgetExceeded func()

	// OnCoalesceLeader fires when a call begins a shared execution for a
	// coalescing key (it ran the work the followers share).
	OnCoalesceLeader func()
	// OnCoalesceFollower fires when a call joins a shared execution already in
	// flight (its work was deduplicated away).
	OnCoalesceFollower func()

	// OnCacheHit fires when a call is served from the read-through cache without
	// executing the downstream work — a fresh value, or a cached error from a
	// negative entry.
	OnCacheHit func()
	// OnCacheMiss fires when the read-through cache has no fresh value and the
	// downstream work is executed (a hard miss or a stale-window revalidation).
	OnCacheMiss func()
	// OnCacheStored fires when a successful result is written to the read-through
	// cache.
	OnCacheStored func()
	// OnStaleServed fires when a downstream execution fails and the read-through
	// cache serves a stale value instead of the error (see [StaleIfError]).
	OnStaleServed func()
	// OnCacheRefreshed fires when a refresh-ahead background reload completes
	// successfully and repopulates the entry (see [RefreshAhead]). A failed reload
	// is silent. The successful store also fires OnCacheStored.
	OnCacheRefreshed func()

	// OnConcurrencyRejected fires when the adaptive concurrency limiter rejects a
	// call because in-flight is at its current limit.
	OnConcurrencyRejected func()
	// OnConcurrencyLimitChanged fires when the adaptive concurrency limiter
	// retunes its integer limit, with the new value.
	OnConcurrencyLimitChanged func(limit int)

	// OnThrottled fires when the adaptive throttler sheds a call locally instead
	// of forwarding it to a struggling backend (see [WithAdaptiveThrottle]).
	OnThrottled func()

	// OnSLOShed fires when the SLO burn-rate governor sheds a call locally to
	// preserve the error budget while it is burning too fast (see [WithSLO]).
	OnSLOShed func()

	// OnRateAdapted fires when the AIMD rate controller moves the rate limiter's
	// refill rate, with the new rate in tokens per second (see [AIMD]). A new
	// rate below the previous one signals server pushback (a multiplicative
	// backoff); a higher one is the additive recovery.
	OnRateAdapted func(rate float64)

	// OnSlowCallRateExceeded fires when the circuit breaker opens because the
	// slow-call rate reached its threshold (see [SlowCallRate]), as opposed to
	// the consecutive-failure trip. OnCircuitOpen also fires for the same
	// transition; this hook identifies the slow-call cause specifically.
	OnSlowCallRateExceeded func()

	// OnPanic fires when [WithRecover] catches a panic from the user function,
	// with the value that was passed to panic(). Use errors.As with *[PanicError]
	// to obtain the full context (value + stack trace) from the returned error.
	OnPanic func(value any)

	// OnConcurrencyBudgetExceeded fires when the concurrency budget is at its
	// ceiling and a retry is suppressed ([ErrConcurrencyBudgetExceeded] is
	// returned) or a hedge is not launched (see [WithConcurrencyBudget]).
	OnConcurrencyBudgetExceeded func()

	// OnChaosInjected fires when a chaos strategy injects on a call, with the
	// strategy kind ("fault", "latency", "outcome", or "behavior"). See
	// [WithChaos].
	OnChaosInjected func(kind string)
}

// Each emit method guards both a nil receiver and a nil field, so a nil *Hooks
// (or any unset callback) is a no-op rather than a panic.

func (h *Hooks) emitRetry(attempt int, err error) {
	if h != nil && h.OnRetry != nil {
		h.OnRetry(attempt, err)
	}
}

func (h *Hooks) emitCircuitOpen() {
	if h != nil && h.OnCircuitOpen != nil {
		h.OnCircuitOpen()
	}
}

func (h *Hooks) emitCircuitClose() {
	if h != nil && h.OnCircuitClose != nil {
		h.OnCircuitClose()
	}
}

func (h *Hooks) emitCircuitRamping() {
	if h != nil && h.OnCircuitRamping != nil {
		h.OnCircuitRamping()
	}
}

func (h *Hooks) emitCircuitHalfOpen() {
	if h != nil && h.OnCircuitHalfOpen != nil {
		h.OnCircuitHalfOpen()
	}
}

func (h *Hooks) emitRateLimited() {
	if h != nil && h.OnRateLimited != nil {
		h.OnRateLimited()
	}
}

func (h *Hooks) emitBulkheadFull() {
	if h != nil && h.OnBulkheadFull != nil {
		h.OnBulkheadFull()
	}
}

func (h *Hooks) emitBulkheadAcquired() {
	if h != nil && h.OnBulkheadAcquired != nil {
		h.OnBulkheadAcquired()
	}
}

func (h *Hooks) emitBulkheadReleased() {
	if h != nil && h.OnBulkheadReleased != nil {
		h.OnBulkheadReleased()
	}
}

func (h *Hooks) emitBulkheadQueued() {
	if h != nil && h.OnBulkheadQueued != nil {
		h.OnBulkheadQueued()
	}
}

func (h *Hooks) emitBulkheadTimeout() {
	if h != nil && h.OnBulkheadTimeout != nil {
		h.OnBulkheadTimeout()
	}
}

func (h *Hooks) emitCoDelShed() {
	if h != nil && h.OnCoDelShed != nil {
		h.OnCoDelShed()
	}
}

func (h *Hooks) emitTimeout() {
	if h != nil && h.OnTimeout != nil {
		h.OnTimeout()
	}
}

func (h *Hooks) emitHedgeTriggered() {
	if h != nil && h.OnHedgeTriggered != nil {
		h.OnHedgeTriggered()
	}
}

func (h *Hooks) emitHedgeWon() {
	if h != nil && h.OnHedgeWon != nil {
		h.OnHedgeWon()
	}
}

func (h *Hooks) emitFallbackUsed(err error) {
	if h != nil && h.OnFallbackUsed != nil {
		h.OnFallbackUsed(err)
	}
}

func (h *Hooks) emitRetryBudgetExceeded() {
	if h != nil && h.OnRetryBudgetExceeded != nil {
		h.OnRetryBudgetExceeded()
	}
}

func (h *Hooks) emitTimeBudgetExceeded() {
	if h != nil && h.OnTimeBudgetExceeded != nil {
		h.OnTimeBudgetExceeded()
	}
}

func (h *Hooks) emitCoalesceLeader() {
	if h != nil && h.OnCoalesceLeader != nil {
		h.OnCoalesceLeader()
	}
}

func (h *Hooks) emitCoalesceFollower() {
	if h != nil && h.OnCoalesceFollower != nil {
		h.OnCoalesceFollower()
	}
}

func (h *Hooks) emitCacheHit() {
	if h != nil && h.OnCacheHit != nil {
		h.OnCacheHit()
	}
}

func (h *Hooks) emitCacheMiss() {
	if h != nil && h.OnCacheMiss != nil {
		h.OnCacheMiss()
	}
}

func (h *Hooks) emitCacheStored() {
	if h != nil && h.OnCacheStored != nil {
		h.OnCacheStored()
	}
}

func (h *Hooks) emitStaleServed() {
	if h != nil && h.OnStaleServed != nil {
		h.OnStaleServed()
	}
}

func (h *Hooks) emitCacheRefreshed() {
	if h != nil && h.OnCacheRefreshed != nil {
		h.OnCacheRefreshed()
	}
}

func (h *Hooks) emitConcurrencyRejected() {
	if h != nil && h.OnConcurrencyRejected != nil {
		h.OnConcurrencyRejected()
	}
}

func (h *Hooks) emitConcurrencyLimitChanged(limit int) {
	if h != nil && h.OnConcurrencyLimitChanged != nil {
		h.OnConcurrencyLimitChanged(limit)
	}
}

func (h *Hooks) emitThrottled() {
	if h != nil && h.OnThrottled != nil {
		h.OnThrottled()
	}
}

func (h *Hooks) emitSLOShed() {
	if h != nil && h.OnSLOShed != nil {
		h.OnSLOShed()
	}
}

func (h *Hooks) emitRateAdapted(rate float64) {
	if h != nil && h.OnRateAdapted != nil {
		h.OnRateAdapted(rate)
	}
}

func (h *Hooks) emitSlowCallRateExceeded() {
	if h != nil && h.OnSlowCallRateExceeded != nil {
		h.OnSlowCallRateExceeded()
	}
}

func (h *Hooks) emitPanic(value any) {
	if h != nil && h.OnPanic != nil {
		h.OnPanic(value)
	}
}

func (h *Hooks) emitConcurrencyBudgetExceeded() {
	if h != nil && h.OnConcurrencyBudgetExceeded != nil {
		h.OnConcurrencyBudgetExceeded()
	}
}

func (h *Hooks) emitChaosInjected(kind string) {
	if h != nil && h.OnChaosInjected != nil {
		h.OnChaosInjected(kind)
	}
}
