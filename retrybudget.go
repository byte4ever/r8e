package r8e

import "sync"

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------.

type (
	// retryBudgetConfig holds the parameters for a RetryBudget before they are
	// validated and stored on the budget in NewRetryBudget.
	retryBudgetConfig struct {
		parent    *RetryBudget
		maxTokens int
		ratio     float64
	}

	// RetryBudgetOption configures a RetryBudget.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewRetryBudget's signature stable.
	RetryBudgetOption func(*retryBudgetConfig)

	// RetryBudget is an adaptive token bucket that throttles retries so a burst
	// of failures cannot amplify a downstream outage (a "retry storm"). It
	// follows gRPC's retryThrottling model: every success returns ratio tokens
	// to the bucket and every retryable failure removes one, and retries are
	// suppressed while the bucket sits at or below half its capacity. Because
	// the bucket is driven purely by call outcomes (no wall-clock refill) it is
	// deterministic and needs no Clock.
	//
	// Construct a RetryBudget with NewRetryBudget; the zero value is not a
	// usable budget (Reconfigure will initialise a zero-value budget to full).
	//
	// A single budget may be shared across several policies to coordinate
	// retries process-wide (see WithSharedRetryBudget); it is safe for
	// concurrent use. The three fields move together under one mutex: capacity,
	// ratio, and the live token count must stay mutually consistent across a
	// concurrent Reconfigure (lowering the capacity must not be able to leave
	// tokens above it), which requires compound atomicity a lock provides and
	// independent atomics do not.
	//
	// Budgets may be NESTED into a tree (see Parent): a child's outcomes are also
	// recorded against its parent and every ancestor, and a retry is permitted
	// only when the child AND every ancestor allow it. So a parent budget caps the
	// AGGREGATE retry pressure of all its descendants — a retry storm in one leaf
	// drains the shared parent and throttles its siblings too, stopping
	// amplification from cascading up a call graph (gateway → service →
	// downstream). A root budget (no parent) behaves exactly as before. The parent
	// link is set once at construction and is immutable. Because the parent always
	// exists before the child, the tree is acyclic by construction.
	//
	// Pattern: Retry Budget — adaptive token bucket gates retries against the
	// live success/failure ratio; mutex-guarded so the (tokens, capacity, ratio)
	// triple stays coherent under concurrent Reconfigure.
	RetryBudget struct {
		// parent is the optional ancestor budget this one rolls up into; nil for a
		// root. Immutable after construction, so it is read without the mutex.
		parent    *RetryBudget
		mu        sync.Mutex
		tokens    float64
		maxTokens float64
		ratio     float64
	}
)

// Default budget parameters, matching gRPC's retryThrottling defaults.
const (
	defaultBudgetMaxTokens = 10
	defaultBudgetRatio     = 0.1
)

// MaxTokens sets the budget capacity. Retries are suppressed once the bucket
// drains to half this value. Values below 1 are clamped to the default.
// Default: 10.
func MaxTokens(n int) RetryBudgetOption {
	return func(cfg *retryBudgetConfig) {
		cfg.maxTokens = n
	}
}

// TokenRatio sets how many tokens each success returns to the bucket; each
// retryable failure removes one whole token. A smaller ratio makes the budget
// stricter. Values at or below 0 are clamped to the default. Default: 0.1.
func TokenRatio(r float64) RetryBudgetOption {
	return func(cfg *retryBudgetConfig) {
		cfg.ratio = r
	}
}

// Parent nests the new budget under an existing one, making it a child in a
// budget tree: the child's call outcomes are also recorded against parent (and,
// transitively, every ancestor), and a retry is permitted only when the child
// AND every ancestor still allow it. Size the parent for the AGGREGATE retry
// pressure of all its descendants — it caps the whole subtree, so a storm in one
// child throttles its siblings too. A nil parent (the default) leaves the budget
// a root. The link is set at construction and is immutable; passing Parent to
// [RetryBudget.Reconfigure] has no effect. Works with both [WithRetryBudget] and
// a [WithSharedRetryBudget] budget built via [NewRetryBudget].
func Parent(parent *RetryBudget) RetryBudgetOption {
	return func(cfg *retryBudgetConfig) {
		cfg.parent = parent
	}
}

// NewRetryBudget creates an adaptive retry budget. The bucket starts full, so a
// healthy service is never throttled until failures have drained it below half
// capacity. Invalid parameters are clamped to the defaults rather than
// panicking, matching NewRateLimiter's tolerant construction.
func NewRetryBudget(opts ...RetryBudgetOption) *RetryBudget {
	cfg := retryBudgetConfig{
		maxTokens: defaultBudgetMaxTokens,
		ratio:     defaultBudgetRatio,
	}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	maxTokens := float64(cfg.maxTokens)

	return &RetryBudget{
		parent: cfg.parent,
		// Start with a full bucket so a healthy service is never throttled.
		tokens:    maxTokens,
		maxTokens: maxTokens,
		ratio:     cfg.ratio,
	}
}

// clamp replaces out-of-range parameters with their defaults so the bucket
// arithmetic always has a positive capacity and ratio.
func (c *retryBudgetConfig) clamp() {
	if c.maxTokens < 1 {
		c.maxTokens = defaultBudgetMaxTokens
	}

	if c.ratio <= 0 {
		c.ratio = defaultBudgetRatio
	}
}

// Reconfigure retunes the budget at runtime. Options are applied on top of the
// current parameters, so a partial update leaves the unspecified one unchanged
// (matching CircuitBreaker.Reconfigure). The ratio, when given, is replaced
// outright (it is not capacity-relative). The live token count is rescaled to
// preserve its fill fraction across a capacity change, so resizing does not
// abruptly change how throttled the budget is — raising the capacity does not
// deepen exhaustion, and lowering it does not relax throttling. (allowRetry
// depends only on tokens/maxTokens, so the current throttle decision is stable
// across the resize.) Safe for concurrent use with the retry path.
func (b *RetryBudget) Reconfigure(opts ...RetryBudgetOption) {
	b.mu.Lock()
	defer b.mu.Unlock()

	oldMax := b.maxTokens

	// Seed from the current values so unspecified options are preserved.
	cfg := retryBudgetConfig{maxTokens: int(oldMax), ratio: b.ratio}

	for _, o := range opts {
		o(&cfg)
	}

	cfg.clamp()

	newMax := float64(cfg.maxTokens)
	b.ratio = cfg.ratio

	// Rescale tokens to the same fill fraction under the new capacity. A
	// constructed budget always has oldMax >= 1, but the exported zero value
	// (oldMax == 0, a RetryBudget not built via NewRetryBudget) has no fraction
	// to preserve and would divide by zero — initialise it to a full bucket,
	// matching NewRetryBudget, rather than leaving it drained.
	if oldMax > 0 {
		b.tokens *= newMax / oldMax
	} else {
		b.tokens = newMax
	}

	b.maxTokens = newMax

	// Guard against floating-point drift nudging tokens out of [0, newMax].
	b.tokens = min(max(b.tokens, 0), newMax)
}

// recordSuccess returns ratio tokens to the bucket, capped at capacity, then
// credits every ancestor so a parent budget tracks the aggregate of its subtree.
// It is a no-op on a nil budget so the retry path can call it unconditionally.
// Each level locks only its own bucket and releases it before recursing, so the
// walk takes one lock at a time (never two at once) and cannot deadlock.
func (b *RetryBudget) recordSuccess() {
	if b == nil {
		return
	}

	b.mu.Lock()
	b.tokens = min(b.tokens+b.ratio, b.maxTokens)
	b.mu.Unlock()

	b.parent.recordSuccess()
}

// recordFailure removes one whole token from the bucket, floored at zero, then
// charges every ancestor. It is a no-op on a nil budget. Like recordSuccess it
// holds one lock at a time as it walks up the tree.
func (b *RetryBudget) recordFailure() {
	if b == nil {
		return
	}

	b.mu.Lock()
	b.tokens = max(b.tokens-1, 0)
	b.mu.Unlock()

	b.parent.recordFailure()
}

// allowRetry reports whether a retry is permitted: this bucket allows it locally
// AND every ancestor allows it too. The check short-circuits up the tree — an
// exhausted child is denied without consulting its parent — so any exhausted
// level on the path to the root blocks the retry. A nil budget always allows
// retries. The lock is released before recursing.
func (b *RetryBudget) allowRetry() bool {
	if b == nil {
		return true
	}

	return b.allowRetryLocal() && b.parent.allowRetry()
}

// allowRetryLocal reports whether THIS bucket alone permits a retry, ignoring any
// ancestors: it holds strictly more than half its capacity, matching gRPC's
// throttling threshold. tokens and maxTokens are read together under the lock so
// a concurrent Reconfigure cannot tear the comparison.
func (b *RetryBudget) allowRetryLocal() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.tokens > b.maxTokens/2
}

// Tokens returns the current number of tokens in the bucket as a point-in-time
// snapshot. A nil budget reports 0. Surfaced by Policy.Metrics as a gauge.
func (b *RetryBudget) Tokens() float64 {
	if b == nil {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.tokens
}

// Exhausted reports whether THIS budget's own bucket is throttling retries
// (tokens at or below half capacity), ignoring any ancestors. A nil budget is
// never exhausted. Surfaced as a degraded health condition, so in a budget tree
// it pinpoints the level that is actually drained: a child whose retries are
// blocked only because an ancestor is exhausted reports healthy here.
//
// A drained ancestor reports the degradation through its OWN policy's health
// only if it is attached to one. A shared parent budget held by no policy of its
// own (a common topology — one gateway budget behind several child policies)
// surfaces its exhaustion only through the children's OnRetryBudgetExceeded hook
// and RetryBudgetExceeded metric, which fire on the full-chain retry decision —
// not through any health condition. Monitor such a parent by reading its own
// Exhausted / Tokens directly.
func (b *RetryBudget) Exhausted() bool {
	if b == nil {
		return false
	}

	return !b.allowRetryLocal()
}
