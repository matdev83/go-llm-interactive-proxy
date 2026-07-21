package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// ComposeRequestPlane builds the complete standard request-plane http.Handler
// for one generation without binding a listener, without owning runtime.App,
// and without starting/stopping feature lifecycles (ledger owns those).
//
// This is the HandlerComposer injected into runtimebundle.CompileGeneration so
// runtimebundle never imports stdhttp (no import cycle).
func ComposeRequestPlane(ctx context.Context, plane runtimebundle.RequestPlane) (http.Handler, error) {
	if ctx == nil {
		return nil, errors.New("stdhttp: nil context")
	}
	cfg := plane.StackConfig()
	if cfg == nil {
		return nil, errors.New("stdhttp: nil request-plane config projection")
	}
	log := plane.Logger()
	if log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	exec := plane.Executor()
	if exec == nil {
		return nil, errors.New("stdhttp: nil executor")
	}
	reg := plane.PluginRegistry()
	if reg == nil {
		return nil, errors.New("stdhttp: nil plugin registry")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		return nil, err
	}

	built := requestPlaneAsBuilt(plane)
	route := strings.TrimSpace(plane.Routing().DefaultRoute)
	if route == "" {
		route = DefaultRouteSelector(cfg)
	}

	mux := http.NewServeMux()

	httpProm, err := mountMetrics(mountMetricsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	if err != nil {
		return nil, err
	}

	if err := mountDiagnostics(mountDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built, Exec: exec, Reg: reg,
		Registrations: plane.Registrations(),
	}); err != nil {
		return nil, err
	}

	mountAccountingAdmin(mountAccountingAdminInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})

	if err := mountSecureSessionDiagnostics(mountSecureSessionDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	}); err != nil {
		return nil, err
	}

	mountModelCatalogDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	mountModelInventoryDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	mountAccountingAuthorityQuery(accountingAuthorityQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})
	mountControlPlaneQuery(controlPlaneQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Built: built,
	})

	maxBody := cfg.Server.EffectiveMaxRequestBodyBytes()
	decodeAdmission := plane.DecodeAdmission()
	preReqKA := cfg.Server.EffectivePreRequestKeepalive()
	var trafficPorts traffic.PortBundle
	if snap := plane.RuntimeSnapshot(); snap != nil {
		trafficPorts = traffic.PortBundle{
			Raw: snap.RawCapture(),
			Obs: snap.TrafficObserver(),
			Red: snap.TrafficRedactors(),
		}
	}
	frontends := plane.Frontends()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 exec,
		DefaultRouteSelector: route,
		RoutePrefixes:        plane.Routing().RoutePrefixes,
		Plugins:              frontends,
		MaxRequestBodyBytes:  maxBody,
		DecodeAdmission:      decodeAdmission,
		Reg:                  reg,
		TrafficPorts:         trafficPorts,
		PreRequestKeepalive: lipsdk.FrontendKeepaliveConfig{
			Enabled:  preReqKA.Enabled,
			Interval: preReqKA.Interval,
		},
	}); err != nil {
		return nil, fmt.Errorf("stdhttp: mount frontends: %w", err)
	}

	mux.Handle(openAIModelsPath, NewModelRegistryHandler(plane.ModelRegistryRuntime()))

	// Intentionally no runtime.App Start/Shutdown: feature lifecycles are owned
	// by the candidate resource ledger (singular Start/Stop).
	traceGen := diag.NewTraceIDGenerator()
	return stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Built: built, TraceGen: traceGen, Inner: mux, HTTPProm: httpProm,
	}), nil
}

// requestPlaneAsBuilt adapts RequestPlane into the legacy Built shape mounts
// still accept. Closers are nil: generation ledger owns teardown.
func requestPlaneAsBuilt(plane runtimebundle.RequestPlane) *runtimebundle.Built {
	return &runtimebundle.Built{
		Executor:              plane.Executor(),
		Store:                 plane.Store(),
		Closers:               nil,
		EffectiveDefaultRoute: plane.Routing().DefaultRoute,
		UpstreamHTTP:          plane.UpstreamHTTP(),
		RoutePrefixes:         plane.Routing().RoutePrefixes,
		DecodeAdmission:       plane.DecodeAdmission(),
		PluginRegistry:        plane.PluginRegistry(),
		Metrics:               plane.Metrics(),
		RuntimeSnapshot:       plane.RuntimeSnapshot(),
		HTTPAuthProviders:     plane.HTTPAuthProviders(),
		SecureSessionStore:    plane.SecureSessionStore(),
		AuthEventDispatcher:   plane.AuthEventDispatcher(),
		CatalogRuntime:        plane.CatalogRuntime(),
		ModelRegistry:         plane.ModelRegistry(),
		ModelRegistryRuntime:  plane.ModelRegistryRuntime(),
		TokenAccountingAdmin:  plane.TokenAccountingAdmin(),
		ControlPlaneQueries:   plane.ControlPlaneQueries(),
		ControlPlaneStatus:    plane.ControlPlaneStatus(),
		ControlPlaneRetention: plane.ControlPlaneRetention(),
		UsageAuthority:        plane.UsageAuthority(),
		ConcurrencyAuthority:  plane.ConcurrencyAuthority(),
		SnapshotGeneration:    plane.SnapshotGeneration(),
		SnapshotController:    plane.SnapshotController(),
		MeteringQuerier:       plane.MeteringQuerier(),
		ReadinessReport:       plane.ReadinessReport(),
		SecretGuardInventory:  plane.SecretGuardInventory(),
		TerminalWorkProcessor: plane.TerminalWorkProcessor(),
		TerminalWorkRegistry:  plane.TerminalWorkRegistry(),
		TerminalWorkQueries:   plane.TerminalWorkQueries(),
		TerminalWorkMetrics:   plane.TerminalWorkMetrics(),
	}
}
