package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"sync"
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
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// GenerationCompileInput is the non-owning input to [CompileCandidate].
// Process must outlive the candidate; candidate Close must not Close Process.
type GenerationCompileInput struct {
	Process *ProcessServices
	Bus     *hooks.Bus
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

	// Closers contains generation-owned teardown only.
	Closers []func() error

	closeOnce sync.Once
	closeErr  error

	terminalWorkReady func(context.Context) error
	terminalWorkRT    *terminalWorkRuntime
}

// Close disposes generation-owned resources in reverse order.
// It does not close process services.
func (c *CandidateRuntime) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
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
	cfg := ps.cfg
	opts := ps.opts
	log := ps.Logger
	if cfg == nil || opts == nil || opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: ProcessServices missing config or PluginRegistry")
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
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

	bctx := buildContext{
		Cfg:               cfg,
		Bus:               bus,
		Log:               log,
		Opts:              opts,
		Parent:            parent,
		PostgresPools:     ps.DatabasePools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
	}

	var closers []func() error
	fail := func(err error) (*CandidateRuntime, error) {
		return nil, withDisposedClosers(err, closers)
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
	if err := ps.DatabasePools.PruneUnclaimed(); err != nil {
		return fail(fmt.Errorf("runtimebundle: prune unclaimed postgres pools: %w", err))
	}

	exec = execRun.Exec
	var twReady func(context.Context) error
	if ps.terminalWorkRT != nil {
		twReady = ps.terminalWorkRT.checkReady
	}

	return &CandidateRuntime{
		Executor:               execRun.Exec,
		Store:                  ps.Continuity,
		Closers:                closers,
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
