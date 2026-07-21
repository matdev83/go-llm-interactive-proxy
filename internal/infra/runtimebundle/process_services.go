package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// ProcessTracing holds process-owned tracing shutdown and outbound-propagation state.
// Constructed once at process startup (typically via tracing.Init in bootstrap).
type ProcessTracing struct {
	Shutdown func(context.Context) error
	Active   bool
}

// DeferredSharedMutableOwnership is retained for compatibility with task 2.3 tests.
// After task 2.4, OwnershipNote is empty: shared mutable continuity is hoisted.
type DeferredSharedMutableOwnership struct {
	// OwnershipNote is empty when all deferred items have been hoisted.
	OwnershipNote string
}

// ProcessServices owns process-scoped resources constructed once per process.
// Generation compilation receives a non-owning reference and must not Close it.
type ProcessServices struct {
	Logger                *slog.Logger
	FactoryCatalog        *pluginreg.Registry
	Tracing               ProcessTracing
	Metrics               *metrics.Bundle
	DatabasePools         *db.PoolRegistry
	Continuity            b2bua.Store
	SecureSessions        ssessionapp.Store
	DecodeAdmission       lipsdk.DecodeAdmission
	ALegLifecycle         *leglifecycle.Coordinator
	AffinityStore         affinity.Store
	CandidateHealth       policy.CandidateHealth
	ExtensionState        lipstate.Store
	AccountingLedger      accountingledger.Recorder
	MeteringRecorder      metering.Recorder
	UsageAuthority        *authorityapp.Service
	Concurrency           *concurrencyapp.Service
	SnapshotGeneration    *snapshotgen.Publisher
	SnapshotController    *SnapshotController
	MeteringQuerier       metering.Querier
	TerminalWorkProcessor *terminalworkapp.Processor
	TerminalWorkRegistry  *terminalworkapp.Registry
	TerminalWorkQueries   *terminalworkapp.QueryService
	TerminalWorkMetrics   *terminalworkapp.MetricsObserver

	// StoreCompatKeys holds typed topology identities for process-owned stores.
	StoreCompatKeys struct {
		Continuity       StoreCompatKey
		SecureSession    StoreCompatKey
		AccountingLedger StoreCompatKey
		MeteringJournal  StoreCompatKey
	}

	DeferredSharedMutable DeferredSharedMutableOwnership

	// Internal handles required by candidate compilation (non-API).
	persistence       *persistenceRuntime
	controlPlane      *controlPlaneRuntime
	usageRT           *usageAuthorityRuntime
	concurrencyRT     *concurrencyAuthorityRuntime
	terminalWorkRT    *terminalWorkRuntime
	dualPlaneMigrator *dualPlaneMigrator
	policyObs         policydecision.Observer
	sharedMutable     *sharedMutableRuntime
	accountingStores  *processAccountingStores
	meteringRT        *meteringRuntime
	cfg               *config.Config
	opts              *BuildOptions
	parent            context.Context

	closers   []func() error
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

// ProcessServicesInput configures [NewProcessServices].
type ProcessServicesInput struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Opts    *BuildOptions
	Tracing ProcessTracing
}

// NewProcessServices constructs process-owned stores, pools, metrics, terminal-work,
// limiters, and related dependencies once. Closers are registered immediately;
// partial failures dispose acquired resources in reverse order.
func NewProcessServices(ctx context.Context, in ProcessServicesInput) (*ProcessServices, error) {
	if in.Cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if in.Log == nil {
		return nil, fmt.Errorf("runtimebundle: nil logger")
	}
	if in.Opts == nil || in.Opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: nil PluginRegistry")
	}
	if err := validateRequiredAuthorityEvidenceWiring(in.Cfg); err != nil {
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
	if ps.Tracing.Shutdown == nil {
		ps.Tracing.Shutdown = func(context.Context) error { return nil }
	}
	ps.StoreCompatKeys.Continuity = StoreCompatKeyFromContinuity(in.Cfg.Continuity)
	ps.StoreCompatKeys.SecureSession = StoreCompatKeyFromSecureSession(in.Cfg.SecureSession)
	ps.StoreCompatKeys.AccountingLedger = StoreCompatKeyFromAccountingLedger(in.Cfg.Accounting)
	ps.StoreCompatKeys.MeteringJournal = StoreCompatKeyFromMeteringJournal(in.Cfg.Metering)

	postgresPools := db.NewPoolRegistry(in.Opts.Testing.PostgresPoolOpener)
	ps.DatabasePools = postgresPools
	ps.dualPlaneMigrator = newDualPlaneMigrator(in.Cfg)
	poolsClaimed := false
	defer func() {
		if !poolsClaimed && postgresPools != nil {
			_ = postgresPools.Close(parent)
		}
	}()

	register := func(c func() error) {
		if c != nil {
			ps.closers = append(ps.closers, c)
		}
	}
	fail := func(err error) (*ProcessServices, error) {
		return nil, withDisposedClosers(err, ps.closers)
	}

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
	if controlPlane != nil && controlPlane.closer != nil {
		register(controlPlane.closer)
	}

	policyObs := assemblePolicyObserverChain(in.Opts, controlPlane)
	ps.policyObs = policyObs

	usageAuthority, usageClosers, err := buildUsageAuthorityRuntime(parent, in.Cfg, in.Log, in.Opts, controlPlane, policyObs, postgresPools, ps.dualPlaneMigrator)
	for _, c := range usageClosers {
		register(c)
	}
	if err != nil {
		return fail(err)
	}
	ps.usageRT = usageAuthority
	if usageAuthority != nil {
		ps.UsageAuthority = usageAuthority.Service
	}

	concurrencyRT, concurrencyClosers, err := buildConcurrencyAuthorityRuntime(parent, in.Cfg, in.Log, in.Opts.Testing, postgresPools, ps.dualPlaneMigrator)
	for _, c := range concurrencyClosers {
		register(c)
	}
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
	persist, persistClosers, err := buildPersistenceRuntime(bctx, controlPlane, ps.Metrics, nil)
	for _, c := range persistClosers {
		register(c)
	}
	if err != nil {
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
	for _, c := range accountingClosers {
		register(c)
	}
	if err != nil {
		return fail(err)
	}
	ps.accountingStores = accountingStores
	if accountingStores != nil {
		ps.AccountingLedger = accountingStores.Ledger
	}

	meteringRT, meteringClosers, err := buildMeteringRuntime(parent, in.Cfg, nowFn, postgresPools, ps.dualPlaneMigrator)
	for _, c := range meteringClosers {
		register(c)
	}
	if err != nil {
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

	twRT, twClosers, err := buildTerminalWorkWithSetReconcile(parent, in.Opts.Production, nowFn, ps.Metrics, ps.Concurrency)
	for _, c := range twClosers {
		register(c)
	}
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

	snapGen, snapCtrl := buildSnapshotGeneration(in.Cfg, in.Opts.Testing, in.Opts.Production)
	ps.SnapshotGeneration = snapGen
	ps.SnapshotController = snapCtrl
	if twRT != nil {
		twRT.bindSnapshotPublisher(snapGen)
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

	// Tracing shutdown remains with the process host (BuildBootstrap.ShutdownTracing /
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

// ReplaceConfigForTest swaps the config pointer used by subsequent candidate
// compiles. Process-owned stores remain unchanged; only generation projections
// and compatibility views re-read cfg.
func (ps *ProcessServices) ReplaceConfigForTest(cfg *config.Config) {
	if ps == nil || cfg == nil {
		return
	}
	ps.cfg = cfg
}

// DisposeProcessClosersForTest exposes reverse-order disposal for unit tests.
func DisposeProcessClosersForTest(closers []func() error) error {
	return disposeClosers(closers)
}
