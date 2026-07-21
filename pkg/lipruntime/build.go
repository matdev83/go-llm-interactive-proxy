package lipruntime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Runtime is an opaque handle over a successfully built OSS composition.
// It does not expose internal Executor or runtimebundle types.
// Facade pointers (host/executor/reload) remain immutable after Build so
// concurrent Reload/Status/ExecutorView remain safe across Close (req 13.7-13.8).
type Runtime struct {
	host                     *runtimebundle.ReloadHost
	executor                 lipsdk.ExecutorView
	shutdownTracing          func(context.Context) error
	trafficObserversAttached bool
	usageObserversAttached   bool
	evidenceSinkAttached     bool
	raterAttached            bool
	meteringAttached         bool
	meteringQuerierAttached  bool
	reload                   *ReloadControl

	closeMu sync.Mutex
	closed  bool
}

// Build constructs a production runtime from public options. The standard
// plugin registry is installed internally; callers must not import internal packages.
// Build binds the same reload coordinator/compiler/manager as cmd/lipstd and
// returns a stable GenerationExecutor facade (req 16.1, 16.12-16.13).
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
	compose := stdhttp.ComposeRequestPlane
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
		HandlerComposer: compose,
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Runtime, error) {
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(context.WithoutCancel(ctx), runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	if res.GenerationManager == nil || res.ProcessServices == nil {
		return fail(fmt.Errorf("lipruntime: bootstrap returned nil generation host"))
	}
	host, err := runtimebundle.AttachReloadHost(ctx, res, path, compose)
	if err != nil {
		return fail(err)
	}
	rt := &Runtime{
		host:                     host,
		executor:                 host.Executor,
		shutdownTracing:          res.ShutdownTracing,
		trafficObserversAttached: len(opts.TrafficObservers) > 0,
		usageObserversAttached:   len(opts.UsageObservers) > 0,
		evidenceSinkAttached:     opts.EvidenceSink != nil,
		raterAttached:            raterAttached,
		meteringAttached:         opts.MeteringRecorder != nil,
		meteringQuerierAttached:  opts.MeteringQuerier != nil,
	}
	rt.bindReloadQuery(host.Coordinator)
	return rt, nil
}

// ExecutorView returns the stable generation-dispatching executor facade.
func (r *Runtime) ExecutorView() lipsdk.ExecutorView {
	if r == nil {
		return nil
	}
	return r.executor
}

// Ready reports whether the runtime has a usable active-generation executor.
func (r *Runtime) Ready() bool {
	if r == nil || r.host == nil || r.host.Manager == nil || r.executor == nil {
		return false
	}
	return r.host.Manager.Active() != nil
}

// HasProductionMetering reports whether a production metering recorder was wired
// onto the active generation executor (requirement 12.4 visibility for tests/fixtures).
func (r *Runtime) HasProductionMetering() bool {
	if r == nil || r.host == nil {
		return false
	}
	return r.host.ActiveHasProductionMetering()
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

// HasProductionRater reports whether a production operator Rater was wired onto
// the active generation executor.
func (r *Runtime) HasProductionRater() bool {
	if r == nil || r.host == nil {
		return false
	}
	return r.host.ActiveHasProductionRater()
}

// MeteringQuerier returns the production metering query mount, or nil.
func (r *Runtime) MeteringQuerier() metering.Querier {
	if r == nil || r.host == nil || r.host.Process == nil {
		return nil
	}
	return r.host.Process.MeteringQuerier
}

// HasProductionMeteringQuerier reports whether a metering Querier was supplied.
func (r *Runtime) HasProductionMeteringQuerier() bool {
	return r != nil && r.meteringQuerierAttached && r.MeteringQuerier() != nil
}

// ReadinessReport returns the active generation readiness report reader, or nil.
func (r *Runtime) ReadinessReport() controlplane.ReadinessReportReader {
	if r == nil || r.host == nil || r.host.Manager == nil {
		return nil
	}
	g := r.host.Manager.Active()
	if g == nil {
		return nil
	}
	type readinessProvider interface {
		ReadinessReport() controlplane.ReadinessReportReader
	}
	if p, ok := g.RequestPlane().(readinessProvider); ok {
		return p.ReadinessReport()
	}
	return nil
}

// SnapshotGenerationID returns the published metadata compatibility generation
// id, or 0 when absent. Prefer [ExecutableGenerationID] for enforcement identity.
func (r *Runtime) SnapshotGenerationID() int64 {
	pub := snapshotPublisher(r)
	if pub == nil {
		return 0
	}
	cur := pub.Current()
	if cur == nil {
		return 0
	}
	return cur.ID
}

// SnapshotUsageVersion returns the active usage-authority source-fetch metadata
// version, or "".
func (r *Runtime) SnapshotUsageVersion() string {
	pub := snapshotPublisher(r)
	if pub == nil {
		return ""
	}
	cur := pub.Current()
	if cur == nil {
		return ""
	}
	return cur.Usage.Version
}

// ExecutableGenerationID returns the active executable generation id, or 0.
func (r *Runtime) ExecutableGenerationID() int64 {
	pub := snapshotPublisher(r)
	if pub == nil {
		return 0
	}
	exec := pub.CurrentExecutable()
	if exec == nil {
		return 0
	}
	return exec.ID
}

// ExecutableGenerationVersion returns the active executable generation version.
func (r *Runtime) ExecutableGenerationVersion() string {
	pub := snapshotPublisher(r)
	if pub == nil {
		return ""
	}
	exec := pub.CurrentExecutable()
	if exec == nil {
		return ""
	}
	return exec.Version
}

// ExecutableGenerationState returns executable generation readiness as a public
// capability state (separate from source-fetch metadata planes).
func (r *Runtime) ExecutableGenerationState() controlplane.CapabilityState {
	pub := snapshotPublisher(r)
	if pub == nil {
		return controlplane.CapabilityDisabled
	}
	exec := pub.CurrentExecutable()
	if exec == nil {
		return controlplane.CapabilityDisabled
	}
	switch exec.State {
	case economics.SnapshotReady, economics.SnapshotStale, "":
		return controlplane.CapabilityReady
	case economics.SnapshotDegraded:
		return controlplane.CapabilityDegraded
	case economics.SnapshotUnavailable:
		return controlplane.CapabilityUnavailable
	case economics.SnapshotDisabled:
		return controlplane.CapabilityDisabled
	default:
		return controlplane.CapabilityUnavailable
	}
}

// ExecutableEvidenceObjectID returns the evaluator object identity used in
// settlement/admission evidence (requirement 9.9), not a metadata-only label.
func (r *Runtime) ExecutableEvidenceObjectID() string {
	pub := snapshotPublisher(r)
	if pub == nil {
		return ""
	}
	exec := pub.CurrentExecutable()
	if exec == nil {
		return ""
	}
	return exec.EvidenceObjectID()
}

// RefreshSnapshots re-reads injectable source-fetch metadata views and, when
// sources succeed, republishes an executable generation for subsequent
// admissions. This remains a subordinate explicit policy refresh, not
// whole-config reload (task 5.5).
func (r *Runtime) RefreshSnapshots(ctx context.Context) error {
	if r == nil || r.host == nil || r.host.Process == nil || r.host.Process.SnapshotController == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("lipruntime: nil context")
	}
	return r.host.Process.SnapshotController.Refresh(ctx)
}

// Close releases runtime resources and tracing using ownership order:
// begin reload shutdown → await candidate idle → drain generations →
// close process services → tracing. The supplied context deadline bounds drain
// and tracing; it is not stripped. Calls are serialized. A successful Close is
// idempotent; a deadline or teardown failure remains retryable after the caller
// releases outstanding pins or otherwise resolves the blocker. Facade pointers
// remain immutable so concurrent Reload/Status/ExecutorView fail through
// manager/coordinator shutdown state rather than racing with nil assignments.
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

	host := r.host
	if host != nil {
		host.BeginShutdown()
		// Candidate work must be canceled and rolled back before generation or
		// process-service teardown can advance.
		if err := host.WaitForIdle(ctx); err != nil {
			return err
		}
		if host.Manager != nil {
			if err := host.Manager.ShutdownDetached(ctx, runtimehost.NewLifecycleWorker()); err != nil {
				return err
			}
			if host.Manager.HasOpenGenerations() {
				return fmt.Errorf("lipruntime: generations remain open after shutdown")
			}
		}
		if host.Process != nil {
			if err := host.Process.Close(); err != nil {
				return err
			}
		}
	}
	if r.shutdownTracing != nil {
		if err := r.shutdownTracing(ctx); err != nil {
			return err
		}
	}
	r.closed = true
	return nil
}

func snapshotPublisher(r *Runtime) *snapshotgen.Publisher {
	if r == nil || r.host == nil || r.host.Process == nil {
		return nil
	}
	return r.host.Process.SnapshotGeneration
}
