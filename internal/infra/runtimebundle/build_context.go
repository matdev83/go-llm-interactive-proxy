package runtimebundle

import (
	"context"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
)

// buildContext bundles the fixed inputs every buildXRuntime unit receives. It is
// constructed once at the top of [Build] after validation and parent-context
// resolution, then passed to each unit. Units do not mutate bctx fields.
type buildContext struct {
	Cfg    *config.Config
	Bus    *hooks.Bus
	Log    *slog.Logger
	Opts   *BuildOptions
	Parent context.Context
}
