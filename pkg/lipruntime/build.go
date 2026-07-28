package lipruntime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
func Build(ctx context.Context, opts Options) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lipruntime: nil context")
	}
	path := strings.TrimSpace(opts.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("lipruntime: empty config path")
	}
	norm, err := normalizeCanonicalOptions(opts)
	if err != nil {
		return nil, err
	}
	logOut := opts.LogWriter
	if logOut == nil {
		logOut = io.Discard
	}
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  logOut,
		Production: runtimebundle.ProductionOptions{
			MeteringRecorder:          opts.MeteringRecorder,
			RequestRegistrations:      norm.RequestRegistrations,
			AttemptRegistrations:      norm.AttemptRegistrations,
			ConcurrencyRegistration:   norm.ConcurrencyRegistration,
			RaterRegistrations:        norm.RaterRegistrations,
			UsageSnapshotSource:       opts.UsageSnapshotSource,
			ConcurrencySnapshotSource: opts.ConcurrencySnapshotSource,
			RatingSnapshotSource:      opts.RatingSnapshotSource,
			EvidenceSink:              opts.EvidenceSink,
			MeteringQuerier:           opts.MeteringQuerier,
			TrafficObservers:          opts.TrafficObservers,
			UsageObservers:            opts.UsageObservers,
			PolicyObservers:           opts.PolicyObservers,
		},
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		return nil, err
	}
	api, err := adaptHost(ctx, host)
	if err != nil {
		return nil, err
	}
	return &Runtime{host: api}, nil
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
