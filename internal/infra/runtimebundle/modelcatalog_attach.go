package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/modelcatalog/modelsdev"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	opencodecatalog "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon/catalog"
)

type startedModelCatalog struct {
	Runtime *modelcatalog.CatalogRuntime
	closers []func() error
}

const (
	defaultModelCatalogCachePath = "data/model_catalog.json"
	defaultModelCatalogSourceURL = "https://models.dev/api.json"
	defaultModelCatalogInterval  = time.Hour
)

func startModelCatalog(parent context.Context, cfg *config.Config, upstream *http.Client) (*startedModelCatalog, error) {
	if !modelCatalogStartupNeeded(cfg) {
		return &startedModelCatalog{}, nil
	}
	catalogHTTP, closeCatalogHTTP := cloneCatalogHTTPClient(upstream)
	mc := cfg.ModelCatalog
	cachePath := strings.TrimSpace(mc.CachePath)
	if cachePath == "" {
		cachePath = filepath.Clean(defaultModelCatalogCachePath)
	}
	store := modelsdev.NewFileSnapshotStore(cachePath)
	interval, _ := mc.UpdateIntervalDuration()
	if interval <= 0 {
		interval = defaultModelCatalogInterval
	} else if interval < defaultModelCatalogInterval {
		interval = defaultModelCatalogInterval
	}
	fetchTimeout, _ := mc.FetchTimeoutDuration()
	sourceURL := strings.TrimSpace(mc.SourceURL)
	if sourceURL == "" {
		sourceURL = defaultModelCatalogSourceURL
	}
	src := modelsdev.NewHTTPSource(catalogHTTP, sourceURL, mc.ExternalUpdatesEnabled, fetchTimeout)
	cat := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  store,
	})
	if err := cat.Start(parent); err != nil {
		closeCatalogHTTP()
		_ = cat.Close()
		return nil, fmt.Errorf("runtimebundle: model catalog runtime start: %w", err)
	}
	if mc.ExternalUpdatesEnabled {
		cat.RunRefresh(parent)
	}

	out := &startedModelCatalog{
		Runtime: cat,
		closers: []func() error{func() error {
			err := cat.Close()
			closeCatalogHTTP()
			return err
		}},
	}

	var refreshWG sync.WaitGroup
	var refreshCleanup func()
	if mc.ExternalUpdatesEnabled && interval > 0 {
		refreshCtx, cancel := context.WithCancel(parent)
		runModelCatalogRefreshLoop(refreshCtx, cat, interval, &refreshWG)
		refreshCleanup = func() {
			cancel()
			refreshWG.Wait()
		}
		out.closers = append(out.closers, func() error {
			refreshCleanup()
			return nil
		})
	}

	return out, nil
}

func modelCatalogStartupNeeded(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.ModelCatalog.Enabled || cfg.ModelCatalog.ExternalUpdatesEnabled
}

func cloneCatalogHTTPClient(upstream *http.Client) (*http.Client, func()) {
	if upstream == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		return &http.Client{Transport: tr}, tr.CloseIdleConnections
	}
	c := *upstream
	if tr, ok := upstream.Transport.(*http.Transport); ok {
		clone := tr.Clone()
		c.Transport = clone
		return &c, clone.CloseIdleConnections
	}
	if closer, ok := upstream.Transport.(interface{ CloseIdleConnections() }); ok {
		return &c, closer.CloseIdleConnections
	}
	return &c, func() {}
}

func openCodeVendorResolver(cat *modelcatalog.CatalogRuntime) pluginreg.ModelVendorResolver {
	if cat != nil {
		return modelVendorResolverAdapter{resolver: opencodecatalog.NewOpenCodeVendorResolver(cat, true)}
	}
	return modelVendorResolverAdapter{
		resolver: opencodecatalog.NewOpenCodeVendorResolver(modelcatalog.StaticActiveSnapshotProvider{}, true),
	}
}

type modelVendorResolverAdapter struct {
	resolver modelcatalog.VendorResolver
}

func (a modelVendorResolverAdapter) CanonicalID(model string) string {
	if a.resolver == nil {
		return ""
	}
	return a.resolver.Resolve(model).CanonicalID
}

func wireModelCatalogExecutor(exec *runtime.Executor, cat *modelcatalog.CatalogRuntime, cfg *config.Config) {
	if exec == nil || cat == nil || cfg == nil || !cfg.ModelCatalog.Enabled {
		return
	}
	sizeEstimator := modelcatalog.DefaultSizeEstimator{}
	ovr := modelcatalog.NewOverrideResolver(OverrideSetFromModelCatalog(cfg.ModelCatalog))
	exec.CatalogResolver = modelcatalog.NewCatalogResolver(
		nil,
		ovr,
		true,
		cat,
	)
	exec.EligibilityResolver = modelcatalog.NewEligibilityResolver(sizeEstimator)
	exec.RequestTokenEstimator = sizeEstimator
}

// attachModelCatalog wires executor catalog resolvers when model_catalog.enabled requests it.
// Catalog I/O must already be started via [startModelCatalog].
func attachModelCatalog(
	exec *runtime.Executor,
	started *startedModelCatalog,
	cfg *config.Config,
) *modelcatalog.CatalogRuntime {
	if started == nil {
		return nil
	}
	wireModelCatalogExecutor(exec, started.Runtime, cfg)
	return started.Runtime
}
