package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Build assembles continuity store, executor, and closers for the standard distribution.
// cfg must be non-nil, bus/log must be non-nil, and opts.PluginRegistry must be set.
// The returned [Built.RuntimeSnapshot] is shared by concurrent requests.
func Build(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, opts *BuildOptions) (built *Built, err error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	if opts == nil || opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: nil PluginRegistry")
	}
	if log == nil {
		return nil, fmt.Errorf("runtimebundle: nil logger")
	}
	if err := validateRequiredAuthorityEvidenceWiring(cfg); err != nil {
		return nil, err
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	parent := context.Background()
	if opts != nil && opts.Startup.StartupContext != nil {
		parent = opts.Startup.StartupContext
	}
	postgresPools := db.NewPoolRegistry(opts.Testing.PostgresPoolOpener)
	dualPlaneMigrator := newDualPlaneMigrator(cfg)
	buildSucceeded := false
	defer func() {
		if !buildSucceeded {
			err = joinCloseErr(err, postgresPools.Close(parent))
		}
	}()
	bctx := buildContext{Cfg: cfg, Bus: bus, Log: log, Opts: opts, Parent: parent, PostgresPools: postgresPools, DualPlaneMigrator: dualPlaneMigrator}
	// closers is the ordered disposal list for every resource Build opens. The
	// control-plane store is opened first (in buildControlPlaneRuntime), so its
	// closer is registered immediately and every later error path disposes it;
	// otherwise durable sqlite/postgres handles leak when a later step fails.
	closers := []func() error{}
	controlPlane, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: parent,
		Cfg:            cfg,
		Log:            log,
		Clock:          opts.Testing.Clock,
		StoreOverride:  opts.Testing.ControlPlaneStoreOverride,
	})
	if err != nil {
		return nil, err
	}
	if controlPlane != nil && controlPlane.closer != nil {
		closers = append(closers, controlPlane.closer)
	}
	// policyObs is assembled once and shared by the runtime snapshot and the
	// usage-authority evidence sink so authority decisions fan to the same
	// observer chain (operator observers + control-plane adapter) without
	// duplicating control-plane events.
	policyObs := assemblePolicyObserverChain(opts, controlPlane)
	usageAuthority, usageClosers, err := buildUsageAuthorityRuntime(parent, cfg, log, opts, controlPlane, policyObs, postgresPools, dualPlaneMigrator)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	if usageClosers != nil {
		closers = append(closers, usageClosers...)
	}
	var usageAuthorityHandle *authorityapp.Service
	if usageAuthority != nil {
		usageAuthorityHandle = usageAuthority.Service
	}
	concurrencyRT, concurrencyClosers, err := buildConcurrencyAuthorityRuntime(parent, cfg, log, opts.Testing, postgresPools, dualPlaneMigrator)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	if concurrencyClosers != nil {
		closers = append(closers, concurrencyClosers...)
	}
	var concurrencyHandle *concurrencyapp.Service
	if concurrencyRT != nil {
		concurrencyHandle = concurrencyRT.Service
	}
	reg := opts.PluginRegistry
	sec, err := buildSecurityRuntime(bctx, controlPlane)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	var regs []lipsdk.Registration
	if cfg != nil {
		regs = config.RegistrationsFromConfig(cfg)
	}
	sg, err := buildSecretGuardRuntime(cfg, log, opts, regs)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	obs := buildObservabilityRuntime(bctx)
	if usageAuthorityHandle != nil && obs.Bundle != nil {
		usageAuthorityHandle.SetStageMetrics(obs.Bundle.AuthorityStageSink())
	}

	model, closers, err := buildModelRuntime(bctx, obs.Upstream, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	persist, closers, err := buildPersistenceRuntime(bctx, controlPlane, obs.Bundle, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}

	nowFn := time.Now
	if opts.Testing.Clock != nil {
		nowFn = opts.Testing.Clock
	}
	twRT, twClosers, err := buildTerminalWorkFromProduction(opts.Production, opts.Testing.Clock, obs.Bundle)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	if twClosers != nil {
		closers = append(closers, twClosers...)
	}
	snapGen, snapCtrl := buildSnapshotGeneration(cfg, opts.Testing, opts.Production)
	if twRT != nil {
		twRT.snapshotPub = snapGen
	}
	var exec *runtime.Executor
	ext := buildExtensionRuntime(bctx, nowFn, func() auxreq.ExecutorRunner { return exec }, controlPlane, policyObs, sg)
	execRun, closers, err := buildExecutorRuntime(executorBuildInput{
		Bctx:               bctx,
		NowFn:              nowFn,
		Ext:                ext,
		Model:              model,
		Persistence:        persist,
		Security:           sec,
		Observability:      &obs,
		ControlPlane:       controlPlane,
		UsageAuthority:     usageAuthorityHandle,
		Concurrency:        concurrencyRT,
		SnapshotGeneration: snapGen,
		TerminalWork:       twRT,
	}, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	if err := postgresPools.PruneUnclaimed(); err != nil {
		return nil, withDisposedClosers(fmt.Errorf("runtimebundle: prune unclaimed postgres pools: %w", err), closers)
	}
	if postgresPools.Len() > 0 {
		closers = append(closers, func() error {
			return postgresPools.Close(context.Background())
		})
	}
	exec = execRun.Exec
	var twReady func(context.Context) error
	var twProc *terminalworkapp.Processor
	var twReg *terminalworkapp.Registry
	var twQueries *terminalworkapp.QueryService
	var twMetrics *terminalworkapp.MetricsObserver
	if twRT != nil {
		twProc, twReg, twReady = twRT.Processor, twRT.Registry, twRT.checkReady
		twQueries, twMetrics = twRT.Queries, twRT.Metrics
	}
	buildSucceeded = true
	return &Built{
		Executor:      execRun.Exec,
		Store:         persist.Store,
		Closers:       closers,
		UpstreamHTTP:  obs.Upstream,
		RoutePrefixes: model.RoutePrefixes,
		DecodeAdmission: decodeqos.New(
			cfg.Server.EffectiveMaxConcurrentDecodes(),
			cfg.Server.EffectiveMaxInflightDecodeBytes(),
		),
		PluginRegistry:        reg,
		EffectiveDefaultRoute: execRun.EffectiveRoute,
		Metrics:               obs.Bundle,
		RuntimeSnapshot:       ext.Snap,
		HTTPAuthProviders:     sec.HTTPAuth,
		SecureSessionStore:    execRun.SecureSessionStore,
		AuthEventDispatcher:   sec.AuthEvents,
		CatalogRuntime:        execRun.CatalogRuntime,
		ModelRegistry:         model.Registry,
		ModelRegistryRuntime:  model.RegistryRuntime,
		TokenAccountingAdmin:  execRun.TokenAccountingAdmin,
		ControlPlaneQueries:   controlPlane.queriesHandle(),
		ControlPlaneStatus:    controlPlane.statusHandle(),
		ControlPlaneRetention: controlPlane.retentionHandle(),
		UsageAuthority:        usageAuthorityHandle,
		ConcurrencyAuthority:  concurrencyHandle,
		SnapshotGeneration:    snapGen,
		SnapshotController:    snapCtrl,
		MeteringQuerier:       opts.Production.MeteringQuerier,
		ReadinessReport:       execRun.ReadinessReport,
		SecretGuardInventory:  sg.Inventory,
		TerminalWorkProcessor: twProc,
		TerminalWorkRegistry:  twReg,
		TerminalWorkQueries:   twQueries,
		TerminalWorkMetrics:   twMetrics,
		terminalWorkReady:     twReady,
		terminalWorkRT:        twRT,
	}, nil
}
