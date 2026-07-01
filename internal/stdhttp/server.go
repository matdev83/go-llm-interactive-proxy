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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	ssessiondiag "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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

// preparedStandardHTTP is the mux plus outer middleware stack used by [RunWithRuntime] and
// [NewStandardHandler] before any TCP listener is bound.
type preparedStandardHTTP struct {
	Handler        http.Handler
	releaseClosers func()
}

// prepareStandardHandler mounts diagnostics, frontends, and stacks outer HTTP middleware.
// On error it invokes resource closers for any partial setup. On success the caller must run
// app shutdown, then releaseClosers (see [RunWithRuntime], [NewStandardHandler]).
func prepareStandardHandler(
	ctx context.Context,
	cfg *config.Config,
	app *runtime.App,
	log *slog.Logger,
	built *runtimebundle.Built,
) (preparedStandardHTTP, error) {
	var out preparedStandardHTTP
	exec := built.Executor
	store := built.Store
	closers := built.Closers
	var closersOnce sync.Once
	releaseClosers := func() {
		closersOnce.Do(func() {
			runClosers(log, closers)
		})
	}
	out.releaseClosers = releaseClosers

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
			return out, fmt.Errorf("stdhttp: observability.metrics.enabled requires built.Metrics from runtimebundle.Build")
		}
		promReg := built.Metrics.Registry
		httpProm = built.Metrics.HTTP
		mp := strings.TrimSpace(cfg.Observability.Metrics.Path)
		if mp == "" {
			mp = "/metrics"
		}
		om := cfg.Observability.Metrics.ExemplarsEnabled
		mux.Handle(mp, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, metrics.MetricsHandler(promReg, om)))
		log.InfoContext(ctx, "prometheus metrics mounted", "path", mp, "open_metrics", om)
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
				return out, fmt.Errorf("stdhttp: attempts handler: %w", err)
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
				return out, fmt.Errorf("stdhttp: inventory handler: %w", err)
			}
			mux.Handle(ip, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ih))
		}
		rt := strings.TrimSpace(cfg.Diagnostics.RouteTracePath)
		if rt != "" {
			traceBuf := diag.NewRouteTraceBuffer(64)
			exec.RouteTrace = traceBuf
			rh, err := diag.RouteTraceHandler(traceBuf, log)
			if err != nil {
				releaseClosers()
				return out, fmt.Errorf("stdhttp: route trace handler: %w", err)
			}
			mux.Handle(rt, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, rh))
		}
		pp := strings.TrimSpace(cfg.Diagnostics.PprofPath)
		if pp != "" {
			if h := diag.PprofHandler(pp); h != nil {
				prefix := strings.TrimSuffix(pp, "/") + "/"
				mux.Handle(prefix, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
				log.InfoContext(ctx, "diagnostics pprof mounted", "path", prefix)
			}
		}
	}
	if cfg.Accounting.Admin.Enabled {
		path := strings.TrimSpace(cfg.Accounting.Admin.Path)
		if path != "" {
			service := built.TokenAccountingAdmin
			if service == nil && built.Executor != nil {
				service = built.Executor.AdminCountService
			}
			h := adminaccounting.NewHandler(adminaccounting.Options{
				Enabled:      true,
				MaxBodyBytes: cfg.Accounting.Admin.MaxBodyBytes,
				Service:      service,
			})
			mux.Handle(path, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
			log.InfoContext(ctx, "token accounting admin mounted", "path", path)
		}
	}
	secureOn := cfg.SecureSessionEffectivelyEnabled()
	exposeSummaries := cfg.SecureSession.DiagnosticsExposeSummaries
	if secureOn && exposeSummaries && built.SecureSessionStore != nil {
		p := strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix)
		if p == "" {
			releaseClosers()
			return out, fmt.Errorf(
				"stdhttp: secure_session diagnostics_expose_summaries requires " +
					"secure_session.diagnostics_path_prefix",
			)
		}
		base := strings.TrimSuffix(p, "/")
		ssh, err := ssessiondiag.NewHandler(
			base,
			built.SecureSessionStore,
			cfg.SecureSession.RedactionDefault,
			nil,
			log,
		)
		if err != nil {
			releaseClosers()
			return out, fmt.Errorf("stdhttp: secure-session diagnostics handler: %w", err)
		}
		dh := diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ssh)
		mux.Handle("GET "+base+"/", dh)
		mux.Handle("GET "+base, dh)
		log.InfoContext(ctx, "secure-session diagnostics mounted", "path", base)
	}
	mountModelCatalogDiagnostics(modelCatalogDiagnosticsMount{
		LogCtx: ctx,
		Mux:    mux,
		Cfg:    cfg,
		Log:    log,
		Built:  built,
	})
	maxBody := cfg.Server.EffectiveMaxRequestBodyBytes()
	preReqKA := cfg.Server.EffectivePreRequestKeepalive()
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
		RoutePrefixes:        built.RoutePrefixes,
		Plugins:              cfg.Plugins.Frontends,
		MaxRequestBodyBytes:  maxBody,
		Reg:                  reg,
		TrafficPorts:         trafficPorts,
		PreRequestKeepalive: lipsdk.FrontendKeepaliveConfig{
			Enabled:  preReqKA.Enabled,
			Interval: preReqKA.Interval,
		},
	}); err != nil {
		releaseClosers()
		return out, fmt.Errorf("stdhttp: mount frontends: %w", err)
	}
	if err := app.Start(ctx); err != nil {
		releaseClosers()
		return out, fmt.Errorf("stdhttp: start app: %w", err)
	}

	traceGen := diag.NewTraceIDGenerator()
	out.Handler = stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Built: built, TraceGen: traceGen, Inner: mux, HTTPProm: httpProm,
	})
	return out, nil
}

// NewStandardHandler returns the same composed [http.Handler] as [RunWithRuntime] uses for client
// requests (including [stackHTTPHandler] and bundled frontend mounts), without binding a listener.
// The cleanup function must be called when the handler is no longer needed; it shuts down app
// feature lifecycles then runs resource closers (same teardown ordering as serve shutdown).
func NewStandardHandler(
	ctx context.Context,
	cfg *config.Config,
	app *runtime.App,
	log *slog.Logger,
	built *runtimebundle.Built,
) (http.Handler, func(context.Context), error) {
	var releaseBuilt sync.Once
	if cfg == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil config")
	}
	if ctx == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil context")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, err
	}
	if app == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil app")
	}
	if log == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil logger")
	}
	if built == nil || built.Executor == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil built runtime")
	}
	if built.PluginRegistry == nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, errors.New("stdhttp: nil plugin registry in built runtime")
	}
	prep, err := prepareStandardHandler(ctx, cfg, app, log, built)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func(shutdownCtx context.Context) {
		app.Shutdown(shutdownCtx)
		releaseBuilt.Do(prep.releaseClosers)
	}
	return prep.Handler, cleanup, nil
}

// Run is a convenience wrapper: it calls [runtimebundle.Build] with cfg, app.HookBus(), log,
// and [runtimebundle.BuildOptions.PluginRegistry] set to reg, then [RunWithRuntime].
//
// Tracing is initialized with [context.Background] so bootstrap completes even when the caller ctx
// is already cancelled; shutdown still runs in this function's defer.
//
// Composition roots (for example the lipstd command in directory cmd/lipstd) should normally
// call [runtimebundle.Build] once themselves—so shared [runtimebundle.BuildOptions] (custom HTTP
// client, wire model, etc.) are explicit—and pass the resulting [*runtimebundle.Built] to
// [RunWithRuntime]. That avoids an extra assembly pass and matches how tests exercise the server.
func Run(ctx context.Context, cfg *config.Config, app *runtime.App, log *slog.Logger, reg *pluginreg.Registry) error {
	if ctx == nil {
		return errors.New("stdhttp: nil context")
	}
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
			log.WarnContext(ctx, "stdhttp: tracing shutdown", "error", err)
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

// modelCatalogDiagnosticsMount carries inputs for [mountModelCatalogDiagnostics].
type modelCatalogDiagnosticsMount struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
}

func mountModelCatalogDiagnostics(in modelCatalogDiagnosticsMount) {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	path := strings.TrimSpace(cfg.ModelCatalog.DiagnosticsPath)
	if path == "" {
		return
	}
	var rt *modelcatalog.CatalogRuntime
	if built != nil {
		rt = built.CatalogRuntime
	}
	var updateInterval time.Duration
	if d, ok := cfg.ModelCatalog.UpdateIntervalDuration(); ok {
		updateInterval = d
	}
	h := NewCatalogStatusHandler(log, modelcatalog.CatalogStatusHandlerConfig{
		Runtime:                rt,
		UsageEnabled:           cfg.ModelCatalog.Enabled,
		ExternalUpdatesEnabled: cfg.ModelCatalog.ExternalUpdatesEnabled,
		UpdateInterval:         updateInterval,
		SourceURL:              cfg.ModelCatalog.SourceURL,
	})
	mux.Handle(path, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
	if log != nil {
		log.InfoContext(logCtx, "model catalog diagnostics mounted", "path", path)
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
