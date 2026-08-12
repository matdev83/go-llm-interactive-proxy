package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	billingadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/billing"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
)

// mountAccountingAdminInput carries inputs for [mountAccountingAdmin].
type mountAccountingAdminInput struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
	Core       HTTPCoreInput
}

// mountAccountingAdmin mounts the token-accounting admin endpoint when accounting.admin.enabled
// is true and a path is configured. The handler is wrapped with the diagnostics shared-secret
// protection. Never returns an error: when disabled or misconfigured it simply mounts nothing.
// billingReportsMount carries the authoritative read-side billing port.
type billingReportsMount struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountBillingReports exposes only journal/TUR-backed, bounded billing reads.
// It is mounted separately from token telemetry and is protected by the existing
// diagnostics secret. No raw stream, metering, database, or provider payload is
// returned by this surface.
func mountBillingReports(in billingReportsMount) {
	if in.Mux == nil || in.Cfg == nil || in.Operations.BillingReports == nil {
		return
	}
	if strings.TrimSpace(in.Cfg.Diagnostics.SharedSecret) == "" {
		if in.Log != nil {
			in.Log.WarnContext(in.LogCtx, "billing report query disabled: diagnostics shared_secret is empty")
		}
		return
	}
	path := strings.TrimSuffix(strings.TrimSpace(in.Operations.BillingReportsPath), "/")
	if path == "" {
		path = "/admin/billing"
	}
	handler := billingadmin.NewHandler(billingadmin.Options{Queries: in.Operations.BillingReports})
	protected := diag.WrapDiagnosticsProtect(in.Cfg.Diagnostics.SharedSecret, http.StripPrefix(path, handler))
	in.Mux.Handle(path, protected)
	in.Mux.Handle(path+"/", protected)
	if in.Log != nil {
		in.Log.InfoContext(in.LogCtx, "authoritative billing reports mounted", "path", path)
	}
}

func mountAccountingAdmin(in mountAccountingAdminInput) {
	mux, cfg, log, ops, core := in.Mux, in.Cfg, in.Log, in.Operations, in.Core
	logCtx := in.LogCtx
	if !cfg.Accounting.Admin.Enabled {
		return
	}
	path := strings.TrimSpace(cfg.Accounting.Admin.Path)
	if path == "" {
		return
	}
	service := ops.TokenAccountingAdmin
	if service == nil && core.Executor != nil {
		service = adminaccounting.AdaptCountCallService(core.Executor.AdminCountService)
	}
	h := adminaccounting.NewHandler(adminaccounting.Options{
		Enabled:      true,
		MaxBodyBytes: cfg.Accounting.Admin.MaxBodyBytes,
		Service:      service,
	})
	mux.Handle(path, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, h))
	log.InfoContext(logCtx, "token accounting admin mounted", "path", path)
}

// controlPlaneQueryMount carries inputs for [mountControlPlaneQuery].
type controlPlaneQueryMount struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountControlPlaneQuery mounts the protected control-plane status and query
// routes only when control-plane query exposure is explicitly enabled and the
// diagnostics shared-secret posture allows it (task 5.3; requirements 2.1–2.9,
// 4.6, 4.7, 5.5, 7.1, 7.4, 8.6, 9.1, 9.4, 10.4, 10.5).
//
// The handler is wrapped with the existing diagnostics shared-secret protection
// so it never becomes a client-facing LLM protocol response path and never
// leaks raw infrastructure details. When the capability is disabled or query
// exposure is off, no route is mounted and the path returns 404.
func mountControlPlaneQuery(in controlPlaneQueryMount) {
	mux, cfg, log, ops := in.Mux, in.Cfg, in.Log, in.Operations
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.ControlPlaneQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(
				logCtx, "control-plane query config enabled but diagnostics shared_secret is empty; mounting disabled (query surface would be unauthenticated)",
				slog.String("component", "control_plane"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if ops.ControlPlaneQueries == nil {
		if log != nil {
			log.WarnContext(
				logCtx, "control-plane query config enabled but no query service wired; mounting disabled",
				slog.String("component", "control_plane"),
				slog.String("notice", "query_service_unavailable"),
			)
		}
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(cfg.ControlPlane.Query.PathPrefix), "/")
	if base == "" {
		return
	}
	handler := cpadmin.NewHandler(cpadmin.Options{
		Queries:         ops.ControlPlaneQueries,
		ReadinessReport: ops.ReadinessReport,
	})
	protected := diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, http.StripPrefix(base, handler))
	mux.Handle(base, protected)
	mux.Handle(base+"/", protected)
	if log != nil {
		log.InfoContext(logCtx, "control-plane query mounted", "path", base)
	}
}

// accountingAuthorityQueryMount carries inputs for [mountAccountingAuthorityQuery].
type accountingAuthorityQueryMount struct {
	LogCtx   context.Context
	Mux      *http.ServeMux
	Cfg      *config.Config
	Log      *slog.Logger
	Security HTTPSecurityInput
	Core     HTTPCoreInput
}

// mountAccountingAuthorityQuery mounts the protected authority status and
// bounded query routes only when the authority capability and diagnostics
// shared-secret posture are explicitly configured.
func mountAccountingAuthorityQuery(in accountingAuthorityQueryMount) {
	mux, cfg, log, sec, core := in.Mux, in.Cfg, in.Log, in.Security, in.Core
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.AuthorityQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(
				logCtx, "accounting authority query config enabled but diagnostics shared_secret is empty; mounting disabled",
				slog.String("component", "accounting_authority"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if sec.UsageAuthority == nil {
		if log != nil {
			log.WarnContext(
				logCtx, "accounting authority query config enabled but no authority service wired; mounting disabled",
				slog.String("component", "accounting_authority"),
				slog.String("notice", "authority_service_unavailable"),
			)
		}
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(cfg.Accounting.Authority.Query.PathPrefix), "/")
	if base == "" {
		return
	}
	accHandler := cpadmin.NewAccountingAuthorityHandler(cpadmin.AuthorityOptions{
		Queries:         sec.UsageAuthority,
		DefaultPageSize: cfg.Accounting.Authority.Query.DefaultPageSize,
		MaxPageSize:     cfg.Accounting.Authority.Query.MaxPageSize,
	})
	handler := http.Handler(accHandler)
	if core.Executor != nil && core.Executor.ConcurrencyProvider != nil {
		leaseHandler := cpadmin.NewConcurrencyAuthorityHandler(cpadmin.ConcurrencyOptions{
			Provider:        core.Executor.ConcurrencyProvider,
			Service:         sec.ConcurrencyAuthority,
			DefaultPageSize: cfg.Accounting.Authority.Query.DefaultPageSize,
			MaxPageSize:     cfg.Accounting.Authority.Query.MaxPageSize,
		})
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if strings.HasPrefix(path, "leases") {
				leaseHandler.ServeHTTP(w, r)
				return
			}
			accHandler.ServeHTTP(w, r)
		})
	}
	protected := diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, http.StripPrefix(base, handler))
	mux.Handle(base, protected)
	mux.Handle(base+"/", protected)
	if log != nil {
		log.InfoContext(logCtx, "accounting authority query mounted", "path", base)
	}
}
