package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
)

// NewProcessServices constructs process-owned stores, pools, metrics, terminal-work,
// limiters, and related dependencies once. Closers are registered immediately;
// partial failures dispose acquired resources in reverse order.
func NewProcessServices(ctx context.Context, in ProcessServicesInput) (*ProcessServices, error) {
	releasePluginOwnership := func() {
		if in.PluginResourcePool != nil {
			_ = in.PluginResourcePool.Close()
			in.PluginResourcePool = nil
		}
		if in.PluginHost != nil {
			_ = in.PluginHost.Close()
			in.PluginHost = nil
		}
		for _, a := range in.PluginArtifacts {
			_ = a.Close()
		}
		in.PluginArtifacts = nil
		if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
			_ = removeAllRetry(dir, 8, 25*time.Millisecond)
			in.PluginStagingDir = ""
		}
	}
	if in.Cfg == nil {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if in.Log == nil {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, fmt.Errorf("runtimebundle: nil logger")
	}
	if in.Opts == nil || in.Opts.PluginRegistry == nil {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, fmt.Errorf("runtimebundle: nil PluginRegistry")
	}
	if err := validateRequiredAuthorityEvidenceWiring(in.Cfg); err != nil {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, err
	}

	parent := ctx
	if parent == nil {
		parent = context.Background()
	}
	if in.Opts.Startup.StartupContext != nil {
		parent = in.Opts.Startup.StartupContext
	}

	keepwarmPolicy, err := keepwarm.NewPolicyStore(keepwarm.DefaultMaxPolicyEntries)
	if err != nil {
		releaseProcessInputOwnership(&in, releasePluginOwnership)
		return nil, fmt.Errorf("runtimebundle: keep-warm policy store: %w", err)
	}
	ps := &ProcessServices{
		Logger:           in.Log,
		FactoryCatalog:   in.Opts.PluginRegistry,
		Tracing:          in.Tracing,
		KeepwarmPolicy:   keepwarmPolicy,
		KeepwarmRegistry: keepwarm.NewManagerRegistry(),
		cfg:              in.Cfg,
		opts:             in.Opts,
	}

	register := func(c func() error) {
		if c != nil {
			ps.closers = append(ps.closers, c)
		}
	}
	adoptBackgroundAuxAndDetector(parent, &in, ps, register)
	owner := &processResourceOwner{register: register}
	fail := func(err error) (*ProcessServices, error) {
		return nil, withDisposedClosers(err, ps.closers)
	}

	// Process-owned pool/host/artifacts/staging: register in acquisition order
	// (staging → artifacts → host → pool) so reverse disposal keeps the pool
	// ahead of Host.Close and artifact/staging teardown. Staging removal must
	// run only after VerifiedArtifact handles are closed so Windows can delete
	// the staged executables.
	if dir := strings.TrimSpace(in.PluginStagingDir); dir != "" {
		stagingDir := dir
		register(func() error {
			return removeAllRetry(stagingDir, 8, 25*time.Millisecond)
		})
		in.PluginStagingDir = ""
	}
	if arts := in.PluginArtifacts; len(arts) > 0 {
		artifacts := make([]*trust.VerifiedArtifact, 0, len(arts))
		artifacts = append(artifacts, arts...)
		register(func() error {
			var out error
			for _, a := range artifacts {
				out = errors.Join(out, a.Close())
			}
			return out
		})
		in.PluginArtifacts = nil
	}
	if in.PluginHost != nil {
		pluginHost := in.PluginHost
		register(pluginHost.Close)
		in.PluginHost = nil
	}
	if in.PluginResourcePool != nil {
		resourcePool := in.PluginResourcePool
		register(resourcePool.Close)
		in.PluginResourcePool = nil
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
		StartupContext:    parent,
		Cfg:               in.Cfg,
		Log:               in.Log,
		Clock:             in.Opts.Testing.Clock,
		StoreOverride:     in.Opts.Testing.ControlPlaneStoreOverride,
		PostgresPools:     postgresPools,
		DualPlaneMigrator: ps.dualPlaneMigrator,
	})
	if err != nil {
		return fail(err)
	}
	ps.controlPlane = controlPlane
	if controlPlane != nil {
		register(controlPlane.closer)
	}
	if err := configureProcessBilling(owner, parent, in.Cfg, in.Opts); err != nil {
		return fail(err)
	}
	policyObs := assemblePolicyObserverChain(in.Opts, controlPlane)
	ps.policyObs = policyObs

	usageAuthority, err := buildUsageAuthorityRuntime(owner, parent, in.Cfg, in.Log, in.Opts, controlPlane, policyObs, postgresPools, ps.dualPlaneMigrator)
	if err != nil {
		return fail(err)
	}
	ps.usageRT = usageAuthority
	if usageAuthority != nil {
		ps.UsageAuthority = usageAuthority.Service
	}

	concurrencyRT, err := buildConcurrencyAuthorityRuntime(owner, parent, in.Cfg, in.Opts.Testing, postgresPools, ps.dualPlaneMigrator)
	if err != nil {
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
	persist, err := buildPersistenceRuntime(owner, bctx, controlPlane, ps.Metrics)
	if err != nil {
		return fail(err)
	}
	ps.persistence = persist
	if persist != nil {
		ps.Continuity = persist.Store
		ps.RouteOverrideStore = persist.OverrideStore
		if persist.SecureSession != nil {
			ps.SecureSessions = persist.SecureSession.appStore
		}
	}
	if in.Cfg.Routing.OverrideAdmin.Enabled && ps.RouteOverrideStore == nil {
		return fail(fmt.Errorf("runtimebundle: routing.override_admin.enabled requires a continuity store that implements routeoverride.Store"))
	}

	nowFn := in.Opts.Testing.Clock
	if nowFn == nil {
		nowFn = time.Now
	}

	accountingStores, err := buildProcessAccountingStores(parent, in.Cfg, nowFn)
	if err != nil {
		return fail(err)
	}
	ps.accountingStores = accountingStores

	meteringRT, err := buildMeteringRuntime(owner, parent, in.Cfg, nowFn, postgresPools, ps.dualPlaneMigrator)
	if err != nil {
		return fail(err)
	}
	ps.meteringRT = meteringRT
	if meteringRT != nil {
		ps.MeteringRecorder = meteringRT.Recorder
	}

	shared := buildSharedMutableRuntime(in.Cfg, nowFn)
	if err := bindSharedMutableProcessServices(parent, ps, shared); err != nil {
		return fail(fmt.Errorf("runtimebundle: branch coordinator: %w", err))
	}

	// Snapshot binder before terminal workers so IntentService/reconciler receive
	// executable pending ownership without a post-start setter race.
	snapGen, snapCtrl := buildSnapshotGeneration(in.Cfg, in.Opts.Testing, in.Opts.Production)
	ps.SnapshotGeneration = snapGen
	ps.SnapshotController = snapCtrl

	twRT, err := buildTerminalWorkWithSetReconcile(owner, parent, in.Opts.Production, nowFn, ps.Metrics, ps.Concurrency, snapGen)
	if err != nil {
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
