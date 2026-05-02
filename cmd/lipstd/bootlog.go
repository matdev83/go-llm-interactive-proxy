package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// logBootstrapAccessAuth returns after emitting structured boot diagnostics for effective access mode
// and merged audit auth labels. On [config.Config.EffectiveAccessMode] error it returns a wrapped
// error only (caller should log once).
func logBootstrapAccessAuth(ctx context.Context, log *slog.Logger, cfg *config.Config) error {
	accessMode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("lipstd: bootstrap access/auth: %w", err)
	}
	effHandler, effLevel := cfg.EffectiveAuthForAudit()
	log.InfoContext(ctx, "lipstd: effective access and authentication",
		"access_mode", string(accessMode),
		"listen_address", cfg.Server.Address,
		"auth_handler", effHandler,
		"auth_required_level", effLevel,
	)
	return nil
}
