package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// GenerationCompileInput is the non-owning input to [CompileCandidate] /
// [CompileGeneration]. Process must outlive the candidate; candidate Close must
// not Close Process.
type GenerationCompileInput struct {
	Process *ProcessServices
	Bus     *hooks.Bus
	// Candidate is the isolated effective configuration for this compile.
	// When nil, Process startup config is used (compatibility with [Build]).
	Candidate *config.Config
	// CandidateOpts supplies generation-owned options (feature lifecycles /
	// extensions) without mutating Process startup options. Process-fixed
	// fields (PluginRegistry, Infra, Testing, Production, Auth, …) remain
	// sourced from ProcessServices.
	CandidateOpts *BuildOptions
	// Compose builds the request-plane http.Handler without binding a listener.
	// Required by [CompileGeneration]; unused by [CompileCandidate].
	Compose HandlerComposer
	// FaultInject is test-only; production leaves it zero.
	FaultInject CandidateFaultInject
}

// CandidateRuntime holds generation-owned assembly produced by [CompileCandidate].
// Process service fields are non-owning references shared across candidates.
type CandidateRuntime struct {
	Executor              *runtime.Executor
	Store                 b2bua.Store
	UpstreamHTTP          *http.Client
	RoutePrefixes         []string
	DecodeAdmission       lipsdk.DecodeAdmission
	PluginRegistry        *pluginreg.Registry
	DatabasePools         *db.PoolRegistry
	Metrics               *metrics.Bundle
	RuntimeSnapshot       *extensions.RequestRuntimeSnapshot
	HTTPAuthProviders     []httpauth.Provider
	SecureSessionStore    ssessionapp.Store
	AuthEventDispatcher   *auth.EventDispatcher
	CatalogRuntime        *modelcatalog.CatalogRuntime
	ModelRegistry         *modelregistry.Registry
	ModelRegistryRuntime  *modelregistry.Runtime
	TokenAccountingAdmin  *accountingapp.Service
	ControlPlaneQueries   *controlplane.QueryService
	ControlPlaneStatus    *controlplane.Status
	ControlPlaneRetention *controlplane.RetentionController
	UsageAuthority        *authorityapp.Service
	ConcurrencyAuthority  *concurrencyapp.Service
	SnapshotGeneration    *snapshotgen.Publisher
	SnapshotController    *SnapshotController
	MeteringQuerier       metering.Querier
	ReadinessReport       *controlplane.ReadinessReportService
	SecretGuardInventory  *diag.InventoryExtras
	TerminalWorkProcessor *terminalworkapp.Processor
	TerminalWorkRegistry  *terminalworkapp.Registry
	TerminalWorkQueries   *terminalworkapp.QueryService
	TerminalWorkMetrics   *terminalworkapp.MetricsObserver
	EffectiveDefaultRoute string

	// ProcessTracingShutdown is intentionally always nil on candidates:
	// tracing ownership stays on ProcessServices / bootstrap (req 6.4, 6.10).
	ProcessTracingShutdown func(context.Context) error

	// Closers contains generation-owned teardown only (legacy view of Ledger).
	Closers []func() error
	// Ledger owns generation resources for rollback/quiesce/close (task 3.2).
	Ledger *ResourceLedger

	closeOnce   sync.Once
	closeErr    error
	quiesceOnce sync.Once
	quiesceErr  error
	didQuiesce  atomic.Bool

	terminalWorkReady func(context.Context) error
	terminalWorkRT    *terminalWorkRuntime
}

// NewCandidateRuntimeForTest builds a minimal candidate bound to ledger (tests).
func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateRuntime {
	c := &CandidateRuntime{Ledger: ledger}
	if ledger != nil {
		c.Closers = ledger.LegacyClosers()
	}
	return c
}

// Quiesce stops admission-independent generation workers once (req 10.5).
func (c *CandidateRuntime) Quiesce(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.quiesceOnce.Do(func() {
		c.didQuiesce.Store(true)
		if c.Ledger != nil {
			c.quiesceErr = c.Ledger.Quiesce(ctx)
		}
	})
	return c.quiesceErr
}

// Close disposes generation-owned resources in reverse order.
// Unpublished discard / compile failure uses full ledger rollback; after Quiesce,
// only remaining close-phase resources are released. Never closes ProcessServices.
func (c *CandidateRuntime) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.Ledger != nil {
			if c.didQuiesce.Load() {
				c.closeErr = c.Ledger.Close(context.Background())
			} else {
				c.closeErr = c.Ledger.Rollback(context.Background())
			}
			return
		}
		c.closeErr = disposeClosers(c.Closers)
	})
	return c.closeErr
}

// CompileCandidate builds one generation-owned candidate against shared process services.
// On failure, only candidate-acquired resources are disposed; Process survives.
func CompileCandidate(ctx context.Context, in GenerationCompileInput) (*CandidateRuntime, error) {
	if in.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	ps := in.Process
	if ps.Closed() {
		return nil, fmt.Errorf("runtimebundle: ProcessServices is closed")
	}
	cfg := in.Candidate
	if cfg == nil {
		cfg = ps.cfg
	}
	opts := mergeCandidateBuildOptions(ps.opts, in.CandidateOpts)
	log := ps.Logger
	if cfg == nil || opts == nil || opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: ProcessServices missing config or PluginRegistry")
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}

	// Reject unsafe feature lifecycles before any generation resource acquisition
	// so unmarked plugins cannot escape cleanup (req 8.8, task 3.2).
	if err := ClassifyFeatureLifecycles(opts.FeatureLifecycles); err != nil {
		return nil, err
	}

	bus := in.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	parent := ps.parent
	if ctx != nil {
		parent = ctx
	}
	if opts.Startup.StartupContext != nil {
		parent = opts.Startup.StartupContext
	}

	ledger := NewResourceLedger()
	bctx := buildContext{
		Cfg:               cfg,
		Bus:               bus,
		Log:               log,
		Opts:              opts,
		Parent:            parent,
		PostgresPools:     ps.DatabasePools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
		Ledger:            ledger,
	}

	var closers []func() error
	fail := func(err error) (*CandidateRuntime, error) {
		rollErr := ledger.Rollback(parent)
		if rollErr != nil {
			return nil, errors.Join(err, rollErr)
		}
		return nil, err
	}
	injectFault := func(boundary string) error {
		if in.FaultInject.After != boundary {
			return nil
		}
		if in.FaultInject.Hook != nil {
			in.FaultInject.Hook()
		}
		return fmt.Errorf("%w: after %s", ErrCandidateFaultInjected, boundary)
	}

	sec, err := buildSecurityRuntime(bctx, ps.controlPlane)
	if err != nil {
		return fail(err)
	}
	var regs []lipsdk.Registration
	if cfg != nil {
		regs = config.RegistrationsFromConfig(cfg)
	}
	sg, err := buildSecretGuardRuntime(cfg, log, opts, regs)
	if err != nil {
		return fail(err)
	}

	obs := buildGenerationObservability(bctx, ps.Metrics)

	model, closers, err := buildModelRuntime(bctx, obs.Upstream, closers)
	if err != nil {
		return fail(err)
	}
	if err := injectFault("model"); err != nil {
		return fail(err)
	}

	nowFn := time.Now
	if opts.Testing.Clock != nil {
		nowFn = opts.Testing.Clock
	}

	var exec *runtime.Executor
	var extState lipstate.Store
	if ps.sharedMutable != nil {
		extState = ps.sharedMutable.ExtensionState
	}
	ext := buildExtensionRuntime(bctx, nowFn, func() auxreq.ExecutorRunner { return exec }, ps.controlPlane, ps.policyObs, sg, extState)
	backendIDs, err := BackendStateIdentitiesFromConfig(cfg)
	if err != nil {
		return fail(err)
	}
	execRun, closers, err := buildExecutorRuntime(executorBuildInput{
		Bctx:               bctx,
		NowFn:              nowFn,
		Ext:                ext,
		Model:              model,
		Persistence:        ps.persistence,
		Security:           sec,
		Observability:      &obs,
		ControlPlane:       ps.controlPlane,
		UsageAuthority:     ps.UsageAuthority,
		Concurrency:        ps.concurrencyRT,
		SnapshotGeneration: ps.SnapshotGeneration,
		TerminalWork:       ps.terminalWorkRT,
		SharedMutable:      ps.sharedMutable,
		AccountingStores:   ps.accountingStores,
		Metering:           ps.meteringRT,
		BackendIdentities:  backendIDs,
	}, closers)
	if err != nil {
		return fail(err)
	}
	if err := AdaptOverlapSafeLifecycles(ledger, opts.FeatureLifecycles); err != nil {
		return fail(err)
	}
	if err := injectFault("prepare"); err != nil {
		return fail(err)
	}
	if err := ledger.Prepare(parent); err != nil {
		return fail(err)
	}
	if err := injectFault("activate"); err != nil {
		return fail(err)
	}
	if err := ledger.Activate(parent); err != nil {
		return fail(err)
	}

	exec = execRun.Exec
	var twReady func(context.Context) error
	if ps.terminalWorkRT != nil {
		twReady = ps.terminalWorkRT.checkReady
	}

	// Prefer ledger-backed closers so Built/Close stay idempotent with Rollback.
	if n := ledger.Len(); n > 0 {
		closers = ledger.LegacyClosers()
	}

	return &CandidateRuntime{
		Executor:               execRun.Exec,
		Store:                  ps.Continuity,
		Closers:                closers,
		Ledger:                 ledger,
		UpstreamHTTP:           obs.Upstream,
		RoutePrefixes:          model.RoutePrefixes,
		DecodeAdmission:        ps.DecodeAdmission,
		PluginRegistry:         ps.FactoryCatalog,
		DatabasePools:          ps.DatabasePools,
		EffectiveDefaultRoute:  execRun.EffectiveRoute,
		Metrics:                ps.Metrics,
		RuntimeSnapshot:        ext.Snap,
		HTTPAuthProviders:      sec.HTTPAuth,
		SecureSessionStore:     execRun.SecureSessionStore,
		AuthEventDispatcher:    sec.AuthEvents,
		CatalogRuntime:         execRun.CatalogRuntime,
		ModelRegistry:          model.Registry,
		ModelRegistryRuntime:   model.RegistryRuntime,
		TokenAccountingAdmin:   execRun.TokenAccountingAdmin,
		ControlPlaneQueries:    ps.controlPlane.queriesHandle(),
		ControlPlaneStatus:     ps.controlPlane.statusHandle(),
		ControlPlaneRetention:  ps.controlPlane.retentionHandle(),
		UsageAuthority:         ps.UsageAuthority,
		ConcurrencyAuthority:   ps.Concurrency,
		SnapshotGeneration:     ps.SnapshotGeneration,
		SnapshotController:     ps.SnapshotController,
		MeteringQuerier:        ps.MeteringQuerier,
		ReadinessReport:        execRun.ReadinessReport,
		SecretGuardInventory:   sg.Inventory,
		TerminalWorkProcessor:  ps.TerminalWorkProcessor,
		TerminalWorkRegistry:   ps.TerminalWorkRegistry,
		TerminalWorkQueries:    ps.TerminalWorkQueries,
		TerminalWorkMetrics:    ps.TerminalWorkMetrics,
		ProcessTracingShutdown: nil,
		terminalWorkReady:      twReady,
		terminalWorkRT:         ps.terminalWorkRT,
	}, nil
}

// mergeCandidateBuildOptions overlays generation-owned FeatureLifecycles and
// Extensions onto a shallow copy of process options without mutating Process.
// When overlay.ReplaceCandidateSurface is true, FeatureLifecycles and Extensions
// replace process values even when nil/empty (complete-generation compile).
// Legacy CompileCandidate callers leave ReplaceCandidateSurface false so nil
// overlay fields mean "no override".
func mergeCandidateBuildOptions(process *BuildOptions, overlay *BuildOptions) *BuildOptions {
	if process == nil {
		return overlay
	}
	if overlay == nil {
		return process
	}
	out := *process
	if overlay.ReplaceCandidateSurface {
		out.FeatureLifecycles = append([]lipplugin.Lifecycle(nil), overlay.FeatureLifecycles...)
		out.Extensions = overlay.Extensions
	} else {
		if overlay.FeatureLifecycles != nil {
			out.FeatureLifecycles = append([]lipplugin.Lifecycle(nil), overlay.FeatureLifecycles...)
		}
		if hasExtensionOverlay(overlay.Extensions) {
			out.Extensions = overlay.Extensions
		}
	}
	if overlay.WireModel != nil {
		out.WireModel = overlay.WireModel
	}
	// Always keep process factory catalog / infra / testing / production / auth.
	out.PluginRegistry = process.PluginRegistry
	out.Startup = process.Startup
	out.Infra = process.Infra
	out.Auth = process.Auth
	out.Policy = process.Policy
	out.Diagnostics = process.Diagnostics
	out.Testing = process.Testing
	out.Production = process.Production
	out.ReplaceCandidateSurface = false
	return &out
}

func hasExtensionOverlay(e ExtensionsOptions) bool {
	return len(e.SessionOpeners) > 0 ||
		len(e.WorkspaceResolvers) > 0 ||
		len(e.ToolCatalogFilters) > 0 ||
		len(e.ToolCallPolicies) > 0 ||
		len(e.ToolCallFinalizers) > 0 ||
		e.ToolCallFinalizationMaxArgsBytes > 0 ||
		len(e.RequestTransforms) > 0 ||
		len(e.PreRequestHandlers) > 0 ||
		len(e.RouteHintProviders) > 0 ||
		len(e.CompletionGates) > 0 ||
		len(e.AttemptTransforms) > 0 ||
		len(e.StreamObserverFactories) > 0 ||
		len(e.TrafficObservers) > 0 ||
		len(e.UsageObservers) > 0 ||
		len(e.RawCaptureSinks) > 0 ||
		len(e.TrafficRedactors) > 0 ||
		len(e.SecretGuards) > 0 ||
		e.SecretGuardEnvironment != nil ||
		e.SecretDecisionObserver != nil
}
