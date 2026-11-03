package r8e

import "context"

// detacher returns the base context of the calls a policy runs detached from
// their caller (see WithBaseContext). It is a func, not a stored
// context.Context, so no struct holds a context. The zero value has no base.
type detacher func() context.Context

// WithBaseContext ties the calls a policy runs detached from their caller to
// ctx: the shared call of [WithCoalesce] and the background reload of
// [RefreshAhead]. Those calls ignore the caller's cancellation and deadline on
// purpose, so that one caller leaving cannot abort work other callers wait
// for, and only the inner [WithTimeout] bounds them. Cancelling ctx stops
// them: their context ends with ctx's cause, so a client that owns the policy
// can stop them when it shuts down. They keep the values of the caller's
// context and still ignore the caller's own cancellation.
//
// Once ctx has ended, the policy stops every detached call, for every holder
// of the policy, including the calls started after the end: a call with a
// coalescing key fails at once with [context.Canceled] (the cause is on the
// context fn would get), and a refresh-ahead reload stops with no error
// surfacing, the entry keeping its value until it expires. A call with no
// coalescing key still runs on its caller's context. No failure is cached
// negatively once ctx has ended, so the end of ctx never reaches a later call
// through the cache, nor another policy sharing it. Set ctx only on a policy
// whose holders all stop with its owner.
//
// A nil ctx is ignored. Code-only: it has no config key and Reconfigure does
// not change it.
func WithBaseContext(ctx context.Context) Option {
	return optionFunc(func(s *policySetup) {
		if ctx != nil {
			s.detach = func() context.Context { return ctx }
		}
	})
}

// base returns the base context, or nil when there is none.
func (d detacher) base() context.Context {
	if d == nil {
		return nil
	}

	return d()
}

// derive returns the context a call detached from parent runs under, and the
// stop func to call once the call returns (see detachedContext).
func (d detacher) derive(parent context.Context) (ctx context.Context, stop func()) {
	return detachedContext(parent, d.base())
}

// ended reports whether the base context has ended. It is false with no base.
func (d detacher) ended() bool {
	base := d.base()

	return base != nil && base.Err() != nil
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
