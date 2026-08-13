package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
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
	if bctx.ExplicitCandidate {
		if err := validateRouteSelectorsAgainstBackends(cfg, effectiveRoute, cfg.ModelAliases, cfg.Plugins.Backends); err != nil {
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
		if prod.BillingHoldTTL <= 0 {
			prod.BillingHoldTTL = cfg.Accounting.Billing.EffectiveHoldTTL()
			if a, ok := prod.BillingAdmission.(*billingadmission.Adapter); ok {
				a.SetHoldTTL(prod.BillingHoldTTL)
			}
		}
		if prod.BillingAdmissionRequired && prod.BillingAdmission == nil {
			return nil, ErrBillingAdmissionRequired
		}
		if prod.BillingAuthoritative {
			if prod.BillingStore == nil || prod.BillingAdmission == nil || prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.AuthorizationID == nil || prod.BillingRatingResolver == nil || in.Ledger == nil {
				return nil, ErrAuthoritativeBillingRequired
			}
			// The authoritative store is the single composition authority for
			// terminal evidence and reports. Do not permit separately injected
			// handoff/report implementations to drift from settlement truth.
			handoff, ok := prod.BillingStore.(billing.UsageRecordAppender)
			if !ok {
				return nil, ErrAuthoritativeBillingRequired
			}
			prod.BillingTerminalHandoff = handoff
			prod.BillingReports = prod.BillingStore
			postTurnStore, ok := prod.BillingStore.(billing.PostTurnStore)
			if !ok {
				return nil, ErrAuthoritativeBillingRequired
			}
			worker, err := billing.NewPostTurnWorker(postTurnStore, prod.BillingRatingResolver, billing.PostTurnWorkerConfig{
				BatchSize: prod.BillingPostTurnBatchSize,
				Interval:  prod.BillingPostTurnInterval,
			})
			if err != nil {
				return nil, fmt.Errorf("runtimebundle: authoritative billing worker: %w", err)
			}
			// PhaseActivate runs during CompileCandidate, before handler
			// composition and host publication. Start only after Publish.
			in.Ledger.AddAction(
				"billing-post-turn-worker", PhasePublish,
				func(context.Context) error { return worker.Start(context.Background()) },
				worker.Stop,
			)
			in.Ledger.AddClose("billing-post-turn-worker", PhaseQuiesce, func() error {
				return worker.Stop(context.Background())
			})
		} else if prod.BillingTerminalHandoff != nil && (prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.AuthorizationID == nil) {
			return nil, ErrBillingTerminalIdentityRequired
		}
		if prod.MeteringRecorder != nil {
			meteringRT = &meteringRuntime{Recorder: prod.MeteringRecorder, StoreBacking: "injected"}
		}
		// Persist cutover wiring so candidate HTTP composition sees the same
		// authoritative reports path/store the executor was built with.
		in.Bctx.Opts.Production = prod
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
		MaxAttempts:             cfg.Routing.MaxAttempts,
		DefaultBackend:          defBE,
		SelectorAliases:         aliasResolver,
		CapsResolver:            capMap,
		CandidateHealth:         candHealth,
		RouteObserver:           routeObserverFor(log),
		AffinityStore:           affStore,
		AffinityMissingIdentity: affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		TransportFallbackPolicy: config.EffectiveTransportFallbackPolicy(cfg),
		RouteOverrideReader:     overrideReader,
	}
	routingRT, catalogRuntime := attachModelCatalog(routingRT, in.Model.StartedCatalog, cfg)

	var holdReleaser billing.HoldReleaser
	if prod.BillingStore != nil {
		if releaser, ok := prod.BillingStore.(billing.HoldReleaser); ok {
			holdReleaser = releaser
		}
	}

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
			BillingAdmission:       prod.BillingAdmission,
			BillingLegObserver:     billingLegObserverFor(log),
			BillingTerminalHandoff: prod.BillingTerminalHandoff,
			BillingHoldReleaser:    holdReleaser,
			BillingIdentity:        prod.BillingIdentity,
			BillingAuthoritative:   prod.BillingAuthoritative,
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
	if in.Ledger != nil && prod.BillingTerminalHandoff != nil {
		// Join detached TUR handoff retries before PhaseClose tears down the store.
		// Registered after the post-turn worker so reverse Quiesce waits handoffs first.
		in.Ledger.AddClose("billing-handoff-retries", PhaseQuiesce, func() error {
			exec.WaitBillingHandoffRetriesForClose()
			return nil
		})
	}

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
	}, nil
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
// replacements reference backends absent from the candidate backend row set (req 9.2).
func validateRouteSelectorsAgainstBackends(cfg *config.Config, effectiveRoute string, aliases []config.ModelAliasConfig, backendRows []config.PluginConfig) error {
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
	}
	return nil
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
