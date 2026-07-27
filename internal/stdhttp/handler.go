package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

// prepareStandardHandler mounts metrics, diagnostics, admin, secure-session diagnostics,
// model-catalog diagnostics, control-plane query, and bundled frontends, then stacks the outer
// HTTP middleware. Mount order is load-bearing and preserved exactly.
//
// The focused composer accepts only [StandardHTTPInput]: it owns neither app start/shutdown nor
// resource closers. Callers ([ComposeStandardHTTP]) project focused groups and own lifecycle
// above this seam.
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
