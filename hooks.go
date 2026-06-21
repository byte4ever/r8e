package r8e

// Hooks holds optional callback functions for resilience pattern lifecycle
// events. All fields are nil by default; callers set only the hooks they care
// about. Once constructed, a Hooks value must not be mutated — emit methods
// read the function fields without synchronisation, which is safe only because
// the struct is read-only after initialisation (there is no runtime
// subscription, unlike a true Observer; it is a plain optional-callback set).
type Hooks struct {
	OnRetry            func(attempt int, err error)
	OnCircuitOpen      func()
	OnCircuitClose     func()
	OnCircuitHalfOpen  func()
	OnRateLimited      func()
	OnBulkheadFull     func()
	OnBulkheadAcquired func()
	OnBulkheadReleased func()
	OnTimeout          func()
	OnHedgeTriggered   func()
	OnHedgeWon         func()
	OnFallbackUsed     func(err error)

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

	// OnConcurrencyRejected fires when the adaptive concurrency limiter rejects a
	// call because in-flight is at its current limit.
	OnConcurrencyRejected func()
	// OnConcurrencyLimitChanged fires when the adaptive concurrency limiter
	// retunes its integer limit, with the new value.
	OnConcurrencyLimitChanged func(limit int)

	// OnThrottled fires when the adaptive throttler sheds a call locally instead
	// of forwarding it to a struggling backend (see [WithAdaptiveThrottle]).
	OnThrottled func()

	// OnSlowCallRateExceeded fires when the circuit breaker opens because the
	// slow-call rate reached its threshold (see [SlowCallRate]), as opposed to
	// the consecutive-failure trip. OnCircuitOpen also fires for the same
	// transition; this hook identifies the slow-call cause specifically.
	OnSlowCallRateExceeded func()
}

func (h *Hooks) emitRetry(attempt int, err error) {
	if h.OnRetry != nil {
		h.OnRetry(attempt, err)
	}
}

func (h *Hooks) emitCircuitOpen() {
	if h.OnCircuitOpen != nil {
		h.OnCircuitOpen()
	}
}

func (h *Hooks) emitCircuitClose() {
	if h.OnCircuitClose != nil {
		h.OnCircuitClose()
	}
}

func (h *Hooks) emitCircuitHalfOpen() {
	if h.OnCircuitHalfOpen != nil {
		h.OnCircuitHalfOpen()
	}
}

func (h *Hooks) emitRateLimited() {
	if h.OnRateLimited != nil {
		h.OnRateLimited()
	}
}

func (h *Hooks) emitBulkheadFull() {
	if h.OnBulkheadFull != nil {
		h.OnBulkheadFull()
	}
}

func (h *Hooks) emitBulkheadAcquired() {
	if h.OnBulkheadAcquired != nil {
		h.OnBulkheadAcquired()
	}
}

func (h *Hooks) emitBulkheadReleased() {
	if h.OnBulkheadReleased != nil {
		h.OnBulkheadReleased()
	}
}

func (h *Hooks) emitTimeout() {
	if h.OnTimeout != nil {
		h.OnTimeout()
	}
}

func (h *Hooks) emitHedgeTriggered() {
	if h.OnHedgeTriggered != nil {
		h.OnHedgeTriggered()
	}
}

func (h *Hooks) emitHedgeWon() {
	if h.OnHedgeWon != nil {
		h.OnHedgeWon()
	}
}

func (h *Hooks) emitFallbackUsed(err error) {
	if h.OnFallbackUsed != nil {
		h.OnFallbackUsed(err)
	}
}

func (h *Hooks) emitRetryBudgetExceeded() {
	if h.OnRetryBudgetExceeded != nil {
		h.OnRetryBudgetExceeded()
	}
}

func (h *Hooks) emitTimeBudgetExceeded() {
	if h.OnTimeBudgetExceeded != nil {
		h.OnTimeBudgetExceeded()
	}
}

func (h *Hooks) emitCoalesceLeader() {
	if h.OnCoalesceLeader != nil {
		h.OnCoalesceLeader()
	}
}

func (h *Hooks) emitCoalesceFollower() {
	if h.OnCoalesceFollower != nil {
		h.OnCoalesceFollower()
	}
}

func (h *Hooks) emitCacheHit() {
	if h.OnCacheHit != nil {
		h.OnCacheHit()
	}
}

func (h *Hooks) emitCacheMiss() {
	if h.OnCacheMiss != nil {
		h.OnCacheMiss()
	}
}

func (h *Hooks) emitCacheStored() {
	if h.OnCacheStored != nil {
		h.OnCacheStored()
	}
}

func (h *Hooks) emitStaleServed() {
	if h.OnStaleServed != nil {
		h.OnStaleServed()
	}
}

func (h *Hooks) emitConcurrencyRejected() {
	if h.OnConcurrencyRejected != nil {
		h.OnConcurrencyRejected()
	}
}

func (h *Hooks) emitConcurrencyLimitChanged(limit int) {
	if h.OnConcurrencyLimitChanged != nil {
		h.OnConcurrencyLimitChanged(limit)
	}
}

func (h *Hooks) emitThrottled() {
	if h.OnThrottled != nil {
		h.OnThrottled()
	}
}

func (h *Hooks) emitSlowCallRateExceeded() {
	if h.OnSlowCallRateExceeded != nil {
		h.OnSlowCallRateExceeded()
	}
}
