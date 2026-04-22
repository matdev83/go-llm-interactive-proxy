package runtime

import (
	"context"
	"errors"
	"log/slog"
	"time"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

var ErrNilConfig = errors.New("runtime config is required")

// Options carries bootstrap-only runtime dependencies for New.
//
// Config must be non-nil (otherwise ErrNilConfig). Logger nil selects slog.Default.
// Nil entries in Lifecycles are ignored by App.Start and App.Shutdown.
type Options struct {
	Config *coreconfig.Config
	Logger *slog.Logger

	// Registrations enumerates configured plugins at bootstrap. When non-empty or Mandatory
	// is set, duplicates and mandatory coverage are validated in New.
	Registrations []lipsdk.Registration
	Mandatory     []lipsdk.Requirement

	// Hooks configures submit, part, and tool-reactor chains (zero value means empty chains).
	Hooks hooks.Config

	// Lifecycles are started after validation and stopped on shutdown (reverse order).
	Lifecycles []lipplugin.Lifecycle
}

// App is the bootstrap composition root for the standard distribution.
type App struct {
	config        *coreconfig.Config
	logger        *slog.Logger
	registrations []lipsdk.Registration
	hookBus       *hooks.Bus
	lifecycles    []lipplugin.Lifecycle
}

// New validates bootstrap wiring without starting the HTTP server (see cmd/lipstd and stdhttp.Run).
// It does not validate coreconfig.Config field semantics; load YAML and run config.Validate upstream.
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
		lifecycles:    opts.Lifecycles,
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

// Start logs hook chain lengths and starts plugin lifecycles. The bundled HTTP server is started by stdhttp.Run from cmd/lipstd.
func (a *App) Start(ctx context.Context) error {
	ns, nrq, nrs, nt := a.HookBus().HookChainLengths()
	a.logger.Debug("runtime bootstrap",
		"server_address", a.config.Server.Address,
		"hook_submit", ns,
		"hook_request_parts", nrq,
		"hook_response_parts", nrs,
		"hook_tool_reactors", nt,
	)
	var started []lipplugin.Lifecycle
	for _, lc := range a.lifecycles {
		if lc == nil {
			continue
		}
		if err := lc.Start(ctx); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].Stop(stopCtx)
			}
			cancel()
			return err
		}
		started = append(started, lc)
	}
	return nil
}

// Shutdown stops plugin lifecycles in reverse registration order.
func (a *App) Shutdown(ctx context.Context) {
	if a == nil {
		return
	}
	for i := len(a.lifecycles) - 1; i >= 0; i-- {
		lc := a.lifecycles[i]
		if lc == nil {
			continue
		}
		_ = lc.Stop(ctx)
	}
}
