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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	ssessiondiag "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// listenAndServe is the [http.Server.ListenAndServe] implementation (overridable in tests).
var listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }

// stackHTTPInput carries dependencies for [stackHTTPHandler] (same stack as [RunWithRuntime]).
type stackHTTPInput struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Built    *runtimebundle.Built
	TraceGen *diag.TraceIDGenerator
	Inner    http.Handler
	HTTPProm *metrics.HTTPMetrics

	// testOuterWrap, if non-nil, wraps the composed handler before the final outer recovery
	// middleware. Used only from stdhttp tests to simulate panics above inner recovery.
	testOuterWrap func(http.Handler) http.Handler
}

// stackHTTPHandler assembles the same middleware stack as [RunWithRuntime] (outer→inner: final
// outer recovery, optional OpenTelemetry HTTP, optional Prometheus, trace + request ID, access log,
// inner recovery, transport auth, route mux). Innermost is the shared [http.ServeMux] from mounting.
//
// Panic containment: [RecoveryMiddleware] remains between access logging and transport auth so
// access logs and HTTP metrics still observe inner handler panics as 5xx. [outerRecoveryMiddleware]
// wraps the full composed stack as a last resort for panics in outer layers (access log, metrics,
// tracing, or future outer wrappers).
func stackHTTPHandler(in stackHTTPInput) http.Handler {
	cfg, log, built, traceGen, inner, httpProm := in.Cfg, in.Log, in.Built, in.TraceGen, in.Inner, in.HTTPProm
	if built == nil {
		built = &runtimebundle.Built{}
	}
	h := stdauth.Middleware(log, built.HTTPAuthProviders, inner)
	h = RecoveryMiddleware(log, h)
	h = accessLogMiddleware(cfg, log, h)
	h = corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(traceGen, h))
	if httpProm != nil {
		h = httpProm.Middleware(h)
	}
	if cfg != nil && cfg.Observability.Tracing.Enabled {
		h = tracing.HTTPMiddleware(true, h)
	}
	if in.testOuterWrap != nil {
		h = in.testOuterWrap(h)
	}
	return outerRecoveryMiddleware(log, h)
}

// Run is a convenience wrapper: it calls [runtimebundle.Build] with cfg, app.HookBus(), log,
// and [runtimebundle.BuildOptions.PluginRegistry] set to reg, then [RunWithRuntime].
//
// Composition roots (for example the lipstd command in directory cmd/lipstd) should normally
// call [runtimebundle.Build] once themselves—so shared [runtimebundle.BuildOptions] (custom HTTP
// client, wire model, etc.) are explicit—and pass the resulting [*runtimebundle.Built] to
// [RunWithRuntime]. That avoids an extra assembly pass and matches how tests exercise the server.
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
func RunWithRuntime(
	ctx context.Context,
	cfg *config.Config,
	app *runtime.App,
	log *slog.Logger,
	built *runtimebundle.Built,
) error {
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
	if cfg.SecureSession.Enabled && cfg.SecureSession.DiagnosticsExposeSummaries && built.SecureSessionStore != nil {
		p := strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix)
		if p == "" {
			releaseClosers()
			return fmt.Errorf("stdhttp: secure_session diagnostics_expose_summaries requires secure_session.diagnostics_path_prefix")
		}
		base := strings.TrimSuffix(p, "/")
		ssh, err := ssessiondiag.NewHandler(base, built.SecureSessionStore, cfg.SecureSession.RedactionDefault, nil, log)
		if err != nil {
			releaseClosers()
			return fmt.Errorf("stdhttp: secure-session diagnostics handler: %w", err)
		}
		dh := diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ssh)
		mux.Handle("GET "+base+"/", dh)
		mux.Handle("GET "+base, dh)
		log.Info("secure-session diagnostics mounted", "path", base)
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
	// outer → inner: final outer panic recovery, optional OpenTelemetry HTTP, optional Prometheus,
	// trace + request ID, access log, inner request panic recovery, transport auth, then mux/frontend
	// routes (R4: auth after inner recovery).
	handler := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Built: built, TraceGen: traceGen, Inner: mux, HTTPProm: httpProm,
	})

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
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					pe := safety.Capture(safety.BoundaryWorker, "listen_and_serve", p)
					if log != nil {
						logCtx := context.Background()
						if ctx != nil {
							logCtx = context.WithoutCancel(ctx)
						}
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
		log.Warn("stdhttp: resource closer errors", "error", joined)
	}
}
