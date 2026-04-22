package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Build assembles continuity store, executor, and closers for the standard distribution.
//
// cfg must be non-nil. bus may be nil (replaced with an empty hooks.Bus). log must be non-nil.
// opts must be non-nil and opts.PluginRegistry must be non-nil; other BuildOptions fields are optional.
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

	upstream := httpclient.Standard()
	if opts.HTTPClient != nil {
		upstream = opts.HTTPClient
	}

	backends := make(map[string]runtime.Backend)
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
	var closers []func() error
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
		id, be := id, be
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return runtime.BackendEffectiveCaps(ctx, be, call, cand)
		}
	}

	var rngSrc mathrand.Source
	var seed int64
	if err := binary.Read(crand.Reader, binary.LittleEndian, &seed); err != nil {
		seed = time.Now().UnixNano()
	}
	rngSrc = mathrand.NewSource(seed)

	nowFn := time.Now
	if opts.Clock != nil {
		nowFn = opts.Clock
	}

	exec := &runtime.Executor{
		Store:           store,
		Bus:             bus,
		Backends:        backends,
		MaxAttempts:     cfg.Routing.MaxAttempts,
		DefaultBackend:  defBE,
		CapsResolver:    capMap,
		Rand:            mathrand.New(rngSrc),
		Now:             nowFn,
		CandidateHealth: routingCandidateHealth(cfg, nowFn),
		RouteObserver:   routeObserverFor(log),
		Log:             log,
	}
	return &Built{
		Executor:              exec,
		Store:                 store,
		Closers:               closers,
		UpstreamHTTP:          upstream,
		PluginRegistry:        reg,
		EffectiveDefaultRoute: effectiveRoute,
	}, nil
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
