package stdhttp

import (
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// BuildExecutor wires enabled backends from configuration into a core executor (standard bundle).
// Prefer [runtimebundle.Build] for tests and composition roots that need [runtimebundle.Built].
func BuildExecutor(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, reg *pluginreg.Registry) (*runtime.Executor, b2bua.Store, []func() error, error) {
	return runtimebundle.BuildExecutor(cfg, bus, log, reg)
}
