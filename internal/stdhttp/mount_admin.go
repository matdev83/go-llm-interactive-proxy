package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
)

// mountAccountingAdminInput carries inputs for [mountAccountingAdmin].
type mountAccountingAdminInput struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
}

// mountAccountingAdmin mounts the token-accounting admin endpoint when accounting.admin.enabled
// is true and a path is configured. The handler is wrapped with the diagnostics shared-secret
// protection. Never returns an error: when disabled or misconfigured it simply mounts nothing.
func mountAccountingAdmin(in mountAccountingAdminInput) {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	logCtx := in.LogCtx
	if !cfg.Accounting.Admin.Enabled {
		return
	}
	path := strings.TrimSpace(cfg.Accounting.Admin.Path)
	if path == "" {
		return
	}
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
	log.InfoContext(logCtx, "token accounting admin mounted", "path", path)
}

// controlPlaneQueryMount carries inputs for [mountControlPlaneQuery].
type controlPlaneQueryMount struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
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
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.ControlPlaneQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(logCtx, "control-plane query config enabled but diagnostics shared_secret is empty; mounting disabled (query surface would be unauthenticated)",
				slog.String("component", "control_plane"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if built == nil || built.ControlPlaneQueries == nil {
		if log != nil {
			log.WarnContext(logCtx, "control-plane query config enabled but no query service wired; mounting disabled",
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
		Queries:         built.ControlPlaneQueries,
		ReadinessReport: built.ReadinessReport,
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
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
}

// mountAccountingAuthorityQuery mounts the protected authority status and
// bounded query routes only when the authority capability and diagnostics
// shared-secret posture are explicitly configured.
func mountAccountingAuthorityQuery(in accountingAuthorityQueryMount) {
	mux, cfg, log, built := in.Mux, in.Cfg, in.Log, in.Built
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.AuthorityQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(logCtx, "accounting authority query config enabled but diagnostics shared_secret is empty; mounting disabled",
				slog.String("component", "accounting_authority"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if built == nil || built.UsageAuthority == nil {
		if log != nil {
			log.WarnContext(logCtx, "accounting authority query config enabled but no authority service wired; mounting disabled",
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
		Queries:         built.UsageAuthority,
		DefaultPageSize: cfg.Accounting.Authority.Query.DefaultPageSize,
		MaxPageSize:     cfg.Accounting.Authority.Query.MaxPageSize,
	})
	handler := accHandler
	if built.Executor != nil && built.Executor.ConcurrencyProvider != nil {
		leaseHandler := cpadmin.NewConcurrencyAuthorityHandler(cpadmin.ConcurrencyOptions{
			Provider:        built.Executor.ConcurrencyProvider,
			Service:         built.ConcurrencyAuthority,
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
