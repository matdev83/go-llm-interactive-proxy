package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
)

// Run mounts bundled frontends and diagnostics, serves HTTP until ctx is cancelled, then shuts down.
func Run(ctx context.Context, cfg *config.Config, bus *hooks.Bus, log *slog.Logger) error {
	if cfg == nil {
		return errors.New("stdhttp: nil config")
	}
	if log == nil {
		log = slog.Default()
	}
	exec, store, err := BuildExecutor(cfg, bus)
	if err != nil {
		return err
	}
	route := DefaultRouteSelector(cfg)

	mux := http.NewServeMux()
	if cfg.Diagnostics.Enabled {
		hp := cfg.Diagnostics.HealthPath
		if hp == "" {
			hp = "/health"
		}
		mux.Handle(hp, diag.HealthHandler())
		ap := cfg.Diagnostics.AttemptsPath
		if ap != "" {
			mux.Handle(ap, diag.AttemptsHandler(store))
		}
	}
	MountBundledFrontends(mux, exec, route)

	srv := &http.Server{
		Addr:    cfg.Server.Address,
		Handler: mux,
	}
	log.Info("listening", "addr", cfg.Server.Address)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
