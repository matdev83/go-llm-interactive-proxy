package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	affinitymem "github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity/memorystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/routinghealth"
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
}

// executorBuildInput groups the upstream unit results consumed by
// [buildExecutorRuntime].
type executorBuildInput struct {
	Bctx          buildContext
	NowFn         func() time.Time
	Ext           *extensionRuntime
	Model         *modelRuntime
	Persistence   *persistenceRuntime
	Security      *securityRuntime
	Observability *observabilityRuntime
	ControlPlane  *controlPlaneRuntime
}

// buildExecutorRuntime runs the executor-assembly sequence: routing resolution,
// capability map, RNG seed, stream-recovery config, token-accounting runtime,
// interleaved-thinking config, price catalog, auth/session/metrics/secure-session
// wiring, synthetic-local-principal flag, and model-catalog resolver attachment.
// All values are computed before [runtime.NewExecutor] so NewExecutor is a strong
// invariant boundary: no post-construction field mutation occurs.
// It appends token-accounting closers to closers and returns the updated slice.
func buildExecutorRuntime(in executorBuildInput, closers []func() error) (*executorRuntime, []func() error, error) {
	bctx := in.Bctx
	cfg, log, opts, parent := bctx.Cfg, bctx.Log, bctx.Opts, bctx.Parent

	effectiveRoute, defBE, aliasResolver, err := resolveRouting(cfg, opts.WireModel)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: %w", err)
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
		return nil, closers, err
	}
	tokenAccounting, accountingClosers, err := buildTokenAccountingRuntime(parent, cfg, in.NowFn, in.Model.Backends)
	if err != nil {
		return nil, closers, err
	}
	closers = append(closers, accountingClosers...)

	// Compute interleaved-thinking config before construction.
	interleaved, err := interleavedExecutorRuntime(cfg)
	if err != nil {
		return nil, closers, err
	}

	// Compute accounting runtime fields.
	accountingRT := runtime.AccountingRuntime{
		LedgerWriteRequired: cfg.Accounting.Ledger.WritePolicy == "required",
	}
	if tokenAccounting != nil {
		accountingRT.Preflight = tokenAccounting.Preflight
		accountingRT.StreamUsage = tokenAccounting.StreamUsage
		accountingRT.Ledger = tokenAccounting.Ledger
		accountingRT.TokenAccountingObservability = tokenAccounting.Observability
		accountingRT.AdminCountService = tokenAccounting.Admin
	}
	if len(cfg.Accounting.Pricing.Models) > 0 {
		catalog, err := accounting.NewPriceCatalog(config.AccountingPriceCatalogConfig(cfg.Accounting.Pricing))
		if err != nil {
			return nil, closers, fmt.Errorf("runtimebundle: accounting pricing: %w", err)
		}
		accountingRT.AccountingPriceCatalog = catalog
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

	// Compute observability runtime with metrics sinks.
	obsRT := runtime.ObservabilityRuntime{
		Log:                      log,
		PolicyDiagnosticsEnabled: opts.Policy.PolicyDiagnosticsEnabled,
	}
	if in.Observability.Bundle != nil {
		obsRT.Metrics = in.Observability.Bundle.ExecutorSink()
		obsRT.ExtensionMetrics = in.Observability.Bundle.ExtensionStageSink()
		securityRT.SecureSessionMetrics = in.Observability.Bundle.SecureSessionMetricsSink()
		if tokenAccounting != nil && tokenAccounting.Observability != nil {
			tokenAccounting.Observability.SetSink(in.Observability.Bundle.TokenAccountingObservabilitySink())
		}
	}

	// Build routing runtime; model catalog resolvers are attached before construction.
	routingRT := runtime.RoutingRuntime{
		MaxAttempts:             cfg.Routing.MaxAttempts,
		DefaultBackend:          defBE,
		SelectorAliases:         aliasResolver,
		CapsResolver:            capMap,
		CandidateHealth:         routinghealth.CandidateHealthFromConfig(cfg, in.NowFn),
		RouteObserver:           routeObserverFor(log),
		AffinityStore:           affinitymem.New(),
		AffinityMissingIdentity: affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		TransportFallbackPolicy: config.EffectiveTransportFallbackPolicy(cfg),
	}
	routingRT, catalogRuntime := attachModelCatalog(routingRT, in.Model.StartedCatalog, cfg)

	// Construct executor with all fields set — no post-construction mutation.
	exec := runtime.NewExecutor(runtime.ExecutorConfig{
		Core: runtime.CoreRuntime{
			Store:                in.Persistence.Store,
			Backends:             in.Model.Backends,
			ALegLifecycle:        leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 2 * time.Second}),
			Rand:                 routing.NewSeededRng(seed),
			Now:                  in.NowFn,
			MaxPendingWireEvents: cfg.Server.MaxPendingWireEvents,
			StreamRecovery:       streamRecovery,
		},
		Routing:       routingRT,
		Security:      securityRT,
		Accounting:    accountingRT,
		Observability: obsRT,
		Extension: runtime.ExtensionRuntime{
			Bus:             bctx.Bus,
			RuntimeSnapshot: in.Ext.Snap,
		},
		Interleaved: interleaved,
	})

	secureSessionStore := in.Persistence.SecureSession.appStore
	if opts.Diagnostics.SecureSessionStore != nil {
		secureSessionStore = opts.Diagnostics.SecureSessionStore
	}
	return &executorRuntime{
		Exec:                 exec,
		EffectiveRoute:       effectiveRoute,
		SecureSessionStore:   secureSessionStore,
		CatalogRuntime:       catalogRuntime,
		TokenAccountingAdmin: tokenAccountingAdmin(tokenAccounting),
	}, closers, nil
}

func tokenAccountingAdmin(r *tokenAccountingRuntime) *accountingapp.Service {
	if r == nil {
		return nil
	}
	return r.Admin
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
