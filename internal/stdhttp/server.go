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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
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
	traceRes, err := tracing.Init(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("stdhttp: tracing init: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := traceRes.Shutdown(shutdownCtx); err != nil {
			log.Warn("stdhttp: tracing shutdown", "error", err)
		}
	}()
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{
		PluginRegistry:  reg,
		OutboundTracing: traceRes.Active,
	})
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
	reg := built.PluginRegistry

	mux := http.NewServeMux()

	var httpProm *metrics.HTTPMetrics
	if cfg.Observability.Metrics.Enabled {
		if built.Metrics == nil || built.Metrics.Registry == nil {
			releaseClosers()
			return fmt.Errorf("stdhttp: observability.metrics.enabled requires built.Metrics from runtimebundle.Build")
		}
		promReg := built.Metrics.Registry
		httpProm = built.Metrics.HTTP
		mp := strings.TrimSpace(cfg.Observability.Metrics.Path)
		if mp == "" {
			mp = "/metrics"
		}
		om := cfg.Observability.Metrics.ExemplarsEnabled
		mux.Handle(mp, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, metrics.MetricsHandler(promReg, om)))
		log.Info("prometheus metrics mounted", "path", mp, "open_metrics", om)
	}

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
			ih, err := diag.InventoryHandler(cfg, &diag.InventoryExtras{
				Reg:           reg,
				Registrations: app.Registrations(),
			})
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
	maxBody := cfg.Server.EffectiveMaxRequestBodyBytes()
	var trafficPorts traffic.PortBundle
	if built.RuntimeSnapshot != nil {
		trafficPorts = traffic.PortBundle{
			Raw: built.RuntimeSnapshot.RawCapture(),
			Obs: built.RuntimeSnapshot.TrafficObserver(),
			Red: built.RuntimeSnapshot.TrafficRedactors(),
		}
	}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 exec,
		DefaultRouteSelector: route,
		Plugins:              cfg.Plugins.Frontends,
		MaxRequestBodyBytes:  maxBody,
		Reg:                  reg,
		TrafficPorts:         trafficPorts,
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
	// Stack below builds outer → inner. After the next three lines: OpenTelemetry trace + request
	// ID, then access log, then transport auth, then the mux/frontend routes (R4: auth before decode to mux).
	// Optional prometheus/tracing layers below wrap the same core further outward.
	inner = stdauth.Middleware(log, built.HTTPAuthProviders, inner)
	inner = accessLogMiddleware(cfg, log, inner)
	inner = corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(traceGen, inner))
	if httpProm != nil {
		inner = httpProm.Middleware(inner)
	}
	if cfg.Observability.Tracing.Enabled {
		inner = tracing.HTTPMiddleware(true, inner)
	}
	handler := inner

	srv := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.EffectiveReadHeaderTimeout(),
		ReadTimeout:       cfg.Server.EffectiveReadTimeout(),
		WriteTimeout:      cfg.Server.EffectiveWriteTimeout(),
		IdleTimeout:       cfg.Server.EffectiveIdleTimeout(),
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
