package lipruntime

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Runtime is an opaque handle over a successfully built OSS composition.
// It does not expose internal Executor or runtimebundle types.
type Runtime struct {
	built                    *runtimebundle.Built
	shutdownTracing          func(context.Context) error
	closers                  []func() error
	trafficObserversAttached bool
	usageObserversAttached   bool
	evidenceSinkAttached     bool
	raterAttached            bool
	meteringQuerierAttached  bool
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
	norm, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	logOut := opts.LogWriter
	if logOut == nil {
		logOut = io.Discard
	}
	raterAttached := len(norm.RaterRegistrations) > 0
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapServe,
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
		evidenceSinkAttached:     opts.EvidenceSink != nil,
		raterAttached:            raterAttached,
		meteringQuerierAttached:  opts.MeteringQuerier != nil,
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

// HasProductionEvidenceSink reports whether a production EvidenceSink was supplied.
func (r *Runtime) HasProductionEvidenceSink() bool {
	return r != nil && r.evidenceSinkAttached
}

// HasProductionRater reports whether a production Rater was wired onto the executor.
func (r *Runtime) HasProductionRater() bool {
	if r == nil || r.built == nil || r.built.Executor == nil {
		return false
	}
	return r.raterAttached && r.built.Executor.EconomicsRater != nil
}

// MeteringQuerier returns the production metering query mount, or nil.
func (r *Runtime) MeteringQuerier() metering.Querier {
	if r == nil || r.built == nil {
		return nil
	}
	return r.built.MeteringQuerier
}

// HasProductionMeteringQuerier reports whether a metering Querier was supplied.
func (r *Runtime) HasProductionMeteringQuerier() bool {
	return r != nil && r.meteringQuerierAttached && r.MeteringQuerier() != nil
}

// ReadinessReport returns the composite readiness report reader, or nil.
func (r *Runtime) ReadinessReport() controlplane.ReadinessReportReader {
	if r == nil || r.built == nil {
		return nil
	}
	return r.built.ReadinessReport
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

// SnapshotUsageVersion returns the active usage-authority snapshot version, or "".
func (r *Runtime) SnapshotUsageVersion() string {
	if r == nil || r.built == nil || r.built.SnapshotGeneration == nil {
		return ""
	}
	cur := r.built.SnapshotGeneration.Current()
	if cur == nil {
		return ""
	}
	return cur.Usage.Version
}

// RefreshSnapshots re-reads injectable snapshot sources and atomically publishes
// a new immutable generation for subsequent admissions (requirements 11.3, 11.6).
// In-flight requests keep their previously bound generation pointers.
// Source failures expose degraded/unavailable posture without substituting an
// unrelated policy version (requirement 11.7).
func (r *Runtime) RefreshSnapshots(ctx context.Context) error {
	if r == nil || r.built == nil || r.built.SnapshotController == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("lipruntime: nil context")
	}
	return r.built.SnapshotController.Refresh(ctx)
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
	for _, v := range slices.Backward(r.closers) {
		if v == nil {
			continue
		}
		if err := v(); err != nil && first == nil {
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
