package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

type GenerationCompileInput struct {
	Process          *ProcessServices
	Bus              *hooks.Bus
	Candidate        *config.Config
	CandidateOpts    *BuildOptions
	Compose          HandlerComposer
	LiveFactoryKinds map[string]int
	FaultInject      CandidateFaultInject
}

func compileCandidate(ctx context.Context, in GenerationCompileInput) (*candidateAssembly, error) {
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

	if in.Candidate != nil && ps.cfg != nil && cfg != ps.cfg {
		if _, err := configreload.Classify(ps.cfg, cfg); err != nil {
			return nil, err
		}
	}
	if err := validateCandidateManifestOwnership(cfg, opts.PluginRegistry); err != nil {
		return nil, err
	}

	if err := ClassifyFeatureLifecycles(opts.FeatureLifecycles); err != nil {
		return nil, err
	}
	if err := ClassifyBackendOverlap(opts.PluginRegistry, cfg, in.LiveFactoryKinds); err != nil {
		return nil, err
	}

	bus := in.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	parent := opts.Startup.StartupContext
	if parent == nil {
		parent = ctx
	}
	if parent == nil {
		return nil, fmt.Errorf("runtimebundle: nil compile context")
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
		ExplicitCandidate: in.Candidate != nil,
	}

	fail := func(err error) error {
		rollErr := ledger.Rollback(parent)
		if rollErr != nil {
			return errors.Join(err, rollErr)
		}
		return err
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
		return nil, fail(err)
	}
	var regs []lipsdk.Registration
	if cfg != nil {
		regs = config.RegistrationsFromConfig(cfg)
	}
	sg, err := buildSecretGuardRuntime(cfg, log, opts, regs)
	if err != nil {
		return nil, fail(err)
	}

	obs := buildGenerationObservability(bctx, ps.Metrics)

	model, err := buildModelRuntime(bctx, obs.Upstream)
	if err != nil {
		return nil, fail(err)
	}
	if err := injectFault("model"); err != nil {
		return nil, fail(err)
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
		return nil, fail(err)
	}
	execRun, err := buildExecutorRuntime(executorBuildInput{
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
	})
	if err != nil {
		return nil, fail(err)
	}
	if err := AdaptOverlapSafeLifecycles(ledger, opts.FeatureLifecycles); err != nil {
		return nil, fail(err)
	}
	if err := injectFault("prepare"); err != nil {
		return nil, fail(err)
	}
	if err := ledger.Prepare(parent); err != nil {
		return nil, fail(err)
	}
	if err := injectFault("activate"); err != nil {
		return nil, fail(err)
	}
	if err := ledger.Activate(parent); err != nil {
		return nil, fail(err)
	}

	exec = execRun.Exec
	var twReady func(context.Context) error
	if ps.terminalWorkRT != nil {
		twReady = ps.terminalWorkRT.checkReady
	}

	return &candidateAssembly{
		execution: candidateExecutionGroup{
			executor:              execRun.Exec,
			routePrefixes:         model.RoutePrefixes,
			effectiveDefaultRoute: execRun.EffectiveRoute,
			decodeAdmission:       ps.DecodeAdmission,
			upstreamHTTP:          obs.Upstream,
		},
		security: candidateSecurityGroup{
			httpAuth:           sec.HTTPAuth,
			secureSessionStore: execRun.SecureSessionStore,
			authEvents:         sec.AuthEvents,
			runtimeSnapshot:    ext.Snap,
		},
		models: candidateModelGroup{
			catalog:         execRun.CatalogRuntime,
			registry:        model.Registry,
			registryRuntime: model.RegistryRuntime,
		},
		operations: candidateOperationsGroup{
			tokenAccountingAdmin: execRun.TokenAccountingAdmin,
			readinessReport:      execRun.ReadinessReport,
			secretGuardInventory: sg.Inventory,
			terminalProcessor:    ps.TerminalWorkProcessor,
			terminalRegistry:     ps.TerminalWorkRegistry,
			terminalQueries:      ps.TerminalWorkQueries,
			terminalMetrics:      ps.TerminalWorkMetrics,
		},
		process: candidateProcessRefs{
			store:                 ps.Continuity,
			pluginRegistry:        ps.FactoryCatalog,
			databasePools:         ps.DatabasePools,
			metrics:               ps.Metrics,
			controlPlaneQueries:   ps.controlPlane.queriesHandle(),
			controlPlaneStatus:    ps.controlPlane.statusHandle(),
			controlPlaneRetention: ps.controlPlane.retentionHandle(),
			usageAuthority:        ps.UsageAuthority,
			concurrencyAuthority:  ps.Concurrency,
			snapshotGeneration:    ps.SnapshotGeneration,
			snapshotController:    ps.SnapshotController,
			meteringQuerier:       ps.MeteringQuerier,
		},
		ledger:            ledger,
		terminalWorkReady: twReady,
		terminalWorkRT:    ps.terminalWorkRT,
	}, nil
}
