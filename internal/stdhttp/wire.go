package stdhttp

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// BuildExecutor wires enabled backends from configuration into a core executor.
func BuildExecutor(cfg *config.Config, bus *hooks.Bus) (*runtime.Executor, b2bua.Store, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("stdhttp: nil config")
	}
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	backends := make(map[string]runtime.Backend)
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		be, err := pluginreg.BuildBackend(p.ID, p.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		backends[p.ID] = be
	}
	store, err := pluginreg.OpenContinuityStore(cfg.Continuity)
	if err != nil {
		return nil, nil, fmt.Errorf("stdhttp: %w", err)
	}
	effectiveRoute := routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	defBE, err := routing.DefaultBackendFromRouteSelector(effectiveRoute)
	if err != nil {
		return nil, nil, fmt.Errorf("stdhttp: %w", err)
	}
	capMap := make(capabilities.MapResolver, len(backends))
	for id, be := range backends {
		id, be := id, be
		capMap[id] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return runtime.BackendEffectiveCaps(ctx, be, call, cand)
		}
	}
	exec := &runtime.Executor{
		Store:          store,
		Bus:            bus,
		Backends:       backends,
		MaxAttempts:    cfg.Routing.MaxAttempts,
		DefaultBackend: defBE,
		CapsResolver:   capMap,
	}
	return exec, store, nil
}
