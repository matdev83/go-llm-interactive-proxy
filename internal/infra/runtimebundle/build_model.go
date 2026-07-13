package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	modelregistryfile "github.com/matdev83/go-llm-interactive-proxy/internal/infra/modelregistry/filestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// modelRuntime holds the model-catalog start, built backends, route prefixes, and
// model-registry runtime produced by [buildModelRuntime]. StartedCatalog is
// exposed so [Build] can later call [attachModelCatalog]; Inventories and
// BackendDeps are internal to the unit.
type modelRuntime struct {
	StartedCatalog  *startedModelCatalog
	Backends        map[string]execbackend.Backend
	RoutePrefixes   []string
	RegistryRuntime *modelregistry.Runtime
	Registry        *modelregistry.Registry
}

// buildModelRuntime runs the catalog -> backends -> registry -> strict-accounting
// sequence formerly inline in [Build]. It appends catalog and registry closers to
// closers (in that order) and returns the updated slice. Error wrapping matches
// the former inline block exactly: catalog errors are returned unwrapped, backend
// errors get "runtimebundle: %w", registry errors get "runtimebundle: model registry: %w".
func buildModelRuntime(bctx buildContext, upstream *http.Client, closers []func() error) (*modelRuntime, []func() error, error) {
	cfg, parent, reg := bctx.Cfg, bctx.Parent, bctx.Opts.PluginRegistry
	startedCatalog, err := startModelCatalog(parent, cfg, upstream)
	if err != nil {
		return nil, closers, err
	}
	var vendorCatalogRuntime *modelcatalog.CatalogRuntime
	if startedCatalog != nil {
		vendorCatalogRuntime = startedCatalog.Runtime
	}
	codexCatalog, _ := loadCodexModelCatalog(parent, cfg, bctx.Log)
	backendDeps := pluginreg.BackendFactoryDeps{
		ModelVendorResolver: openCodeVendorResolver(vendorCatalogRuntime),
		CodexModelCatalog:   codexCatalog,
	}

	if startedCatalog != nil {
		closers = append(closers, startedCatalog.closers...)
	}
	backends, inventories, routePrefixes, err := buildBackends(cfg, reg, upstream, backendDeps)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: %w", err)
	}
	modelRegistryRuntime, modelRegistry, modelRegistryClosers, err := startModelRegistryRuntime(parent, cfg, inventories)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: model registry: %w", err)
	}
	closers = append(closers, modelRegistryClosers...)
	if cfg.Accounting.StrictAuthoritative {
		for id, be := range backends {
			if be.FinalizeBilling == nil {
				return nil, closers, fmt.Errorf("runtimebundle: accounting strict_authoritative requires billing finalizer for backend %q", id)
			}
		}
	}
	return &modelRuntime{
		StartedCatalog:  startedCatalog,
		Backends:        backends,
		RoutePrefixes:   routePrefixes,
		RegistryRuntime: modelRegistryRuntime,
		Registry:        modelRegistry,
	}, closers, nil
}

func buildBackends(
	cfg *config.Config,
	reg *pluginreg.Registry,
	upstream *http.Client,
	backendDeps pluginreg.BackendFactoryDeps,
) (map[string]execbackend.Backend, []modelregistry.BackendInventory, []string, error) {
	backends := make(map[string]execbackend.Backend, len(cfg.Plugins.Backends))
	inventories := make([]modelregistry.BackendInventory, 0, len(cfg.Plugins.Backends))
	rawPrefixes := make([]string, 0, len(cfg.Plugins.Backends))
	modelInventoryFetchTimeout := cfg.ModelInventory.FetchTimeoutDuration()
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		iid := p.InstanceID()
		be, err := reg.BuildBackend(fid, p.Config, upstream, backendDeps)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("backend instance %s (factory %s): %w", iid, fid, err)
		}
		backends[iid] = be
		rawPrefixes = append(rawPrefixes, be.BackendPrefixes...)
		inventories = append(inventories, modelregistry.BackendInventory{
			BackendID:       iid,
			Kind:            fid,
			BackendPrefixes: be.BackendPrefixes,
			Provider:        be.ModelInventory,
			FetchTimeout:    modelInventoryFetchTimeout,
		})
	}
	routePrefixes := routing.FilterRoutePrefixes(rawPrefixes)
	return backends, inventories, routePrefixes, nil
}

func startModelRegistryRuntime(
	parent context.Context,
	cfg *config.Config,
	inventories []modelregistry.BackendInventory,
) (*modelregistry.Runtime, *modelregistry.Registry, []func() error, error) {
	var cache modelregistry.Cache
	if path := strings.TrimSpace(cfg.ModelInventory.CachePath); path != "" {
		cache = modelregistryfile.New(path)
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: inventories,
		Cache:       cache,
	})
	if err := rt.Start(parent); err != nil {
		return nil, nil, nil, err
	}
	reg := rt.ActiveRegistry()
	if reg == nil {
		return nil, nil, nil, modelregistry.ErrSnapshotUnavailable
	}
	closers := []func() error{}
	if cfg.ModelInventory.EffectiveRefreshEnabled() && hasRefreshableModelInventory(inventories) {
		interval := cfg.ModelInventory.RefreshIntervalDuration()
		if interval > 0 {
			refreshCtx, refreshCancel := context.WithCancel(parent)
			var refreshWG sync.WaitGroup
			runModelRegistryRefreshLoop(refreshCtx, rt, interval, &refreshWG)
			closers = append(closers, func() error {
				refreshCancel()
				refreshWG.Wait()
				return nil
			})
		}
	}
	return rt, reg, closers, nil
}

func hasRefreshableModelInventory(inventories []modelregistry.BackendInventory) bool {
	for _, inv := range inventories {
		if inv.Provider == nil {
			continue
		}
		if static, ok := inv.Provider.(modelinventory.StaticInventory); ok && static.StaticInventory() {
			continue
		}
		return true
	}
	return false
}
