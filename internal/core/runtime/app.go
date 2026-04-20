package runtime

import (
	"context"
	"errors"
	"log/slog"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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
}

// App is the bootstrap composition root for the future runtime.
type App struct {
	config        *coreconfig.Config
	logger        *slog.Logger
	registrations []lipsdk.Registration
}

// New validates bootstrap wiring without implementing runtime behavior.
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
	}, nil
}

// Start currently validates the scaffold only.
func (a *App) Start(context.Context) error {
	a.logger.Debug("runtime bootstrap start", "server_address", a.config.Server.Address)
	return nil
}
