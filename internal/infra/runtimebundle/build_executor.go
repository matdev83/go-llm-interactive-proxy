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

// buildExecutorRuntime runs the executor-assembly sequence formerly inline in
// [Build]: routing resolution, capability map, RNG seed, stream-recovery config,
// token-accounting runtime, executor struct construction, interleaved-thinking
// application, token-accounting/price-catalog/auth/session/metrics/secure-session
// wiring, synthetic-local-principal flag, model-catalog attach, and the optional
// BuildOptions.SecureSessionStore override. It appends token-accounting closers
// to closers and returns the updated slice. Error wrapping matches the former
// inline block exactly.
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
	exec := &runtime.Executor{
		Store:                    in.Persistence.Store,
		Bus:                      bctx.Bus,
		RuntimeSnapshot:          in.Ext.Snap,
		Backends:                 in.Model.Backends,
		ALegLifecycle:            leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 2 * time.Second}),
		MaxAttempts:              cfg.Routing.MaxAttempts,
		DefaultBackend:           defBE,
		SelectorAliases:          aliasResolver,
		CapsResolver:             capMap,
		Rand:                     routing.NewSeededRng(seed),
		Now:                      in.NowFn,
		CandidateHealth:          routinghealth.CandidateHealthFromConfig(cfg, in.NowFn),
		RouteObserver:            routeObserverFor(log),
		AffinityStore:            affinitymem.New(),
		AffinityMissingIdentity:  affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		Log:                      log,
		MaxPendingWireEvents:     cfg.Server.MaxPendingWireEvents,
		StreamRecovery:           streamRecovery,
		TransportFallbackPolicy:  config.EffectiveTransportFallbackPolicy(cfg),
		PolicyDiagnosticsEnabled: opts.Policy.PolicyDiagnosticsEnabled,
	}
	if err := applyInterleavedToExecutor(exec, cfg); err != nil {
		return nil, closers, err
	}
	if tokenAccounting != nil {
		exec.Preflight = tokenAccounting.Preflight
		exec.StreamUsage = tokenAccounting.StreamUsage
		exec.Ledger = tokenAccounting.Ledger
		exec.LedgerWriteRequired = cfg.Accounting.Ledger.WritePolicy == "required"
		exec.TokenAccountingObservability = tokenAccounting.Observability
		exec.AdminCountService = tokenAccounting.Admin
	}
	if len(cfg.Accounting.Pricing.Models) > 0 {
		catalog, err := accounting.NewPriceCatalog(config.AccountingPriceCatalogConfig(cfg.Accounting.Pricing))
		if err != nil {
			return nil, closers, fmt.Errorf("runtimebundle: accounting pricing: %w", err)
		}
		exec.AccountingPriceCatalog = catalog
	}
	exec.AuthEvents = in.Security.AuthEvents
	exec.SessionAuditPolicy = in.Security.SAP
	applySecureSessionToExecutor(exec, in.Persistence.SecureSession)
	ssStore := strings.TrimSpace(cfg.SecureSession.Store)
	if ssStore == "" {
		ssStore = "memory"
	}
	exec.SyntheticLocalPrincipal = cfg.SingleUserLocalMode() && strings.EqualFold(ssStore, "memory")
	if in.Observability.Bundle != nil {
		exec.Metrics = in.Observability.Bundle.ExecutorSink()
		exec.ExtensionMetrics = in.Observability.Bundle.ExtensionStageSink()
		exec.SecureSessionMetrics = in.Observability.Bundle.SecureSessionMetricsSink()
		if tokenAccounting != nil && tokenAccounting.Observability != nil {
			tokenAccounting.Observability.SetSink(in.Observability.Bundle.TokenAccountingObservabilitySink())
		}
	}
	secureSessionStore := in.Persistence.SecureSession.appStore
	if opts.Diagnostics.SecureSessionStore != nil {
		secureSessionStore = opts.Diagnostics.SecureSessionStore
	}
	catalogRuntime := attachModelCatalog(exec, in.Model.StartedCatalog, cfg)
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
