package runtimebundle

import (
	"context"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// Factory IDs that consume the shared Codex model catalog resolved at startup.
// Kept as composition-root strings so runtimebundle does not import concrete
// backend plugin packages.
const (
	codexCatalogConsumerOpenAICodex    = "openai-codex"
	codexCatalogConsumerCodexAppServer = "openai-codex-app-server"
)

// loadCodexModelCatalog resolves the shared Codex model catalog once at startup:
// `codex debug models` when enabled, else (or on failure) the shipped/override
// fallback snapshot. Discovery runs only in single_user mode when at least one
// enabled, registered Codex consumer backend is loaded (openai-codex and/or
// openai-codex-app-server). multi_user and no-consumer startups skip discovery
// and do not advertise a composition-root fallback catalog.
func loadCodexModelCatalog(parent context.Context, cfg *config.Config, reg *pluginreg.Registry, log *slog.Logger, loadFn CodexCatalogLoadFunc) (*codexcatalog.Catalog, codexcatalog.Source) {
	if loadFn == nil {
		loadFn = codexcatalog.Load
	}
	if !shouldLoadCodexModelCatalog(cfg, reg, log) {
		return nil, codexcatalog.SourceUnknown
	}
	opts := codexcatalog.LoadOptions{
		Enabled:         cfg.CodexModelCatalog.EffectiveEnabled(),
		FallbackPath:    strings.TrimSpace(cfg.CodexModelCatalog.FallbackPath),
		CodexBinaryPath: strings.TrimSpace(cfg.CodexModelCatalog.CodexBinaryPath),
		Timeout:         cfg.CodexModelCatalog.TimeoutDuration(),
	}
	cat, src, err := loadFn(parent, opts)
	if err == nil && cat != nil {
		if log != nil {
			log.Info(
				"codex model catalog loaded",
				slog.String("source", string(src)),
				slog.Int("routable_models", len(cat.RoutableSlugs())),
			)
		}
		return cat, src
	}
	if err != nil {
		if log != nil {
			log.Warn(
				"codex model catalog load failed; using shipped fallback",
				slog.String("error", err.Error()),
			)
		}
	}
	// Last resort: the shipped embedded snapshot.
	if cat, ferr := codexcatalog.LoadFallback(""); ferr == nil {
		if log != nil {
			log.Info(
				"codex model catalog loaded from shipped fallback",
				slog.Int("routable_models", len(cat.RoutableSlugs())),
			)
		}
		return cat, codexcatalog.SourceShippedFallback
	} else if log != nil {
		log.Warn(
			"codex model catalog unavailable; connectors will lazy-load the shipped snapshot",
			slog.String("error", ferr.Error()),
		)
	}
	return nil, codexcatalog.SourceUnknown
}

func shouldLoadCodexModelCatalog(cfg *config.Config, reg *pluginreg.Registry, log *slog.Logger) bool {
	if cfg == nil {
		return false
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		if log != nil {
			log.Warn(
				"codex model catalog skipped",
				slog.String("reason", "access_mode_unavailable"),
				slog.String("error", err.Error()),
			)
		}
		return false
	}
	if mode != accessmode.ModeSingleUser {
		if log != nil {
			log.Info(
				"codex model catalog skipped",
				slog.String("reason", "access_mode_not_single_user"),
				slog.String("access_mode", string(mode)),
			)
		}
		return false
	}
	if !hasEnabledRegisteredCodexCatalogConsumer(cfg, reg) {
		if log != nil {
			log.Info(
				"codex model catalog skipped",
				slog.String("reason", "no_enabled_registered_codex_catalog_consumer"),
			)
		}
		return false
	}
	return true
}

func hasEnabledRegisteredCodexCatalogConsumer(cfg *config.Config, reg *pluginreg.Registry) bool {
	if cfg == nil || reg == nil {
		return false
	}
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		switch fid {
		case codexCatalogConsumerOpenAICodex, codexCatalogConsumerCodexAppServer:
			if reg.HasBackend(fid) {
				return true
			}
		}
	}
	return false
}
