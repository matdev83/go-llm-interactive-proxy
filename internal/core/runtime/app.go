package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

var ErrNilConfig = errors.New("runtime: config is required")

// ErrNilLogger is returned by [New] when Options.Logger is nil.
var ErrNilLogger = errors.New("runtime: logger is required")

const (
	lifecycleStartOp = "lifecycle_start"
	lifecycleStopOp  = "lifecycle_stop"
)

// Options carries bootstrap-only runtime dependencies for New.
//
// We use an explicit struct rather than functional options: the surface is small, fields are
// easy to read at call sites, and the project prefers explicit construction over indirection
// (see repository AGENTS.md). Add fields here as needed instead of a generic options closure chain.
//
// Config must be non-nil (otherwise ErrNilConfig). Logger must be non-nil (otherwise ErrNilLogger).
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
			return nil, fmt.Errorf("runtime: validate registrations: %w", err)
		}
	}

	if opts.Logger == nil {
		return nil, ErrNilLogger
	}
	logger := opts.Logger

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
	return a.hookBus
}

// Registrations returns a copy of bootstrap plugin registrations (may be empty).
func (a *App) Registrations() []lipsdk.Registration {
	if a == nil {
		return nil
	}
	out := make([]lipsdk.Registration, len(a.registrations))
	copy(out, a.registrations)
	return out
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
	started := []lipplugin.Lifecycle{}
	for _, lc := range a.lifecycles {
		if lc == nil {
			continue
		}
		err := safety.Call(safety.BoundaryWorker, lifecycleStartOp, func() error {
			return lc.Start(ctx)
		})
		if err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			a.stopLifecyclesAfterStartFailure(ctx, stopCtx, started)
			return fmt.Errorf("runtime: lifecycle start: %w", err)
		}
		started = append(started, lc)
	}
	return nil
}

func (a *App) stopLifecyclesAfterStartFailure(logCtx, stopCtx context.Context, started []lipplugin.Lifecycle) {
	for i := len(started) - 1; i >= 0; i-- {
		idx := i
		err := safety.Call(safety.BoundaryWorker, lifecycleStopOp, func() error {
			return started[idx].Stop(stopCtx)
		})
		if err != nil {
			a.logLifecycleStopAfterStartFailure(logCtx, idx, err)
		}
	}
}

func (a *App) logLifecycleStopAfterStartFailure(ctx context.Context, index int, err error) {
	if a == nil || a.logger == nil || err == nil {
		return
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) && pe != nil {
		attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{})
		attrs = append(attrs, slog.Int("lifecycle_index", index))
		attrs = diag.AppendIsolatedCrashStack(attrs, pe)
		a.logger.LogAttrs(ctx, slog.LevelError, "runtime: lifecycle stop after start failure (isolated panic)", attrs...)
		return
	}
	a.logger.Warn("runtime: lifecycle stop after start failure", "index", index, "error", err)
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
		idx := i
		err := safety.Call(safety.BoundaryWorker, lifecycleStopOp, func() error {
			return lc.Stop(ctx)
		})
		if err != nil {
			a.logShutdownLifecycleStop(ctx, idx, err)
		}
	}
}

func (a *App) logShutdownLifecycleStop(ctx context.Context, index int, err error) {
	if a == nil || a.logger == nil || err == nil {
		return
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) && pe != nil {
		attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{})
		attrs = append(attrs, slog.Int("lifecycle_index", index))
		attrs = diag.AppendIsolatedCrashStack(attrs, pe)
		a.logger.LogAttrs(ctx, slog.LevelError, "runtime: lifecycle stop failed (isolated panic)", attrs...)
		return
	}
	a.logger.Warn("runtime: lifecycle stop failed", "index", index, "error", err)
}
