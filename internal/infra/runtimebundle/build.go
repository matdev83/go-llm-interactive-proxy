package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	affinitymem "github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity/memorystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	coreworkspace "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/routinghealth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// Build assembles continuity store, executor, and closers for the standard distribution.
//
// cfg must be non-nil. bus may be nil (replaced with an empty hooks.Bus). log must be non-nil.
// opts must be non-nil and opts.PluginRegistry must be non-nil; other BuildOptions fields are optional.
//
// The returned [Built.RuntimeSnapshot] (and executor snapshot) is shared by concurrent requests:
// do not mutate bus or snapshot contents after Build. Reload or reconfiguration must call Build
// again and swap to new [*Built] / executor instances so each generation gets its own snapshot.
func Build(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, opts *BuildOptions) (*Built, error) {
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
	authEvents, err := buildAuthEventDispatcher(cfg, log, opts)
	if err != nil {
		return nil, err
	}
	reg := opts.PluginRegistry
	if err := validateBackendSecurityProfiles(cfg, reg); err != nil {
		return nil, err
	}
	sap, err := buildSessionAuditPolicy(cfg)
	if err != nil {
		return nil, err
	}
	httpAuth, err := resolveHTTPAuthProviders(cfg, log, opts, authEvents, sap)
	if err != nil {
		return nil, err
	}

	var bundle *metrics.Bundle
	if cfg.Observability.Metrics.Enabled {
		bundle = metrics.NewBundle(cfg)
	}

	tune := httpclient.TransportTuneFromConfig(cfg)
	upstream := httpclient.StandardWithTune(cfg.EffectiveTrustEnvironmentProxy(), tune)
	if opts.HTTPClient != nil {
		upstream = opts.HTTPClient
	}
	upstream = wrapUpstreamClient(upstream, bundle, opts.OutboundTracing)

	backends := make(map[string]execbackend.Backend, len(cfg.Plugins.Backends))
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		iid := p.InstanceID()
		be, err := reg.BuildBackend(fid, p.Config, upstream)
		if err != nil {
			return nil, fmt.Errorf("backend instance %s (factory %s): %w", iid, fid, err)
		}
		backends[iid] = be
	}
	if cfg.Accounting.StrictAuthoritative {
		for id, be := range backends {
			if be.FinalizeBilling == nil {
				return nil, fmt.Errorf("runtimebundle: accounting strict_authoritative requires billing finalizer for backend %q", id)
			}
		}
	}
	parent := context.Background()
	if opts.StartupContext != nil {
		parent = opts.StartupContext
	}
	store, err := continuity.OpenStoreContext(parent, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	closers := []func() error{}
	if c, ok := store.(interface{ Close() error }); ok {
		closers = append(closers, c.Close)
	}

	ssRun, err := buildSecureSessionRuntime(secureSessionBuildInput{
		StartupContext: parent,
		Cfg:            cfg,
		B2B:            store,
		Log:            log,
		Bundle:         bundle,
	})
	if err != nil {
		if derr := disposeClosers(closers); derr != nil {
			return nil, errors.Join(err, derr)
		}
		return nil, err
	}
	if ssRun.closer != nil {
		closers = append(closers, ssRun.closer)
	}

	wireModel := opts.WireModel
	if wireModel == nil {
		wireModel = pluginreg.DefaultWireModel
	}
	rawDefaultRoute := config.EffectiveDefaultRouteSelector(cfg, wireModel)
	aliasResolver, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg))
	if err != nil {
		if derr := disposeClosers(closers); derr != nil {
			return nil, errors.Join(fmt.Errorf("runtimebundle: model_aliases: %w", err), derr)
		}
		return nil, fmt.Errorf("runtimebundle: model_aliases: %w", err)
	}
	effectiveRoute := aliasResolver.Resolve(rawDefaultRoute)
	defBE, err := routing.DefaultBackendFromRouteSelector(effectiveRoute)
	if err != nil {
		wrapped := fmt.Errorf("runtimebundle: %w", err)
		if derr := disposeClosers(closers); derr != nil {
			return nil, errors.Join(wrapped, derr)
		}
		return nil, wrapped
	}
	capMap := make(capabilities.MapResolver, len(backends))
	for id, be := range backends {
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, be, call, cand)
		}
	}

	var seed int64
	if err := binary.Read(crand.Reader, binary.LittleEndian, &seed); err != nil {
		seed = time.Now().UnixNano()
	}

	nowFn := time.Now
	if opts.Clock != nil {
		nowFn = opts.Clock
	}

	var ws lipworkspace.Resolver = lipworkspace.DisabledResolver{}
	if len(opts.WorkspaceResolvers) > 0 {
		ss := cfg.SecureSession
		secureOn := cfg.SecureSessionEffectivelyEnabled()
		resolveFailClosed := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError)) == "fail_closed"
		failClosedWS := secureOn && resolveFailClosed
		if failClosedWS {
			ws = coreworkspace.NewStrictChain(opts.WorkspaceResolvers)
		} else {
			ws = coreworkspace.NewResolverChain(opts.WorkspaceResolvers)
		}
	}
	var openers []session.Opener
	if len(opts.SessionOpeners) > 0 {
		openers = slices.Clone(opts.SessionOpeners)
	}
	var catalogFilters []toolcatalog.Filter
	if len(opts.ToolCatalogFilters) > 0 {
		catalogFilters = slices.Clone(opts.ToolCatalogFilters)
	}
	var toolPolicies []toolpolicy.Policy
	if len(opts.ToolCallPolicies) > 0 {
		toolPolicies = slices.Clone(opts.ToolCallPolicies)
	}
	var reqTransforms []request.Transform
	if len(opts.RequestTransforms) > 0 {
		reqTransforms = slices.Clone(opts.RequestTransforms)
	}
	var routeHints []routehint.Provider
	if len(opts.RouteHintProviders) > 0 {
		routeHints = slices.Clone(opts.RouteHintProviders)
	}
	var exec *runtime.Executor
	var compGates []completion.Gate
	if len(opts.CompletionGates) > 0 {
		compGates = slices.Clone(opts.CompletionGates)
	}
	var trafficObs traffic.Observer = traffic.NoopObserver{}
	if len(opts.TrafficObservers) > 0 {
		trafficObs = traffic.ChainObservers(opts.TrafficObservers...)
	}
	var usageObs usage.Observer = usage.NoopObserver{}
	if len(opts.UsageObservers) > 0 {
		usageObs = usage.ChainObservers(opts.UsageObservers...)
	}
	var trafficRaw traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if len(opts.RawCaptureSinks) > 0 {
		trafficRaw = traffic.MultiRawCapture(opts.RawCaptureSinks...)
	}
	var trafficRedactors []traffic.Redactor
	if len(opts.TrafficRedactors) > 0 {
		trafficRedactors = slices.Clone(opts.TrafficRedactors)
	}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State:              corestate.NewMem(nowFn),
		Aux:                auxreq.NewClient(func() auxreq.ExecutorRunner { return exec }),
		Workspace:          ws,
		SessionOpeners:     openers,
		ToolCatalogFilters: catalogFilters,
		ToolCallPolicies:   toolPolicies,
		RequestTransforms:  reqTransforms,
		RouteHintProviders: routeHints,
		CompletionGates:    compGates,
		TrafficObserver:    trafficObs,
		UsageObserver:      usageObs,
		RawCapture:         trafficRaw,
		TrafficRedactors:   trafficRedactors,
	})
	streamRecovery, err := streamRecoveryConfigFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	tokenAccounting, accountingClosers, err := buildTokenAccountingRuntime(parent, cfg, nowFn, backends)
	if err != nil {
		if derr := disposeClosers(closers); derr != nil {
			return nil, errors.Join(err, derr)
		}
		return nil, err
	}
	closers = append(closers, accountingClosers...)
	exec = &runtime.Executor{
		Store:                   store,
		Bus:                     bus,
		RuntimeSnapshot:         snap,
		Backends:                backends,
		ALegLifecycle:           leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 2 * time.Second}),
		MaxAttempts:             cfg.Routing.MaxAttempts,
		DefaultBackend:          defBE,
		SelectorAliases:         aliasResolver,
		CapsResolver:            capMap,
		Rand:                    routing.NewSeededRng(seed),
		Now:                     nowFn,
		CandidateHealth:         routinghealth.CandidateHealthFromConfig(cfg, nowFn),
		RouteObserver:           routeObserverFor(log),
		AffinityStore:           affinitymem.New(),
		AffinityMissingIdentity: affinity.MissingIdentityPolicy(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity)),
		Log:                     log,
		MaxPendingWireEvents:    cfg.Server.MaxPendingWireEvents,
		StreamRecovery:          streamRecovery,
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
			return nil, fmt.Errorf("runtimebundle: accounting pricing: %w", err)
		}
		exec.AccountingPriceCatalog = catalog
	}
	exec.AuthEvents = authEvents
	exec.SessionAuditPolicy = sap
	applySecureSessionToExecutor(exec, ssRun)
	ssStore := strings.TrimSpace(cfg.SecureSession.Store)
	if ssStore == "" {
		ssStore = "memory"
	}
	exec.SyntheticLocalPrincipal = cfg.SingleUserLocalMode() && strings.EqualFold(ssStore, "memory")
	if bundle != nil {
		exec.Metrics = bundle.ExecutorSink()
		exec.ExtensionMetrics = bundle.ExtensionStageSink()
		exec.SecureSessionMetrics = bundle.SecureSessionMetricsSink()
		if tokenAccounting != nil && tokenAccounting.Observability != nil {
			tokenAccounting.Observability.SetSink(bundle.TokenAccountingObservabilitySink())
		}
	}
	secureSessionStore := ssRun.appStore
	if opts.SecureSessionStore != nil {
		secureSessionStore = opts.SecureSessionStore
	}
	catalogRuntime, err := attachModelCatalog(parent, exec, &closers, cfg, upstream)
	if err != nil {
		wrapped := fmt.Errorf("runtimebundle: model catalog: %w", err)
		if derr := disposeClosers(closers); derr != nil {
			return nil, errors.Join(wrapped, derr)
		}
		return nil, wrapped
	}
	return &Built{
		Executor:              exec,
		Store:                 store,
		Closers:               closers,
		UpstreamHTTP:          upstream,
		PluginRegistry:        reg,
		EffectiveDefaultRoute: effectiveRoute,
		Metrics:               bundle,
		RuntimeSnapshot:       snap,
		HTTPAuthProviders:     httpAuth,
		SecureSessionStore:    secureSessionStore,
		AuthEventDispatcher:   authEvents,
		CatalogRuntime:        catalogRuntime,
		TokenAccountingAdmin:  tokenAccountingAdmin(tokenAccounting),
	}, nil
}

func tokenAccountingAdmin(r *tokenAccountingRuntime) *accountingapp.Service {
	if r == nil {
		return nil
	}
	return r.Admin
}

func disposeClosers(closers []func() error) error {
	var out error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: dispose closer: %w", err))
		}
	}
	return out
}

func wrapUpstreamClient(client *http.Client, bundle *metrics.Bundle, outboundTracing bool) *http.Client {
	if client == nil {
		return nil
	}
	rt := client.Transport
	if rt == nil {
		rt = httpclient.DefaultTransport()
	}
	wrapped := rt
	if bundle != nil && bundle.Upstream != nil {
		wrapped = bundle.Upstream.WrapUpstreamRoundTripper(wrapped)
	}
	if outboundTracing {
		wrapped = tracing.WrapTransport(true, wrapped)
	}
	if wrapped == rt {
		return client
	}
	c := *client
	c.Transport = wrapped
	return &c
}

// BuildExecutor wires enabled backends from configuration into a core executor with production
// defaults. Prefer Build for a structured composition result.
func BuildExecutor(
	cfg *config.Config,
	bus *hooks.Bus,
	log *slog.Logger,
	reg *pluginreg.Registry,
) (*runtime.Executor, b2bua.Store, []func() error, error) {
	b, err := Build(cfg, bus, log, &BuildOptions{PluginRegistry: reg})
	if err != nil {
		return nil, nil, nil, err
	}
	return b.Executor, b.Store, b.Closers, nil
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
