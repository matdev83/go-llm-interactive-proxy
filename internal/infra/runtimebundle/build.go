package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	coreworkspace "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
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
	reg := opts.PluginRegistry

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

	backends := make(map[string]runtime.Backend, len(cfg.Plugins.Backends))
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
	store, err := pluginreg.OpenContinuityStore(cfg.Continuity)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	closers := []func() error{}
	if c, ok := store.(interface{ Close() error }); ok {
		closers = append(closers, c.Close)
	}

	wireModel := opts.WireModel
	if wireModel == nil {
		wireModel = pluginreg.DefaultWireModel
	}
	effectiveRoute := routing.EffectiveDefaultRouteSelector(cfg, wireModel)
	defBE, err := routing.DefaultBackendFromRouteSelector(effectiveRoute)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	capMap := make(capabilities.MapResolver, len(backends))
	for id, be := range backends {
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return runtime.BackendEffectiveCaps(ctx, be, call, cand)
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
		ws = coreworkspace.NewResolverChain(opts.WorkspaceResolvers)
	}
	var openers []session.Opener
	if len(opts.SessionOpeners) > 0 {
		openers = slices.Clone(opts.SessionOpeners)
	}
	var catalogFilters []toolcatalog.Filter
	if len(opts.ToolCatalogFilters) > 0 {
		catalogFilters = slices.Clone(opts.ToolCatalogFilters)
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
		RequestTransforms:  reqTransforms,
		RouteHintProviders: routeHints,
		CompletionGates:    compGates,
		TrafficObserver:    trafficObs,
		RawCapture:         trafficRaw,
		TrafficRedactors:   trafficRedactors,
	})
	exec = &runtime.Executor{
		Store:                store,
		Bus:                  bus,
		RuntimeSnapshot:      snap,
		Backends:             backends,
		MaxAttempts:          cfg.Routing.MaxAttempts,
		DefaultBackend:       defBE,
		CapsResolver:         capMap,
		Rand:                 routing.NewSeededRng(seed),
		Now:                  nowFn,
		CandidateHealth:      routingCandidateHealth(cfg, nowFn),
		RouteObserver:        routeObserverFor(log),
		Log:                  log,
		MaxPendingWireEvents: cfg.Server.MaxPendingWireEvents,
	}
	if bundle != nil {
		exec.Metrics = bundle.ExecutorSink()
		exec.ExtensionMetrics = bundle.ExtensionStageSink()
	}
	var httpAuth []httpauth.Provider
	if len(opts.HTTPAuthProviders) > 0 {
		httpAuth = slices.Clone(opts.HTTPAuthProviders)
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
	}, nil
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
func BuildExecutor(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, reg *pluginreg.Registry) (*runtime.Executor, b2bua.Store, []func() error, error) {
	b, err := Build(cfg, bus, log, &BuildOptions{PluginRegistry: reg})
	if err != nil {
		return nil, nil, nil, err
	}
	return b.Executor, b.Store, b.Closers, nil
}
