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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// preparedStandardHTTP is the mux plus outer middleware stack used by [RunWithRuntime] and
// [NewStandardHandler] before any TCP listener is bound.
type preparedStandardHTTP struct {
	Handler        http.Handler
	releaseClosers func()
}

// prepareStandardHandler mounts metrics, diagnostics, admin, secure-session diagnostics,
// model-catalog diagnostics, control-plane query, and bundled frontends, then stacks the outer
// HTTP middleware. Mount order is load-bearing and preserved exactly.
//
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

	httpProm, err := mountMetrics(mountMetricsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	if err != nil {
		releaseClosers()
		return out, err
	}

	if err := mountDiagnostics(mountDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built, Exec: exec, Reg: reg, App: app,
	}); err != nil {
		releaseClosers()
		return out, err
	}

	mountAccountingAdmin(mountAccountingAdminInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})

	if err := mountSecureSessionDiagnostics(mountSecureSessionDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	}); err != nil {
		releaseClosers()
		return out, err
	}

	mountModelCatalogDiagnostics(modelCatalogDiagnosticsMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	mountControlPlaneQuery(controlPlaneQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
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
