package r8e

import "time"

// Hooks holds optional callback functions for resilience pattern lifecycle
// events. All fields are nil by default; callers set only the hooks they care
// about. Once constructed, a Hooks value must not be mutated — emit methods
// read the function fields without synchronisation, which is safe as long as
// the struct is read-only after initialisation.
//
// Pattern: Observer — decouples resilience event emission from consumers
// (logging, metrics, alerting) without patterns knowing about observers.
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
	OnStaleServed      func(age time.Duration)
	OnCacheRefreshed   func()
	OnHedgeTriggered   func()
	OnHedgeWon         func()
	OnFallbackUsed     func(err error)
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

func (h *Hooks) emitStaleServed(age time.Duration) {
	if h.OnStaleServed != nil {
		h.OnStaleServed(age)
	}
}

func (h *Hooks) emitCacheRefreshed() {
	if h.OnCacheRefreshed != nil {
		h.OnCacheRefreshed()
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
