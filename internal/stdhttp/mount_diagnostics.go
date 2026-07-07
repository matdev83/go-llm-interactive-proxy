package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// mountDiagnosticsInput carries inputs for [mountDiagnostics].
type mountDiagnosticsInput struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
	Exec   *runtime.Executor
	Reg    *pluginreg.Registry
	App    *runtime.App
}

// mountDiagnostics mounts health, attempts, inventory, route-trace, and pprof endpoints when
// diagnostics is enabled. Errors are returned with the same wrapping the inline block previously
// used so [RunWithRuntime]'s error chain (and tests asserting on it) stay identical.
func mountDiagnostics(in mountDiagnosticsInput) error {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	logCtx := in.LogCtx
	if !cfg.Diagnostics.Enabled {
		return nil
	}
	hp := cfg.Diagnostics.HealthPath
	if hp == "" {
		hp = "/healthz"
	}
	mux.Handle(hp, diag.HealthHandler())
	if ap := cfg.Diagnostics.AttemptsPath; ap != "" {
		ah, err := diag.AttemptsHandler(built.Store)
		if err != nil {
			return fmt.Errorf("stdhttp: attempts handler: %w", err)
		}
		mux.Handle(ap, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ah))
	}
	if ip := strings.TrimSpace(cfg.Diagnostics.InventoryPath); ip != "" {
		ih, err := diag.InventoryHandler(cfg, &diag.InventoryExtras{
			Reg:           in.Reg,
			Registrations: in.App.Registrations(),
		})
		if err != nil {
			return fmt.Errorf("stdhttp: inventory handler: %w", err)
		}
		mux.Handle(ip, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, ih))
	}
	if rt := strings.TrimSpace(cfg.Diagnostics.RouteTracePath); rt != "" {
		traceBuf := diag.NewRouteTraceBuffer(64)
		in.Exec.RouteTrace = traceBuf
		rh, err := diag.RouteTraceHandler(traceBuf, log)
		if err != nil {
			return fmt.Errorf("stdhttp: route trace handler: %w", err)
		}
		mux.Handle(rt, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, rh))
	}
	if pp := strings.TrimSpace(cfg.Diagnostics.PprofPath); pp != "" {
		if h := diag.PprofHandler(pp); h != nil {
			prefix := strings.TrimSuffix(pp, "/") + "/"
			mux.Handle(prefix, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
			log.InfoContext(logCtx, "diagnostics pprof mounted", "path", prefix)
		}
	}
	return nil
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
