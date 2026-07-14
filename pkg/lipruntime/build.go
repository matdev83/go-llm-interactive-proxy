package lipruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Runtime is an opaque handle over a successfully built OSS composition.
// It does not expose internal Executor or runtimebundle types.
type Runtime struct {
	built                    *runtimebundle.Built
	shutdownTracing          func(context.Context) error
	closers                  []func() error
	trafficObserversAttached bool
	usageObserversAttached   bool
}

// Build constructs a production runtime from public options. The standard
// plugin registry is installed internally; callers must not import internal packages.
func Build(ctx context.Context, opts Options) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lipruntime: nil context")
	}
	path := strings.TrimSpace(opts.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("lipruntime: empty config path")
	}
	for i, d := range opts.ProviderDescriptors {
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("lipruntime: provider_descriptors[%d]: %w", i, err)
		}
	}
	logOut := opts.LogWriter
	if logOut == nil {
		logOut = io.Discard
	}
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  logOut,
		Production: runtimebundle.ProductionOptions{
			MeteringRecorder:          opts.MeteringRecorder,
			RequestProviders:          opts.RequestProviders,
			AttemptProviders:          opts.AttemptProviders,
			ConcurrencyProvider:       opts.ConcurrencyProvider,
			UsageSnapshotSource:       opts.UsageSnapshotSource,
			ConcurrencySnapshotSource: opts.ConcurrencySnapshotSource,
			RatingSnapshotSource:      opts.RatingSnapshotSource,
			TrafficObservers:          opts.TrafficObservers,
			UsageObservers:            opts.UsageObservers,
			PolicyObservers:           opts.PolicyObservers,
		},
	})
	if err != nil {
		return nil, err
	}
	if res.Built == nil {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.WithoutCancel(ctx))
		}
		return nil, fmt.Errorf("lipruntime: bootstrap returned nil runtime")
	}
	return &Runtime{
		built:                    res.Built,
		shutdownTracing:          res.ShutdownTracing,
		closers:                  append([]func() error(nil), res.Built.Closers...),
		trafficObserversAttached: len(opts.TrafficObservers) > 0,
		usageObserversAttached:   len(opts.UsageObservers) > 0,
	}, nil
}

// ExecutorView returns the public executor surface for request execution.
// The concrete Executor type remains unexported.
func (r *Runtime) ExecutorView() lipsdk.ExecutorView {
	if r == nil || r.built == nil {
		return nil
	}
	return r.built.Executor
}

// Ready reports whether the runtime has a usable executor.
func (r *Runtime) Ready() bool {
	return r != nil && r.built != nil && r.built.Executor != nil
}

// HasProductionMetering reports whether a production metering recorder was wired
// onto the executor (requirement 12.4 visibility for tests/fixtures).
func (r *Runtime) HasProductionMetering() bool {
	if r == nil || r.built == nil || r.built.Executor == nil {
		return false
	}
	return r.built.Executor.MeteringRecorder != nil
}

// HasTrafficObservers reports whether production traffic observers were supplied
// at Build (requirement 12.7 compatibility).
func (r *Runtime) HasTrafficObservers() bool {
	return r != nil && r.trafficObserversAttached
}

// HasUsageObservers reports whether production usage observers were supplied
// at Build (requirement 12.7 compatibility).
func (r *Runtime) HasUsageObservers() bool {
	return r != nil && r.usageObserversAttached
}

// SnapshotGenerationID returns the published generation id, or 0 when absent.
func (r *Runtime) SnapshotGenerationID() int64 {
	if r == nil || r.built == nil || r.built.SnapshotGeneration == nil {
		return 0
	}
	cur := r.built.SnapshotGeneration.Current()
	if cur == nil {
		return 0
	}
	return cur.ID
}

// Close releases runtime resources and tracing.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var first error
	for i := len(r.closers) - 1; i >= 0; i-- {
		if r.closers[i] == nil {
			continue
		}
		if err := r.closers[i](); err != nil && first == nil {
			first = err
		}
	}
	r.closers = nil
	if r.shutdownTracing != nil {
		if err := r.shutdownTracing(context.WithoutCancel(ctx)); err != nil && first == nil {
			first = err
		}
		r.shutdownTracing = nil
	}
	r.built = nil
	return first
}
