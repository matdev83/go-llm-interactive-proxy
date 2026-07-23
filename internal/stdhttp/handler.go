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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// prepareStandardHandler mounts metrics, diagnostics, admin, secure-session diagnostics,
// model-catalog diagnostics, control-plane query, and bundled frontends, then stacks the outer
// HTTP middleware. Mount order is load-bearing and preserved exactly.
//
// The focused composer accepts only [StandardHTTPInput]: it owns neither app start/shutdown nor
// resource closers. Callers ([NewStandardHandler], [RunWithRuntime], [ComposeStandardHTTP]) project
// broad sources into groups and own lifecycle above this seam.
func prepareStandardHandler(
	ctx context.Context,
	cfg *config.Config,
	log *slog.Logger,
	in StandardHTTPInput,
) (http.Handler, error) {
	mux := http.NewServeMux()

	httpProm, err := mountMetrics(mountMetricsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Operations: in.Operations,
	})
	if err != nil {
		return nil, err
	}

	if err := mountDiagnostics(mountDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Operations: in.Operations, Core: in.Core, Reg: in.Frontends.Registry,
	}); err != nil {
		return nil, err
	}

	mountAccountingAdmin(mountAccountingAdminInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Operations: in.Operations, Core: in.Core,
	})

	if err := mountSecureSessionDiagnostics(mountSecureSessionDiagnosticsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Security: in.Security,
	}); err != nil {
		return nil, err
	}

	mountModelCatalogDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Models: in.Models,
	})
	mountModelInventoryDiagnostics(ctx, diagnosticsMount{
		Mux: mux, Cfg: cfg, Log: log, Models: in.Models,
	})
	mountAccountingAuthorityQuery(accountingAuthorityQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log,
		Security: in.Security, Core: in.Core,
	})
	mountControlPlaneQuery(controlPlaneQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: log, Operations: in.Operations,
	})

	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:       mux,
		Frontends: in.Frontends,
	}); err != nil {
		return nil, fmt.Errorf("stdhttp: mount frontends: %w", err)
	}

	if err := callMount(func() error {
		mux.Handle(openAIModelsPath, NewModelRegistryHandler(in.Models.ModelRegistryRuntime))
		return nil
	}); err != nil {
		return nil, err
	}

	traceGen := diag.NewTraceIDGenerator()
	return stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Security: in.Security, TraceGen: traceGen, Inner: mux, HTTPProm: httpProm,
	}), nil
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
	input := standardHTTPInputFromBuilt(built, cfg, app.Registrations())
	handler, err := prepareStandardHandler(ctx, cfg, log, input)
	if err != nil {
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, err
	}
	if err := app.Start(ctx); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
		releaseBuiltResources(log, built, &releaseBuilt)
		return nil, nil, fmt.Errorf("stdhttp: start app: %w", err)
	}
	cleanup := func(shutdownCtx context.Context) {
		app.Shutdown(shutdownCtx)
		releaseBuilt.Do(func() { runClosers(log, built.Closers) })
	}
	return handler, cleanup, nil
}
