// Package clocktest provides a deterministic [r8e.Clock] for use in external
// test packages (r8econf, r8ehttp) that cannot reach the package-internal fake
// clock. Timers fire immediately so retry/backoff sleeps complete without real
// waiting; Advance moves the reported time forward for recovery-timeout logic.
package clocktest

import (
	"sync"
	"time"

	"github.com/byte4ever/r8e"
)

// Clock is a controllable [r8e.Clock]. The zero value is not usable; call [New].
type Clock struct {
	now    time.Time
	offset time.Duration
	mu     sync.Mutex
}

// New returns a Clock anchored at a fixed instant.
func New() *Clock {
	return &Clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now returns the anchored time plus any advanced offset.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now.Add(c.offset)
}

// Since returns the elapsed duration since t.
func (c *Clock) Since(t time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now.Add(c.offset).Sub(t)
}

// NewTimer returns a timer that has already fired, so backoff sleeps return
// immediately in tests.
//
//nolint:ireturn // satisfies the r8e.Timer interface by design
func (c *Clock) NewTimer(_ time.Duration) r8e.Timer {
	ch := make(chan time.Time, 1)
	ch <- c.Now()

	return &timer{ch: ch}
}

// Advance moves the reported time forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.offset += d
}

//nolint:decorder // timer is a small helper type kept next to its use
type timer struct {
	ch chan time.Time
}

func (t *timer) C() <-chan time.Time      { return t.ch }
func (*timer) Stop() bool                 { return true }
func (*timer) Reset(_ time.Duration) bool { return false }
