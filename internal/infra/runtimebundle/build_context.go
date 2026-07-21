package runtimebundle

import (
	"context"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// buildContext bundles the fixed inputs every buildXRuntime unit receives. It is
// constructed once at the top of [Build] after validation and parent-context
// resolution, then passed to each unit. Units do not mutate bctx fields.
type buildContext struct {
	Cfg               *config.Config
	Bus               *hooks.Bus
	Log               *slog.Logger
	Opts              *BuildOptions
	Parent            context.Context
	PostgresPools     *db.PoolRegistry
	DualPlaneMigrator *dualPlaneMigrator
	// Ledger, when non-nil, receives generation-owned resources immediately
	// after acquisition (task 3.2). Process services never register here.
	Ledger *ResourceLedger
}
