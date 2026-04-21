package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

// Run mounts bundled frontends and diagnostics, serves HTTP until ctx is cancelled, then shuts down.
func Run(ctx context.Context, cfg *config.Config, app *runtime.App, log *slog.Logger) error {
	if cfg == nil {
		return errors.New("stdhttp: nil config")
	}
	if app == nil {
		return errors.New("stdhttp: nil app")
	}
	if log == nil {
		log = slog.Default()
	}
	exec, store, err := BuildExecutor(cfg, app.HookBus())
	if err != nil {
		return err
	}
	route := DefaultRouteSelector(cfg)

	mux := http.NewServeMux()
	var traceBuf *diag.RouteTraceBuffer
	if cfg.Diagnostics.Enabled {
		hp := cfg.Diagnostics.HealthPath
		if hp == "" {
			hp = "/healthz"
		}
		mux.Handle(hp, diag.HealthHandler())
		ap := cfg.Diagnostics.AttemptsPath
		if ap != "" {
			mux.Handle(ap, diag.AttemptsHandler(store))
		}
		ip := strings.TrimSpace(cfg.Diagnostics.InventoryPath)
		if ip != "" {
			mux.Handle(ip, diag.InventoryHandler(cfg))
		}
		rt := strings.TrimSpace(cfg.Diagnostics.RouteTracePath)
		if rt != "" {
			traceBuf = diag.NewRouteTraceBuffer(64)
			exec.RouteTrace = traceBuf
			mux.Handle(rt, diag.RouteTraceHandler(traceBuf))
		}
	}
	maxBody := cfg.Server.EffectiveMaxRequestBodyBytes()
	if err := MountBundledFrontends(mux, exec, route, cfg.Plugins.Frontends, maxBody); err != nil {
		return err
	}
	if err := app.Start(ctx); err != nil {
		return err
	}

	handler := http.Handler(mux)
	if cfg.Diagnostics.Enabled {
		// Header X-Trace-ID first; RequestIDMiddleware fills context when absent.
		handler = corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(mux))
	}

	srv := &http.Server{
		Addr:    cfg.Server.Address,
		Handler: handler,
	}
	log.Info("listening", "addr", cfg.Server.Address)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		app.Shutdown(shutdownCtx)
		cancel()
		return err
	}
}
