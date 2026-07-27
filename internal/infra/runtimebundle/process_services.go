package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
)

// NewProcessServices constructs process-owned stores, pools, metrics, terminal-work,
// limiters, and related dependencies once. Closers are registered immediately;
// partial failures dispose acquired resources in reverse order.
func NewProcessServices(ctx context.Context, in ProcessServicesInput) (*ProcessServices, error) {
	releasePluginOwnership := func() {
		if in.PluginHost != nil {
			_ = in.PluginHost.Close()
			in.PluginHost = nil
		}
		if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
			_ = os.RemoveAll(dir)
			in.PluginStagingDir = ""
		}
	}
	if in.Cfg == nil {
		releasePluginOwnership()
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if in.Log == nil {
		releasePluginOwnership()
		return nil, fmt.Errorf("runtimebundle: nil logger")
	}
	if in.Opts == nil || in.Opts.PluginRegistry == nil {
		releasePluginOwnership()
		return nil, fmt.Errorf("runtimebundle: nil PluginRegistry")
	}
	if err := validateRequiredAuthorityEvidenceWiring(in.Cfg); err != nil {
		releasePluginOwnership()
		return nil, err
	}

	parent := ctx
	if parent == nil {
		parent = context.Background()
	}
	if in.Opts.Startup.StartupContext != nil {
		parent = in.Opts.Startup.StartupContext
	}

	ps := &ProcessServices{
		Logger:         in.Log,
		FactoryCatalog: in.Opts.PluginRegistry,
		Tracing:        in.Tracing,
		cfg:            in.Cfg,
		opts:           in.Opts,
		parent:         parent,
	}

	register := func(c func() error) {
		if c != nil {
			ps.closers = append(ps.closers, c)
		}
	}
	fail := func(err error) (*ProcessServices, error) {
		return nil, withDisposedClosers(err, ps.closers)
	}
	regStep := func(closers []func() error, err error) error {
		for _, c := range closers {
			register(c)
		}
		return err
	}

	// Process-owned host/staging: register first → dispose last (… → host → staging).
	if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
		stagingDir := dir
		register(func() error {
			_ = os.RemoveAll(stagingDir)
			return nil
		})
		in.PluginStagingDir = ""
	}
	if in.PluginHost != nil {
		pluginHost := in.PluginHost
		register(pluginHost.Close)
		in.PluginHost = nil
	}

	// Discovery/trust catalog is process-owned and startup-fixed (req 7.3, 8.7).
	ps.FactoryCatalog.FreezeDiscovery()
	if ps.Tracing.Shutdown == nil {
		ps.Tracing.Shutdown = func(context.Context) error { return nil }
	}

	postgresPools := db.NewPoolRegistry(in.Opts.Testing.PostgresPoolOpener)
	ps.DatabasePools = postgresPools
	ps.dualPlaneMigrator = newDualPlaneMigrator(in.Cfg)
	poolsClaimed := false
	defer func() {
		if !poolsClaimed && postgresPools != nil {
			_ = postgresPools.Close(parent)
		}
	}()

	controlPlane, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: parent,
		Cfg:            in.Cfg,
		Log:            in.Log,
		Clock:          in.Opts.Testing.Clock,
		StoreOverride:  in.Opts.Testing.ControlPlaneStoreOverride,
	})
	if err != nil {
		return fail(err)
	}
	ps.controlPlane = controlPlane
	if controlPlane != nil {
		register(controlPlane.closer)
	}

	policyObs := assemblePolicyObserverChain(in.Opts, controlPlane)
	ps.policyObs = policyObs

	usageAuthority, usageClosers, err := buildUsageAuthorityRuntime(parent, in.Cfg, in.Log, in.Opts, controlPlane, policyObs, postgresPools, ps.dualPlaneMigrator)
	if err := regStep(usageClosers, err); err != nil {
		return fail(err)
	}
	ps.usageRT = usageAuthority
	if usageAuthority != nil {
		ps.UsageAuthority = usageAuthority.Service
	}

	concurrencyRT, concurrencyClosers, err := buildConcurrencyAuthorityRuntime(parent, in.Cfg, in.Log, in.Opts.Testing, postgresPools, ps.dualPlaneMigrator)
	if err := regStep(concurrencyClosers, err); err != nil {
		return fail(err)
	}
	ps.concurrencyRT = concurrencyRT
	if concurrencyRT != nil {
		ps.Concurrency = concurrencyRT.Service
	}

	ps.Metrics = buildProcessMetricsBundle(in.Cfg, postgresPools.Stats)
	if ps.UsageAuthority != nil && ps.Metrics != nil {
		ps.UsageAuthority.SetStageMetrics(ps.Metrics.AuthorityStageSink())
	}

	bctx := buildContext{
		Cfg:               in.Cfg,
		Bus:               nil, // generation-owned; unused by persistence
		Log:               in.Log,
		Opts:              in.Opts,
		Parent:            parent,
		PostgresPools:     postgresPools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
	}
	persist, persistClosers, err := buildPersistenceRuntime(bctx, controlPlane, ps.Metrics, nil)
	if err := regStep(persistClosers, err); err != nil {
		return fail(err)
	}
	ps.persistence = persist
	if persist != nil {
		ps.Continuity = persist.Store
		if persist.SecureSession != nil {
			ps.SecureSessions = persist.SecureSession.appStore
		}
	}

	nowFn := in.Opts.Testing.Clock
	if nowFn == nil {
		nowFn = time.Now
	}

	accountingStores, accountingClosers, err := buildProcessAccountingStores(parent, in.Cfg, nowFn)
	if err := regStep(accountingClosers, err); err != nil {
		return fail(err)
	}
	ps.accountingStores = accountingStores
	if accountingStores != nil {
		ps.AccountingLedger = accountingStores.Ledger
	}

	meteringRT, meteringClosers, err := buildMeteringRuntime(parent, in.Cfg, nowFn, postgresPools, ps.dualPlaneMigrator)
	if err := regStep(meteringClosers, err); err != nil {
		return fail(err)
	}
	ps.meteringRT = meteringRT
	if meteringRT != nil {
		ps.MeteringRecorder = meteringRT.Recorder
	}

	shared := buildSharedMutableRuntime(in.Cfg, nowFn)
	ps.sharedMutable = shared
	ps.ALegLifecycle = shared.ALegLifecycle
	ps.ExtensionState = shared.ExtensionState
	ps.AffinityStore = &processAffinityHandle{reg: shared.affinity}
	ps.CandidateHealth = shared.underlyingHealth

	// Snapshot binder before terminal workers so IntentService/reconciler receive
	// executable pending ownership without a post-start setter race.
	snapGen, snapCtrl := buildSnapshotGeneration(in.Cfg, in.Opts.Testing, in.Opts.Production)
	ps.SnapshotGeneration = snapGen
	ps.SnapshotController = snapCtrl

	twRT, twClosers, err := buildTerminalWorkWithSetReconcile(parent, in.Opts.Production, nowFn, ps.Metrics, ps.Concurrency, snapGen)
	if err := regStep(twClosers, err); err != nil {
		return fail(err)
	}
	ps.terminalWorkRT = twRT
	if twRT != nil {
		ps.TerminalWorkProcessor = twRT.Processor
		ps.TerminalWorkRegistry = twRT.Registry
		ps.TerminalWorkQueries = twRT.Queries
		ps.TerminalWorkMetrics = twRT.Metrics
	}

	ps.DecodeAdmission = decodeqos.New(
		in.Cfg.Server.EffectiveMaxConcurrentDecodes(),
		in.Cfg.Server.EffectiveMaxInflightDecodeBytes(),
	)
	ps.MeteringQuerier = in.Opts.Production.MeteringQuerier

	// One-time prune after all process-owned Open/Claim paths complete. Candidate
	// compilation must remain read-only with respect to the process pool registry.
	if err := postgresPools.PruneUnclaimed(); err != nil {
		return fail(fmt.Errorf("runtimebundle: prune unclaimed postgres pools: %w", err))
	}

	// ProcessServices owns the pool registry on every successful return. Close
	// disposes it after dependent process resources; empty registries are cheap.
	poolsClaimed = true

	// Tracing shutdown remains with the process host (Host.ShutdownTracing /
	// stdhttp). ProcessServices retains the non-owning handle for identity reuse only.

	return ps, nil
}

// Close disposes process-owned resources in reverse acquisition order.
// It is idempotent.
func (ps *ProcessServices) Close() error {
	if ps == nil {
		return nil
	}
	ps.closeOnce.Do(func() {
		ps.closeErr = disposeClosers(ps.closers)
		if ps.DatabasePools != nil {
			if err := ps.DatabasePools.Close(context.Background()); err != nil {
				ps.closeErr = errors.Join(ps.closeErr, fmt.Errorf("runtimebundle: close database pools: %w", err))
			}
		}
		ps.closed.Store(true)
	})
	return ps.closeErr
}

// Closed reports whether [ProcessServices.Close] has completed.
func (ps *ProcessServices) Closed() bool {
	if ps == nil {
		return true
	}
	return ps.closed.Load()
}
