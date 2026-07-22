package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	var codexLoadFn CodexCatalogLoadFunc
	if bctx.Opts != nil {
		codexLoadFn = bctx.Opts.Testing.CodexCatalogLoad
	}
	codexCatalog, codexCatalogSource := loadCodexModelCatalog(parent, cfg, reg, bctx.Log, codexLoadFn)
	backendDeps := pluginreg.BackendFactoryDeps{
		ModelVendorResolver:     openCodeVendorResolver(vendorCatalogRuntime),
		CodexModelCatalog:       codexCatalog,
		CodexModelCatalogSource: codexCatalogSource,
		Identity:                cfg.Identity,
	}

	if startedCatalog != nil {
		closers = registerStartedCatalogClosers(bctx.Ledger, closers, startedCatalog)
	}
	backends, inventories, routePrefixes, err := buildBackends(cfg, reg, upstream, backendDeps, bctx.Ledger)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: %w", err)
	}
	// Register backend closers before inventory/registry startup so later
	// model-runtime failures dispose already-constructed backends via the
	// existing reverse-order rollback path.
	closers = appendBackendClosers(closers, cfg, backends, bctx.Ledger)
	modelRegistryRuntime, modelRegistry, modelRegistryClosers, err := startModelRegistryRuntime(parent, cfg, inventories, bctx.Log, bctx.Ledger)
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

// registerStartedCatalogClosers registers close before refresh quiesce so every
// reverse-order path (candidate rollback and legacy Built disposal) cancels and
// waits for refresh work before closing the catalog runtime and HTTP client.
func registerStartedCatalogClosers(ledger *ResourceLedger, closers []func() error, started *startedModelCatalog) []func() error {
	if started == nil {
		return closers
	}
	for i, c := range started.closers {
		fn := c
		if ledger != nil {
			fn = ledger.AddClose(fmt.Sprintf("catalog-%d", i), PhaseClose, c)
		}
		closers = append(closers, fn)
	}
	for i, c := range started.quiesceClosers {
		fn := c
		if ledger != nil {
			fn = ledger.AddClose(fmt.Sprintf("catalog-refresh-%d", i), PhaseQuiesce, c)
		}
		closers = append(closers, fn)
	}
	return closers
}

func buildBackends(
	cfg *config.Config,
	reg *pluginreg.Registry,
	upstream *http.Client,
	backendDeps pluginreg.BackendFactoryDeps,
	ledger *ResourceLedger,
) (map[string]execbackend.Backend, []modelregistry.BackendInventory, []string, error) {
	backends := make(map[string]execbackend.Backend, len(cfg.Plugins.Backends))
	inventories := make([]modelregistry.BackendInventory, 0, len(cfg.Plugins.Backends))
	rawPrefixes := make([]string, 0, len(cfg.Plugins.Backends))
	constructedClosers := make([]func() error, 0, len(cfg.Plugins.Backends))
	modelInventoryFetchTimeout := cfg.ModelInventory.FetchTimeoutDuration()
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		iid := p.InstanceID()
		be, err := reg.BuildBackend(fid, p.Config, upstream, backendDeps)
		if err != nil {
			return nil, nil, nil, withDisposedClosers(
				fmt.Errorf("backend instance %s (factory %s): %w", iid, fid, err),
				constructedClosers,
			)
		}
		hooks := optionalBackendHooksFromBackend(be)
		inst := WrapBackendInstance(be, hooks)
		wrapped := inst.AsBackend()
		// Keep optional hooks only on the wrapper; executor map sees Close ownership.
		wrapped.Start = nil
		wrapped.Stop = nil
		wrapped.CleanupIdleTransports = nil
		wrapped.PreflightCapability = nil
		if hooks.PreflightCapability != nil {
			wrapped.PreflightCapability = func(ctx context.Context) (execbackend.CapabilityPreflight, error) {
				res, err := inst.PreflightCapability(ctx)
				return execbackend.CapabilityPreflight{
					Ready:       res.Ready,
					Billable:    res.Billable,
					Description: res.Description,
				}, err
			}
		}

		var releaseOnce sync.Once
		var releaseErr error
		release := func() error {
			releaseOnce.Do(func() {
				if err := inst.CleanupIdleTransports(context.Background()); err != nil {
					releaseErr = errors.Join(releaseErr, err)
				}
				if err := inst.Close(); err != nil {
					releaseErr = errors.Join(releaseErr, err)
				}
			})
			return releaseErr
		}
		owns := be.Close != nil || hooks.Start != nil || hooks.Stop != nil || hooks.CleanupIdleTransports != nil
		if owns {
			wrapped.Close = release
		}
		if owns {
			fn := release
			if ledger != nil {
				fn = ledger.AddClose("backend:"+iid, PhaseClose, release)
				if hooks.Start != nil {
					ledger.AddAction("backend:"+iid+":start", PhasePrepare, inst.Start, nil)
				}
			}
			wrapped.Close = fn
			constructedClosers = append(constructedClosers, fn)
		}
		backends[iid] = wrapped
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

func optionalBackendHooksFromBackend(be execbackend.Backend) OptionalBackendHooks {
	hooks := OptionalBackendHooks{
		Start:                 be.Start,
		Stop:                  be.Stop,
		CleanupIdleTransports: be.CleanupIdleTransports,
	}
	if be.PreflightCapability != nil {
		preflight := be.PreflightCapability
		hooks.PreflightCapability = func(ctx context.Context) (BackendPreflightResult, error) {
			res, err := preflight(ctx)
			if err != nil {
				return BackendPreflightResult{}, err
			}
			return BackendPreflightResult{
				Ready:       res.Ready,
				Billable:    res.Billable,
				Description: res.Description,
			}, nil
		}
	}
	return hooks
}

// appendBackendClosers appends non-nil backend Close callbacks in configuration
// order. nil Close remains a no-op and is not registered. When a ledger already
// wrapped Close during buildBackends, the same once-safe handle is appended.
func appendBackendClosers(closers []func() error, cfg *config.Config, backends map[string]execbackend.Backend, ledger *ResourceLedger) []func() error {
	if cfg == nil {
		return closers
	}
	_ = ledger // ledger registration happens in buildBackends; keep param for call-site clarity
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		be, ok := backends[p.InstanceID()]
		if !ok || be.Close == nil {
			continue
		}
		closers = append(closers, be.Close)
	}
	return closers
}

func startModelRegistryRuntime(
	parent context.Context,
	cfg *config.Config,
	inventories []modelregistry.BackendInventory,
	log *slog.Logger,
	ledger *ResourceLedger,
) (*modelregistry.Runtime, *modelregistry.Registry, []func() error, error) {
	var cache modelregistry.Cache
	if path := strings.TrimSpace(cfg.ModelInventory.CachePath); path != "" {
		cache = modelregistryfile.New(path)
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: inventories,
		Cache:       cache,
		Log:         log,
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
			fn := func() error {
				refreshCancel()
				refreshWG.Wait()
				return nil
			}
			if ledger != nil {
				fn = ledger.AddClose("model-registry-refresh", PhaseQuiesce, fn)
			}
			closers = append(closers, fn)
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
