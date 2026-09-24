package lipruntime

import (
	"context"
	"sync"
)

// Runtime is an opaque handle over a successfully built OSS composition.
// Close synchronization follows the public retry/idempotency contract (req 10.1-10.4).
type Runtime struct {
	host    hostAPI
	closeMu sync.Mutex
	closed  bool
}

// Build constructs a production runtime from public options.
// Callers must not import internal packages; Build installs the standard registry.
// The returned Runtime owns one immutable host-facing dependency graph.
// Build binds one complete Host via runtimebundle.BuildHost (req 4.1, 10.1-10.4).
// Build stays non-money: monetary composition requires BuildWithBilling.
func Build(ctx context.Context, opts Options) (*Runtime, error) {
	return buildCommon(ctx, opts, nil)
}

// Close releases runtime resources by delegating to Host.Close (req 8.6-8.7).
// Calls are serialized. A successful Close is idempotent; a deadline or teardown
// failure remains retryable. A nil ctx is tolerated and substituted with
// [context.Background].
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.closed {
		return nil
	}
	if r.host != nil {
		if err := r.host.Close(ctx); err != nil {
			return err
		}
	}
	r.closed = true
	return nil
}
