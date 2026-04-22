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

// Run is a convenience wrapper: it calls [runtimebundle.Build] with cfg, app.HookBus(), log,
// and [runtimebundle.BuildOptions.PluginRegistry] set to reg, then [RunWithRuntime].
//
// Composition roots (for example the lipstd command in directory cmd/lipstd) should normally call [runtimebundle.Build] once
// themselves—so shared [runtimebundle.BuildOptions] (custom HTTP client, wire model, etc.) are
// explicit—and pass the resulting [*runtimebundle.Built] to [RunWithRuntime]. That avoids an
// extra assembly pass and matches how tests exercise the server.
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
			runClosers(log, closers)
		})
	}
	route := strings.TrimSpace(built.EffectiveDefaultRoute)
	if route == "" {
		route = DefaultRouteSelector(cfg)
	}

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
			mux.Handle(ap, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ah))
		}
		ip := strings.TrimSpace(cfg.Diagnostics.InventoryPath)
		if ip != "" {
			ih, err := diag.InventoryHandler(cfg)
			if err != nil {
				releaseClosers()
				return fmt.Errorf("stdhttp: inventory handler: %w", err)
			}
			mux.Handle(ip, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ih))
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
			mux.Handle(rt, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, rh))
		}
		pp := strings.TrimSpace(cfg.Diagnostics.PprofPath)
		if pp != "" {
			if h := diag.PprofHandler(pp); h != nil {
				prefix := strings.TrimSuffix(pp, "/") + "/"
				mux.Handle(prefix, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
				log.Info("diagnostics pprof mounted", "path", prefix)
			}
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
	inner := http.Handler(mux)
	inner = accessLogMiddleware(cfg, log, inner)
	handler := corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(traceGen, inner))

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
		if err := srv.Shutdown(shutdownCtx); err != nil && log != nil {
			log.Warn("stdhttp: http server shutdown", "error", err)
		}
		app.Shutdown(shutdownCtx)
		releaseClosers()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			releaseClosers()
			return nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
		releaseClosers()
		return fmt.Errorf("stdhttp: serve: %w", err)
	}
}

// runClosers invokes closers in reverse registration order. Errors are joined and logged once at
// warn (best-effort teardown; failures are not returned to the caller).
func runClosers(log *slog.Logger, closers []func() error) {
	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		if closers[i] == nil {
			continue
		}
		if err := closers[i](); err != nil {
			errs = append(errs, fmt.Errorf("closer %d: %w", i, err))
		}
	}
	if len(errs) == 0 {
		return
	}
	joined := errors.Join(errs...)
	if log != nil {
		log.Warn("stdhttp: resource closer errors", "error", joined)
	}
}
