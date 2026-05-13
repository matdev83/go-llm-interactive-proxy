package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/modelcatalog/modelsdev"
)

// attachModelCatalog starts optional catalog I/O (local cache load and background refresh) when
// model_catalog.enabled or model_catalog.external_updates_enabled requests it. When catalog usage
// is enabled, wires resolver, size estimator, and eligibility on the executor.
func attachModelCatalog(
	parent context.Context,
	exec *runtime.Executor,
	closers *[]func() error,
	cfg *config.Config,
	upstream *http.Client,
) (*modelcatalog.CatalogRuntime, error) {
	mc := cfg.ModelCatalog
	if !mc.Enabled && !mc.ExternalUpdatesEnabled {
		return nil, nil
	}
	cachePath := strings.TrimSpace(mc.CachePath)
	store := modelsdev.NewFileSnapshotStore(cachePath)
	interval, _ := mc.UpdateIntervalDuration()
	fetchTimeout, _ := mc.FetchTimeoutDuration()
	src := modelsdev.NewHTTPSource(upstream, strings.TrimSpace(mc.SourceURL), mc.ExternalUpdatesEnabled, fetchTimeout)
	cat := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  store,
	})
	if err := cat.Start(parent); err != nil {
		_ = cat.Close()
		return nil, fmt.Errorf("runtimebundle: model catalog runtime start: %w", err)
	}

	var refreshWG sync.WaitGroup
	var refreshCleanup func()
	if mc.ExternalUpdatesEnabled && interval > 0 {
		cat.RunRefresh(parent)
		refreshCtx, cancel := context.WithCancel(parent)
		runModelCatalogRefreshLoop(refreshCtx, cat, interval, &refreshWG)
		refreshCleanup = func() {
			cancel()
			refreshWG.Wait()
		}
	}

	if closers != nil {
		*closers = append(*closers, cat.Close)
		if refreshCleanup != nil {
			*closers = append(*closers, func() error {
				refreshCleanup()
				return nil
			})
		}
	} else if refreshCleanup != nil {
		refreshCleanup()
	}

	if mc.Enabled {
		sizeEstimator := modelcatalog.DefaultSizeEstimator{}
		ovr := modelcatalog.NewOverrideResolver(OverrideSetFromModelCatalog(mc))
		exec.CatalogResolver = modelcatalog.NewCatalogResolver(
			nil,
			ovr,
			true,
			cat,
		)
		exec.EligibilityResolver = modelcatalog.NewEligibilityResolver(sizeEstimator)
		exec.RequestTokenEstimator = sizeEstimator
	}
	return cat, nil
}
