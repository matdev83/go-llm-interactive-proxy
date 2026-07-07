package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// listenAndServe is the [http.Server.ListenAndServe] implementation (overridable in tests).
var listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }

// RunWithRuntime mounts bundled frontends and diagnostics, serves HTTP, then shuts down in order:
// stop HTTP server, app feature lifecycles, then resource closers.
func RunWithRuntime(
	ctx context.Context,
	cfg *config.Config,
	app *runtime.App,
	log *slog.Logger,
	built *runtimebundle.Built,
) error {
	var releaseBuilt sync.Once
	if cfg == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil config")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return fmt.Errorf("stdhttp: validate startup security: %w", err)
	}
	if app == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil app")
	}
	if log == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil logger")
	}
	if built == nil || built.Executor == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil built runtime")
	}
	if built.PluginRegistry == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil plugin registry in built runtime")
	}
	if ctx == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return errors.New("stdhttp: nil context")
	}
	prep, err := prepareStandardHandler(ctx, cfg, app, log, built)
	if err != nil {
		return fmt.Errorf("stdhttp: prepare standard handler: %w", err)
	}
	releaseClosers := func() { releaseBuilt.Do(prep.releaseClosers) }
	handler := prep.Handler

	srv := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.EffectiveReadHeaderTimeout(),
		ReadTimeout:       cfg.Server.EffectiveReadTimeout(),
		WriteTimeout:      cfg.Server.EffectiveWriteTimeout(),
		IdleTimeout:       cfg.Server.EffectiveIdleTimeout(),
	}
	log.InfoContext(ctx, "listening", "addr", cfg.Server.Address)

	errCh := make(chan error, 1)
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					pe := safety.Capture(safety.BoundaryWorker, "listen_and_serve", p)
					if log != nil {
						logCtx := context.WithoutCancel(ctx)
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						log.LogAttrs(logCtx, slog.LevelError, "stdhttp: isolated panic in listenAndServe worker", attrs...)
					}
					// [RunWithRuntime] wraps the channel result once with "stdhttp: serve".
					err = pe
				}
			}()
			return listenAndServe(srv)
		}()
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && log != nil {
			log.WarnContext(shutdownCtx, "stdhttp: http server shutdown", "error", err)
		}
		app.Shutdown(shutdownCtx)
		releaseClosers()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			app.Shutdown(shutdownCtx)
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

func releaseBuiltResources(log *slog.Logger, built *runtimebundle.Built, once *sync.Once) {
	if built == nil || once == nil {
		return
	}
	once.Do(func() { runClosers(log, built.Closers) })
}

// runClosers invokes closers in reverse registration order. Errors are joined and logged once at
// warn (best-effort teardown; failures are not returned to the caller).
func runClosers(log *slog.Logger, closers []func() error) {
	var errs []error
	logCtx := context.Background()
	for i := len(closers) - 1; i >= 0; i-- {
		if closers[i] == nil {
			continue
		}
		func(idx int) {
			defer func() {
				if p := recover(); p != nil {
					pe := safety.Capture(safety.BoundaryWorker, "resource_closer", p)
					if log != nil {
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						log.LogAttrs(logCtx, slog.LevelError, "stdhttp: isolated panic in resource closer", attrs...)
					}
				}
			}()
			if err := closers[idx](); err != nil {
				errs = append(errs, fmt.Errorf("closer %d: %w", idx, err))
			}
		}(i)
	}
	if len(errs) == 0 {
		return
	}
	joined := errors.Join(errs...)
	if log != nil {
		log.WarnContext(context.Background(), "stdhttp: resource closer errors", "error", joined)
	}
}
