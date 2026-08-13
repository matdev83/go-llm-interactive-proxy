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
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// mountDiagnosticsInput carries inputs for [mountDiagnostics].
type mountDiagnosticsInput struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
	Core       HTTPCoreInput
	Reg        *pluginreg.Registry
}

// mountDiagnostics mounts health, attempts, inventory, route-trace, and pprof endpoints when
// diagnostics is enabled. Errors are returned with the same wrapping the inline block previously
// used so ComposeStandardHTTP's error chain (and tests asserting on it) stay identical.
func mountDiagnostics(in mountDiagnosticsInput) error {
	mux, cfg, log, ops := in.Mux, in.Cfg, in.Log, in.Operations
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
		ah, err := diag.AttemptsHandler(ops.Store)
		if err != nil {
			return fmt.Errorf("stdhttp: attempts handler: %w", err)
		}
		mux.Handle(ap, wrapDiagnostics(cfg, ah))
	}
	if ip := strings.TrimSpace(cfg.Diagnostics.InventoryPath); ip != "" {
		ih, err := diag.InventoryHandler(cfg, mergeInventoryExtrasForDiagnostics(in.Reg, ops.Registrations, ops.SecretGuardInventory))
		if err != nil {
			return fmt.Errorf("stdhttp: inventory handler: %w", err)
		}
		mux.Handle(ip, wrapDiagnostics(cfg, ih))
	}
	if rt := strings.TrimSpace(cfg.Diagnostics.RouteTracePath); rt != "" {
		traceBuf := diag.NewRouteTraceBuffer(diag.DefaultRouteTraceCapacity)
		if in.Core.Executor != nil {
			in.Core.Executor.RouteTrace = traceBuf
		}
		rh, err := diag.RouteTraceHandler(traceBuf, log)
		if err != nil {
			return fmt.Errorf("stdhttp: route trace handler: %w", err)
		}
		mux.Handle(rt, wrapDiagnostics(cfg, rh))
	}
	if pp := strings.TrimSpace(cfg.Diagnostics.PprofPath); pp != "" {
		if h := diag.PprofHandler(pp); h != nil {
			prefix := strings.TrimSuffix(pp, "/") + "/"
			mux.Handle(prefix, wrapDiagnostics(cfg, h))
			log.InfoContext(logCtx, "diagnostics pprof mounted", "path", prefix)
		}
	}
	return nil
}

func mergeInventoryExtrasForDiagnostics(reg *pluginreg.Registry, registrations []lipsdk.Registration, secretGuard *diag.InventoryExtras) *diag.InventoryExtras {
	out := &diag.InventoryExtras{Reg: reg, Registrations: registrations}
	if secretGuard == nil {
		return out
	}
	out.SecretGuardCatalogEntryCount = secretGuard.SecretGuardCatalogEntryCount
	out.SecretGuardSourceCategories = append([]string(nil), secretGuard.SecretGuardSourceCategories...)
	out.SecretGuardAccessMode = secretGuard.SecretGuardAccessMode
	out.SecretGuardAction = secretGuard.SecretGuardAction
	return out
}

// diagnosticsMount carries non-context inputs for model diagnostics mounts.
// Callers pass context.Context as an explicit parameter (never stored).
type diagnosticsMount struct {
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Models HTTPModelInput
}

func mountModelCatalogDiagnostics(ctx context.Context, in diagnosticsMount) {
	mux, cfg, log, models := in.Mux, in.Cfg, in.Log, in.Models
	if mux == nil || cfg == nil {
		return
	}
	path := strings.TrimSpace(cfg.ModelCatalog.DiagnosticsPath)
	if path == "" {
		return
	}
	rt := models.CatalogRuntime
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
	mux.Handle(path, wrapDiagnostics(cfg, h))
	if log != nil {
		log.InfoContext(ctx, "model catalog diagnostics mounted", "path", path)
	}
}

func mountModelInventoryDiagnostics(ctx context.Context, in diagnosticsMount) {
	mux, cfg, log, models := in.Mux, in.Cfg, in.Log, in.Models
	if mux == nil || cfg == nil {
		return
	}
	path := strings.TrimSpace(cfg.ModelInventory.DiagnosticsPath)
	if path == "" {
		return
	}
	mux.Handle(path, wrapDiagnostics(cfg, NewModelRegistryStatusHandler(models.ModelRegistryRuntime)))
	if log != nil {
		log.InfoContext(ctx, "model inventory diagnostics mounted", "path", path)
	}
}
