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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	// Registrations is used when App is nil (generation path).
	Registrations []lipsdk.Registration
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
		regs := in.Registrations
		if in.App != nil {
			regs = in.App.Registrations()
		}
		ih, err := diag.InventoryHandler(cfg, mergeInventoryExtrasForDiagnostics(in.Reg, regs, in.Built.SecretGuardInventory))
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

// diagnosticsMount carries non-context inputs for diagnostics mount functions.
// Callers pass context.Context as an explicit parameter (never stored).
type diagnosticsMount struct {
	Mux   *http.ServeMux
	Cfg   *config.Config
	Log   *slog.Logger
	Built *runtimebundle.Built
}

func mountModelCatalogDiagnostics(ctx context.Context, in diagnosticsMount) {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
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
		log.InfoContext(ctx, "model catalog diagnostics mounted", "path", path)
	}
}

func mountModelInventoryDiagnostics(ctx context.Context, in diagnosticsMount) {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	if mux == nil || cfg == nil {
		return
	}
	path := strings.TrimSpace(cfg.ModelInventory.DiagnosticsPath)
	if path == "" {
		return
	}
	var rt *modelregistry.Runtime
	if built != nil {
		rt = built.ModelRegistryRuntime
	}
	mux.Handle(path, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, NewModelRegistryStatusHandler(rt)))
	if log != nil {
		log.InfoContext(ctx, "model inventory diagnostics mounted", "path", path)
	}
}
