package stdhttp

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func wrapDiagnostics(cfg *config.Config, next http.Handler) http.Handler {
	if cfg == nil {
		return diag.WrapDiagnosticsProtect("", next)
	}
	return diag.WrapDiagnosticsProtectHeaders(cfg.Diagnostics.SharedSecret, cfg.HTTPHeaders.Effective().DiagnosticsSecret, next)
}
