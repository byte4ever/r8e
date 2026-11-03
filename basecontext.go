package r8e

import "context"

// detacher builds the context a call detached from its caller runs under (see
// detachedContext). The zero value detaches with no base context.
type detacher func(parent context.Context) (ctx context.Context, stop func())

// WithBaseContext ties the calls a policy runs detached from their caller to
// ctx: the shared call of [WithCoalesce] and the background reload of
// [RefreshAhead]. Those calls ignore the caller's cancellation and deadline on
// purpose, so that one caller leaving cannot abort work other callers wait
// for, and only the inner [WithTimeout] bounds them. Cancelling ctx stops
// them: their context ends with ctx's cause, so a client that owns the policy
// can stop them when it shuts down. They keep the values of the caller's
// context and still ignore the caller's own cancellation.
//
// The base context only stops detached calls; a call on the caller's path is
// not affected. A nil ctx is ignored. Code-only: it has no config key and
// Reconfigure does not change it. A policy has no close method of its own,
// because it is often shared and closing it would cut other callers' calls.
func WithBaseContext(ctx context.Context) Option {
	return optionFunc(func(s *policySetup) {
		if ctx != nil {
			s.detach = detachFrom(ctx)
		}
	})
}

// detachFrom returns the detacher whose calls also end when base ends.
func detachFrom(base context.Context) detacher {
	return func(parent context.Context) (context.Context, func()) {
		return detachedContext(parent, base)
	}
}

// run detaches parent: with the base context d was built from, or with none
// when d is nil.
func (d detacher) run(parent context.Context) (ctx context.Context, stop func()) {
	if d == nil {
		return detachedContext(parent, nil)
	}

	return d(parent)
}

// detachedContext returns the context a detached call runs under: parent's
// values, without parent's cancellation or deadline. When base is non-nil, the
// returned context also ends when base does, with base's cause. The returned
// stop func releases the link to base and must be called once the call
// returns.
func detachedContext(parent, base context.Context) (ctx context.Context, stop func()) {
	detached := context.WithoutCancel(parent)
	if base == nil {
		return detached, func() {}
	}

	linked, cancel := context.WithCancelCause(detached)
	if base.Err() != nil {
		// Already ended: end the call before it starts, not on AfterFunc's
		// goroutine.
		cancel(context.Cause(base))

		return linked, func() {}
	}

	unlink := context.AfterFunc(base, func() { cancel(context.Cause(base)) })

	return linked, func() {
		unlink()
		cancel(nil)
	}
}
