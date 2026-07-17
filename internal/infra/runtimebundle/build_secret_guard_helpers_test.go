package runtimebundle

import (
	"io"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// bindSecretGuardAudit is a test helper alias for buildSecretGuardRuntime with a
// discard logger fallback when log is nil.
func bindSecretGuardAudit(cfg *config.Config, opts *BuildOptions, regs []lipsdk.Registration, log *slog.Logger) (*secretGuardRuntime, error) {
	if opts == nil {
		return nil, nil
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	}
	return buildSecretGuardRuntime(cfg, log, opts, regs)
}
