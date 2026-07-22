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
	// ExplicitCandidate is true only when [GenerationCompileInput.Candidate] was
	// set by the caller (a genuine reload-candidate compile), as opposed to the
	// legacy single-shot [Build] path where Cfg is reused from ProcessServices.
	// Route/alias-vs-backend-set validation (req 9.2) and startup-only-field
	// classification (req 7.5) apply only to explicit candidates: the legacy
	// path has no "prior generation" to reload from and callers routinely
	// exercise unrelated subsystems with degenerate/placeholder routing.
	ExplicitCandidate bool
}
