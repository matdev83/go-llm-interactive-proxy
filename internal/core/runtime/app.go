package runtime

import (
	"context"
	"errors"
	"log/slog"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

var ErrNilConfig = errors.New("runtime config is required")

// Options carries bootstrap-only runtime dependencies.
type Options struct {
	Config *coreconfig.Config
	Logger *slog.Logger

	// Registrations enumerates configured plugins at bootstrap. When non-empty or Mandatory
	// is set, duplicates and mandatory coverage are validated before Start.
	Registrations []lipsdk.Registration
	Mandatory     []lipsdk.Requirement

	// Hooks configures submit, part, and tool-reactor chains (zero value means empty chains).
	Hooks hooks.Config
}

// App is the bootstrap composition root for the standard distribution.
type App struct {
	config        *coreconfig.Config
	logger        *slog.Logger
	registrations []lipsdk.Registration
	hookBus       *hooks.Bus
}

// New validates bootstrap wiring without starting the HTTP server (see cmd/lipstd and stdhttp.Run).
func New(opts Options) (*App, error) {
	if opts.Config == nil {
		return nil, ErrNilConfig
	}

	if len(opts.Mandatory) > 0 || len(opts.Registrations) > 0 {
		if err := lipsdk.ValidateRegistrations(opts.Registrations, opts.Mandatory); err != nil {
			return nil, err
		}
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &App{
		config:        opts.Config,
		logger:        logger,
		registrations: opts.Registrations,
		hookBus:       hooks.New(opts.Hooks),
	}, nil
}

// HookBus returns the configured hook bus (never nil after New).
func (a *App) HookBus() *hooks.Bus {
	if a == nil {
		return hooks.New(hooks.Config{})
	}
	if a.hookBus == nil {
		return hooks.New(hooks.Config{})
	}
	return a.hookBus
}

// Start logs hook chain lengths for diagnostics. The bundled HTTP server is started by stdhttp.Run from cmd/lipstd.
func (a *App) Start(context.Context) error {
	ns, nrq, nrs, nt := a.HookBus().HookChainLengths()
	a.logger.Debug("runtime bootstrap",
		"server_address", a.config.Server.Address,
		"hook_submit", ns,
		"hook_request_parts", nrq,
		"hook_response_parts", nrs,
		"hook_tool_reactors", nt,
	)
	return nil
}
