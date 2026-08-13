package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	ssessiondiag "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/diag"
)

// mountSecureSessionDiagnosticsInput carries inputs for [mountSecureSessionDiagnostics].
type mountSecureSessionDiagnosticsInput struct {
	LogCtx   context.Context
	Mux      *http.ServeMux
	Cfg      *config.Config
	Log      *slog.Logger
	Security HTTPSecurityInput
}

// mountSecureSessionDiagnostics mounts the secure-session diagnostics summary endpoints when
// secure sessions are effectively enabled, summary exposure is requested, and a store is wired.
// Errors are returned with the same wrapping the inline block previously used so
// ComposeStandardHTTP's error chain stays identical.
func mountSecureSessionDiagnostics(in mountSecureSessionDiagnosticsInput) error {
	mux, cfg, log, sec := in.Mux, in.Cfg, in.Log, in.Security
	logCtx := in.LogCtx
	secureOn := cfg.SecureSessionEffectivelyEnabled()
	exposeSummaries := cfg.SecureSession.DiagnosticsExposeSummaries
	if !secureOn || !exposeSummaries || sec.SecureSessionStore == nil {
		return nil
	}
	p := strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix)
	if p == "" {
		return fmt.Errorf(
			"stdhttp: secure_session diagnostics_expose_summaries requires " +
				"secure_session.diagnostics_path_prefix",
		)
	}
	base := strings.TrimSuffix(p, "/")
	ssh, err := ssessiondiag.NewHandler(
		base,
		sec.SecureSessionStore,
		cfg.SecureSession.RedactionDefault,
		nil,
		log,
	)
	if err != nil {
		return fmt.Errorf("stdhttp: secure-session diagnostics handler: %w", err)
	}
	dh := wrapDiagnostics(cfg, ssh)
	mux.Handle("GET "+base+"/", dh)
	mux.Handle("GET "+base, dh)
	log.InfoContext(logCtx, "secure-session diagnostics mounted", "path", base)
	return nil
}
