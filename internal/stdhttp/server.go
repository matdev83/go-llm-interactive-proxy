package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// Run assembles the standard runtime and serves HTTP until ctx is cancelled, then shuts down.
// Prefer [RunWithRuntime] with a pre-built [runtimebundle.Built] from the composition root.
func Run(ctx context.Context, cfg *config.Config, app *runtime.App, log *slog.Logger, reg *pluginreg.Registry) error {
	if reg == nil {
		return errors.New("stdhttp: nil plugin registry")
	}
	if log == nil {
		return errors.New("stdhttp: nil logger")
	}
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if err != nil {
		return fmt.Errorf("stdhttp: build runtime: %w", err)
	}
	return RunWithRuntime(ctx, cfg, app, log, built)
}

// RunWithRuntime mounts bundled frontends and diagnostics, serves HTTP, then shuts down in order:
// stop HTTP server, app feature lifecycles, then resource closers.
func RunWithRuntime(ctx context.Context, cfg *config.Config, app *runtime.App, log *slog.Logger, built *runtimebundle.Built) error {
	if cfg == nil {
		return errors.New("stdhttp: nil config")
	}
	if app == nil {
		return errors.New("stdhttp: nil app")
	}
	if log == nil {
		return errors.New("stdhttp: nil logger")
	}
	if built == nil || built.Executor == nil {
		return errors.New("stdhttp: nil built runtime")
	}
	if built.PluginRegistry == nil {
		return errors.New("stdhttp: nil plugin registry in built runtime")
	}
	exec := built.Executor
	store := built.Store
	closers := built.Closers
	var closersOnce sync.Once
	releaseClosers := func() {
		closersOnce.Do(func() {
			runClosers(closers)
		})
	}
	route := DefaultRouteSelector(cfg)

	mux := http.NewServeMux()
	if cfg.Diagnostics.Enabled {
		hp := cfg.Diagnostics.HealthPath
		if hp == "" {
			hp = "/healthz"
		}
		mux.Handle(hp, diag.HealthHandler())
		ap := cfg.Diagnostics.AttemptsPath
		if ap != "" {
			ah, err := diag.AttemptsHandler(store)
			if err != nil {
				releaseClosers()
				return fmt.Errorf("stdhttp: attempts handler: %w", err)
			}
			mux.Handle(ap, ah)
		}
		ip := strings.TrimSpace(cfg.Diagnostics.InventoryPath)
		if ip != "" {
			ih, err := diag.InventoryHandler(cfg)
			if err != nil {
				releaseClosers()
				return fmt.Errorf("stdhttp: inventory handler: %w", err)
			}
			mux.Handle(ip, ih)
		}
		rt := strings.TrimSpace(cfg.Diagnostics.RouteTracePath)
		if rt != "" {
			traceBuf := diag.NewRouteTraceBuffer(64)
			exec.RouteTrace = traceBuf
			rh, err := diag.RouteTraceHandler(traceBuf)
			if err != nil {
				releaseClosers()
				return fmt.Errorf("stdhttp: route trace handler: %w", err)
			}
			mux.Handle(rt, rh)
		}
	}
	reg := built.PluginRegistry
	maxBody := cfg.Server.EffectiveMaxRequestBodyBytes()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 exec,
		DefaultRouteSelector: route,
		Plugins:              cfg.Plugins.Frontends,
		MaxRequestBodyBytes:  maxBody,
		Reg:                  reg,
	}); err != nil {
		releaseClosers()
		return fmt.Errorf("stdhttp: mount frontends: %w", err)
	}
	if err := app.Start(ctx); err != nil {
		releaseClosers()
		return fmt.Errorf("stdhttp: start app: %w", err)
	}

	traceGen := diag.NewTraceIDGenerator()
	handler := corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(traceGen, mux))

	srv := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Info("listening", "addr", cfg.Server.Address)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		app.Shutdown(shutdownCtx)
		releaseClosers()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			releaseClosers()
			return nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		app.Shutdown(shutdownCtx)
		cancel()
		releaseClosers()
		return fmt.Errorf("stdhttp: serve: %w", err)
	}
}

func runClosers(closers []func() error) {
	for i := len(closers) - 1; i >= 0; i-- {
		if closers[i] != nil {
			_ = closers[i]()
		}
	}
}
