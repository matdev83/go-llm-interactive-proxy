package runtimebundle

import (
	"context"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// loadCodexModelCatalog resolves the shared Codex model catalog once at startup:
// `codex debug models` when enabled, else (or on failure) the shipped/override
// fallback snapshot. It always returns a non-nil catalog when the shipped
// snapshot is parseable, so the codex connectors never hardcode slugs. The
// returned source is logged for operator visibility.
func loadCodexModelCatalog(parent context.Context, cfg *config.Config, log *slog.Logger) (*codexcatalog.Catalog, codexcatalog.Source) {
	opts := codexcatalog.LoadOptions{
		Enabled:         cfg.CodexModelCatalog.EffectiveEnabled(),
		FallbackPath:    strings.TrimSpace(cfg.CodexModelCatalog.FallbackPath),
		CodexBinaryPath: strings.TrimSpace(cfg.CodexModelCatalog.CodexBinaryPath),
		Timeout:         cfg.CodexModelCatalog.TimeoutDuration(),
	}
	cat, src, err := codexcatalog.Load(parent, opts)
	if err == nil && cat != nil {
		log.Info("codex model catalog loaded",
			slog.String("source", string(src)),
			slog.Int("routable_models", len(cat.RoutableSlugs())),
		)
		return cat, src
	}
	if err != nil {
		log.Warn("codex model catalog load failed; using shipped fallback",
			slog.String("error", err.Error()),
		)
	}
	// Last resort: the shipped embedded snapshot.
	if cat, ferr := codexcatalog.LoadFallback(""); ferr == nil {
		log.Info("codex model catalog loaded from shipped fallback",
			slog.Int("routable_models", len(cat.RoutableSlugs())),
		)
		return cat, codexcatalog.SourceShippedFallback
	}
	log.Warn("codex model catalog unavailable; connectors will lazy-load the shipped snapshot")
	return nil, codexcatalog.SourceUnknown
}
