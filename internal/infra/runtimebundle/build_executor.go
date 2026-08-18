package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	corecp "github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingspool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// executorRuntime holds the assembled executor and the values [Build] needs from
// the executor unit: the effective default route, the secure-session store (after
// the optional BuildOptions override), the model-catalog runtime attached to the
// executor, and the token-accounting admin service.
type executorRuntime struct {
	Exec                 *runtime.Executor
	EffectiveRoute       string
	SecureSessionStore   ssessionapp.Store
	CatalogRuntime       *modelcatalog.CatalogRuntime
	TokenAccountingAdmin *accountingapp.Service
	ReadinessReport      *corecp.ReadinessReportService
	// Production is the candidate-local effective production wiring. It is
	// returned instead of mutating shared BuildOptions during concurrent builds.
	Production ProductionOptions
}

// executorBuildInput groups the upstream unit results consumed by
// [buildExecutorRuntime].
type executorBuildInput struct {
	Bctx               buildContext
	Ledger             *ResourceLedger
	NowFn              func() time.Time
	Ext                *extensionRuntime
	Model              *modelRuntime
	Persistence        *persistenceRuntime
	Security           *securityRuntime
	Observability      *observabilityRuntime
	ControlPlane       *controlPlaneRuntime
	UsageAuthority     *authorityapp.Service
	Concurrency        *concurrencyAuthorityRuntime
	SnapshotGeneration *snapshotgen.Publisher
	TerminalWork       *terminalWorkRuntime
	SharedMutable      *sharedMutableRuntime
	AccountingStores   *processAccountingStores
	Metering           *meteringRuntime
	BackendIdentities  map[string]BackendStateIdentity
}

// buildExecutorRuntime runs the executor-assembly sequence: routing resolution,
// capability map, RNG seed, stream-recovery config, token-accounting runtime,
// interleaved-thinking config, price catalog, auth/session/metrics/secure-session
// wiring, synthetic-local-principal flag, and model-catalog resolver attachment.
// All values are computed before [runtime.NewExecutor] so NewExecutor is a strong
// invariant boundary: no post-construction field mutation occurs.
// Accounting ledger and metering stores are process-owned; this bind only
// attaches generation backends to those shared identities.
func buildExecutorRuntime(in executorBuildInput) (*executorRuntime, error) {
	bctx := in.Bctx
	cfg, log, opts := bctx.Cfg, bctx.Log, bctx.Opts

	effectiveRoute, defBE, aliasResolver, err := resolveRouting(cfg, opts.WireModel)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	execResolver := buildBackendExecutionResolver(cfg, opts.PluginRegistry)
	execPolicy := cfg.Routing.EffectiveExecutionCompositionPolicy()
	if bctx.ExplicitCandidate {
		if err := validateRouteSelectorsAgainstBackends(cfg, effectiveRoute, cfg.ModelAliases, cfg.Plugins.Backends, execResolver, execPolicy); err != nil {
			return nil, err
		}
	}
	capMap := make(capabilities.MapResolver, len(in.Model.Backends))
	for id, be := range in.Model.Backends {
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, be, call, cand)
		}
	}

	var seed int64
	if err := binary.Read(crand.Reader, binary.LittleEndian, &seed); err != nil {
		seed = time.Now().UnixNano()
	}

	streamRecovery, err := streamRecoveryConfigFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	tokenAccounting, err := bindTokenAccountingRuntime(in.AccountingStores, cfg, in.Model.Backends)
	if err != nil {
		return nil, err
	}

	meteringRT := in.Metering
	var prod ProductionOptions
	if in.Bctx.Opts != nil {
		prod = in.Bctx.Opts.Production
		if cfg.Accounting.Billing.Authoritative {
			prod.BillingAuthoritative = true
		}
		if path := strings.TrimSpace(cfg.Accounting.Billing.ReportsPath); path != "" && strings.TrimSpace(prod.BillingReportsPath) == "" {
			prod.BillingReportsPath = path
		}
		if prod.BillingAuthoritative {
			if err := requireAuthoritativeBillingPorts(prod, in.Ledger); err != nil {
				return nil, err
			}
			if prod.BillingTerminalUsageSink == nil && strings.TrimSpace(cfg.Accounting.Billing.SpoolPath) != "" {
				spoolPath := strings.TrimSpace(cfg.Accounting.Billing.SpoolPath)
				if err := requireStableSpoolPath(spoolPath); err != nil {
					return nil, err
				}
				central, ok := prod.BillingStore.(billing.TerminalUsageSink)
				if !ok {
					return nil, fmt.Errorf("%w: terminal spool central sink", ErrAuthoritativeBillingRequired)
				}
				spool, err := billingspool.Open(bctx.Parent, billingspool.Config{Path: spoolPath}, central)
				if err != nil {
					return nil, fmt.Errorf("runtimebundle: open billing terminal spool: %w", err)
				}
				prod.BillingTerminalUsageSink = spool
			}
			if owned, ok := prod.BillingTerminalUsageSink.(interface {
				Start(context.Context) error
				Stop(context.Context) error
			}); ok {
				in.Ledger.AddAction("billing-terminal-spool", PhasePublish, owned.Start, owned.Stop)
			}
			if closer, ok := prod.BillingTerminalUsageSink.(interface{ Close() error }); ok {
				in.Ledger.AddClose("billing-terminal-spool", PhaseQuiesce, closer.Close)
			}
			if prod.BillingTerminalUsageSink == nil {
				return nil, fmt.Errorf("%w: TerminalUsageSink (configure the process-local spool)", ErrAuthoritativeBillingRequired)
			}
			prod.BillingReports = prod.BillingStore
			if prod.BillingExposureAdmission != nil {
				callUsage, usageOK := prod.BillingStore.(billing.CallUsageStore)
				callSettlement, settlementOK := prod.BillingStore.(billing.CallSettlementStore)
				callResolver := prod.BillingCallRatingResolver
				if !usageOK || !settlementOK || callResolver == nil {
					return nil, ErrAuthoritativeBillingRequired
				}
				callWorker, err := billing.NewCallPostUsageWorker(callUsage, callSettlement, callResolver, prod.BillingPostTurnBatchSize)
				if err != nil {
					return nil, fmt.Errorf("runtimebundle: complete-call billing worker: %w", err)
				}
				in.Ledger.AddAction("billing-complete-call-worker", PhasePublish, func(context.Context) error { return callWorker.Start(context.Background()) }, callWorker.Stop)
				in.Ledger.AddClose("billing-complete-call-worker", PhaseQuiesce, func() error { return callWorker.Stop(context.Background()) })

				providerWork, providerWorkOK := prod.BillingStore.(billing.ProviderCostWorkReader)
				providerStore, providerStoreOK := prod.BillingStore.(billing.ProviderCostStore)
				if !providerWorkOK {
					return nil, fmt.Errorf("%w: ProviderCostWorkReader", ErrAuthoritativeBillingRequired)
				}
				if !providerStoreOK {
					return nil, fmt.Errorf("%w: ProviderCostStore", ErrAuthoritativeBillingRequired)
				}
				if prod.BillingProviderCostResolver == nil {
					return nil, fmt.Errorf("%w: BillingProviderCostResolver", ErrAuthoritativeBillingRequired)
				}
				providerWorker, err := billing.NewCallProviderCostWorker(providerWork, providerStore, prod.BillingProviderCostResolver, prod.BillingPostTurnBatchSize)
				if err != nil {
					return nil, fmt.Errorf("runtimebundle: provider-cost worker: %w", err)
				}
				in.Ledger.AddAction("billing-provider-cost-worker", PhasePublish, func(context.Context) error { return providerWorker.Start(context.Background()) }, providerWorker.Stop)
				in.Ledger.AddClose("billing-provider-cost-worker", PhaseQuiesce, func() error { return providerWorker.Stop(context.Background()) })
			}
		}
		if prod.MeteringRecorder != nil {
			meteringRT = &meteringRuntime{Recorder: prod.MeteringRecorder, StoreBacking: "injected"}
		}
	} else if cfg.Accounting.Billing.Authoritative {
		return nil, ErrAuthoritativeBillingRequired
	}

	// Compute interleaved-thinking config before construction.
	interleaved, err := interleavedExecutorRuntime(cfg)
	if err != nil {
		return nil, err
	}

	// Compute accounting runtime fields.
	accountingRT := runtime.AccountingRuntime{}
	if tokenAccounting != nil {
		accountingRT.Preflight = tokenAccounting.Preflight
		accountingRT.StreamUsage = tokenAccounting.StreamUsage
		accountingRT.TokenAccountingObservability = tokenAccounting.Observability
		// Request metering admission needs the same configured counter regardless
		// of whether the optional public/admin count endpoint is mounted.
		accountingRT.AdminCountService = tokenAccounting.Counter
	}
	if meteringRT != nil {
		accountingRT.MeteringRecorder = meteringRT.Recorder
	}
	if in.UsageAuthority != nil {
		accountingRT.UsageAuthority = in.UsageAuthority
		cleanupTimeout, err := cfg.Accounting.Authority.CleanupTimeoutDuration()
		if err != nil {
			return nil, err
		}
		accountingRT.UsageAuthorityCleanupTimeout = cleanupTimeout
	}
	attachConcurrencyToAccounting(&accountingRT, in.Concurrency)
	if err := attachCompatibleAdmission(&prod, cfg); err != nil {
		return nil, err
	}
	if accountingRT.UsageAuthority != nil || accountingRT.ConcurrencyProvider != nil || prod.HasAuthorityOverrides() {
		if err := attachAuthorityCoordinators(&accountingRT, prod); err != nil {
			return nil, err
		}
	}
	accountingRT.SnapshotGeneration = in.SnapshotGeneration
	if in.TerminalWork != nil {
		accountingRT.TerminalWork = in.TerminalWork.Intents
	}
	// Compute security runtime from secure-session + auth.
	securityRT := securityRuntimeFromSecureSession(in.Persistence.SecureSession)
	securityRT.AuthEvents = in.Security.AuthEvents
	securityRT.SessionAuditPolicy = in.Security.SAP
	ssStore := strings.TrimSpace(cfg.SecureSession.Store)
	if ssStore == "" {
		ssStore = "memory"
	}
	securityRT.SyntheticLocalPrincipal = cfg.SingleUserLocalMode() && strings.EqualFold(ssStore, "memory")

	policyDiagEnabled := false
	maxArgsBytes := 0
	if opts != nil {
		policyDiagEnabled = opts.Policy.PolicyDiagnosticsEnabled
		maxArgsBytes = opts.Extensions.ToolCallFinalizationMaxArgsBytes
	}

	// Compute observability runtime with metrics sinks.
	obsRT := runtime.ObservabilityRuntime{
		Log:                      log,
		PolicyDiagnosticsEnabled: policyDiagEnabled,
	}
	if in.Observability.Bundle != nil {
		obsRT.Metrics = in.Observability.Bundle.ExecutorSink()
		obsRT.ExtensionMetrics = in.Observability.Bundle.ExtensionStageSink()
		obsRT.SecretGuardDecisionMetrics = in.Observability.Bundle.SecretGuardDecisionSink()
		securityRT.SecureSessionMetrics = in.Observability.Bundle.SecureSessionMetricsSink()
		if tokenAccounting != nil && tokenAccounting.Observability != nil {
			tokenAccounting.Observability.SetSink(in.Observability.Bundle.TokenAccountingObservabilitySink())
		}
	}

	// Build routing runtime; model catalog resolvers are attached before construction.
	var affStore affinity.Store
	var candHealth policy.CandidateHealth
	aLeg := (*leglifecycle.Coordinator)(nil)
	if in.SharedMutable != nil {
		affStore, candHealth = in.SharedMutable.candidateRoutingViews(in.BackendIdentities, cfg, in.NowFn)
		aLeg = in.SharedMutable.ALegLifecycle
	}
	var overrideReader routeoverride.Reader
	if in.Persistence != nil && in.Persistence.OverrideStore != nil {
		overrideReader = in.Persistence.OverrideStore
	}
	routingRT := runtime.RoutingRuntime{
		MaxAttempts:                cfg.Routing.MaxAttempts,
		DefaultBackend:             defBE,
		SelectorAliases:            aliasResolver,
		CapsResolver:               capMap,
		CandidateHealth:            candHealth,
		RouteObserver:              routeObserverFor(log),
		AffinityStore:              affStore,
		AffinityMissingIdentity:    affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		TransportFallbackPolicy:    config.EffectiveTransportFallbackPolicy(cfg),
		RouteOverrideReader:        overrideReader,
		ExecutionCompositionPolicy: execPolicy,
		BackendExecutionResolver:   execResolver,
	}
	routingRT, catalogRuntime := attachModelCatalog(routingRT, in.Model.StartedCatalog, cfg)

	// Construct executor with all fields set — no post-construction mutation.
	exec := runtime.NewExecutor(runtime.ExecutorConfig{
		Core: runtime.CoreRuntime{
			Store:                in.Persistence.Store,
			Backends:             in.Model.Backends,
			ALegLifecycle:        aLeg,
			Rand:                 routing.NewSeededRng(seed),
			Now:                  in.NowFn,
			MaxPendingWireEvents: cfg.Server.EffectiveMaxPendingWireEvents(),
			StreamRecovery:       streamRecovery,
		},
		Billing: runtime.BillingRuntime{
			BillingCreditGate:        prod.BillingCreditGate,
			BillingExposureAdmission: prod.BillingExposureAdmission,
			BillingLegObserver:       billingLegObserverFor(log),
			TerminalUsageSink:        prod.BillingTerminalUsageSink,
			BillingIdentity:          prod.BillingIdentity,
			BillingAuthoritative:     prod.BillingAuthoritative,
		},

		Routing:       routingRT,
		Security:      securityRT,
		Accounting:    accountingRT,
		Observability: obsRT,
		Extension: runtime.ExtensionRuntime{
			Bus:                              bctx.Bus,
			RuntimeSnapshot:                  in.Ext.Snap,
			ToolCallFinalizationMaxArgsBytes: maxArgsBytes,
		},
		Interleaved: interleaved,
	})

	secureSessionStore := in.Persistence.SecureSession.appStore
	if opts.Diagnostics.SecureSessionStore != nil {
		secureSessionStore = opts.Diagnostics.SecureSessionStore
	}
	var concurrencySvc *concurrencyapp.Service
	if in.Concurrency != nil {
		concurrencySvc = in.Concurrency.Service
	}
	var tokenAccountingAdminSvc *accountingapp.Service
	if tokenAccounting != nil {
		tokenAccountingAdminSvc = tokenAccounting.Admin
	}
	readiness := buildReadinessReportService(readinessReportBuildInput{
		Cfg:                cfg,
		ControlPlaneStatus: in.ControlPlane.statusHandle(),
		UsageAuthority:     in.UsageAuthority,
		Concurrency:        concurrencySvc,
		Metering:           meteringRT,
		SnapshotGeneration: in.SnapshotGeneration,
		Executor:           exec,
		Production:         prod,
		TerminalWork:       in.TerminalWork,
	})
	return &executorRuntime{
		Exec:                 exec,
		EffectiveRoute:       effectiveRoute,
		SecureSessionStore:   secureSessionStore,
		CatalogRuntime:       catalogRuntime,
		TokenAccountingAdmin: tokenAccountingAdminSvc,
		ReadinessReport:      readiness,
		Production:           prod,
	}, nil
}

func requireAuthoritativeBillingPorts(prod ProductionOptions, ledger *ResourceLedger) error {
	switch {
	case prod.BillingStore == nil:
		return fmt.Errorf("%w: BillingStore", ErrAuthoritativeBillingRequired)
	case prod.BillingExposureAdmission == nil:
		return fmt.Errorf("%w: BillingExposureAdmission", ErrAuthoritativeBillingRequired)
	case prod.BillingCreditGate == nil:
		return fmt.Errorf("%w: BillingCreditGate", ErrAuthoritativeBillingRequired)
	case prod.BillingTerminalUsageSink == nil && strings.TrimSpace(prod.BillingReportsPath) == "":
		return fmt.Errorf("%w: TerminalUsageSink", ErrAuthoritativeBillingRequired)
	case prod.BillingIdentity.AccountID == nil:
		return fmt.Errorf("%w: BillingIdentity.AccountID", ErrAuthoritativeBillingRequired)
	case ledger == nil:
		return fmt.Errorf("%w: ResourceLedger", ErrAuthoritativeBillingRequired)
	}
	return nil
}

// requireStableSpoolPath rejects volatile OS temp directories for the durable
// monetary terminal spool. The check is a best-effort production guardrail:
// TMPDIR can be overridden and Windows has no POSIX temp semantics, so
// deployment must still pin a real state directory.
func requireStableSpoolPath(path string) error {
	clean := filepath.Clean(path)
	for _, candidate := range []string{os.TempDir(), "/tmp", "/var/tmp", "/dev/shm"} {
		if candidate == "" {
			continue
		}
		cand := filepath.Clean(candidate)
		if clean == cand || strings.HasPrefix(clean, cand+string(filepath.Separator)) {
			return fmt.Errorf("runtimebundle: billing spool path %q is inside volatile temp directory %q; use a stable state directory", path, candidate)
		}
	}
	return nil
}

func streamRecoveryConfigFromConfig(cfg *config.Config) (streamrecovery.Config, error) {
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{})
	if err != nil {
		return streamrecovery.Config{}, fmt.Errorf("runtimebundle: stream recovery config: %w", err)
	}
	return streamrecovery.Config{
		Enabled:     eff.Enabled,
		IdleTimeout: eff.IdleTimeout,
		GracePeriod: eff.GracePeriod,
		EmitWarning: eff.EmitWarning,
	}, nil
}

// validateRouteSelectorsAgainstBackends fails compile when explicit default_route or alias
// replacements reference backends absent from the candidate backend row set (req 9.2) or
// violate execution composition safety under the active policy.
func validateRouteSelectorsAgainstBackends(
	cfg *config.Config,
	effectiveRoute string,
	aliases []config.ModelAliasConfig,
	backendRows []config.PluginConfig,
	execResolver routing.BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) error {
	configured := make(map[string]struct{}, len(backendRows))
	for _, p := range backendRows {
		if id := p.InstanceID(); id != "" {
			configured[id] = struct{}{}
		}
	}
	if strings.TrimSpace(cfg.Routing.DefaultRoute) != "" {
		if err := validateSelectorTextAgainstBackends("routing default_route", effectiveRoute, configured); err != nil {
			return err
		}
		if sel, err := routing.Parse(effectiveRoute); err == nil {
			if err := routing.ValidateExecutionComposition(sel, execResolver, policy); err != nil {
				return fmt.Errorf("runtimebundle: routing default_route: %w", err)
			}
		}
	}
	for i, a := range aliases {
		repl := strings.TrimSpace(a.Replacement)
		if repl == "" {
			continue
		}
		label := fmt.Sprintf("model_aliases[%d].replacement", i)
		if err := validateSelectorTextAgainstBackends(label, repl, configured); err != nil {
			return err
		}
		if sel, err := routing.Parse(repl); err == nil {
			if err := routing.ValidateExecutionComposition(sel, execResolver, policy); err != nil {
				return fmt.Errorf("runtimebundle: %s: %w", label, err)
			}
		}
	}
	return nil
}

func buildBackendExecutionResolver(cfg *config.Config, reg *pluginreg.Registry) routing.BackendExecutionResolver {
	if cfg == nil {
		return routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
			return lipsdk.BackendExecutionUnknown, false
		})
	}
	classes := make(map[string]lipsdk.BackendExecutionClass, len(cfg.Plugins.Backends))
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		iid := p.InstanceID()
		if iid == "" {
			continue
		}
		fid := p.FactoryID()
		var cls lipsdk.BackendExecutionClass = lipsdk.BackendExecutionUnknown
		if reg != nil {
			if prof, ok := reg.BackendExecutionProfile(fid); ok {
				cls = prof.EffectiveClass()
			}
		}
		classes[iid] = cls
	}
	return routing.BackendExecutionResolverFunc(func(id string) (lipsdk.BackendExecutionClass, bool) {
		c, ok := classes[id]
		return c, ok
	})
}

func validateSelectorTextAgainstBackends(label, text string, configured map[string]struct{}) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	sel, err := routing.Parse(text)
	if err != nil {
		return nil // malformed selectors rejected by config.Validate
	}
	for _, id := range routing.BackendIDsReferenced(sel) {
		if _, ok := configured[id]; !ok {
			return fmt.Errorf("runtimebundle: %s references unconfigured backend %q", label, id)
		}
	}
	return nil
}

func resolveRouting(cfg *config.Config, wireModel config.WireModelForBackend) (string, string, *routing.AliasResolver, error) {
	if wireModel == nil {
		wireModel = standardplugins.DefaultWireModel
	}
	rawDefaultRoute := config.EffectiveDefaultRouteSelector(cfg, wireModel)
	aliasResolver, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg))
	if err != nil {
		return "", "", nil, fmt.Errorf("model_aliases: %w", err)
	}
	effectiveRoute := aliasResolver.Resolve(rawDefaultRoute)
	defBE, err := routing.DefaultBackendFromRouteSelector(effectiveRoute)
	if err != nil {
		return "", "", nil, err
	}
	return effectiveRoute, defBE, aliasResolver, nil
}
