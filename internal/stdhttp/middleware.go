package stdhttp

import (
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
)

// stackHTTPInput carries dependencies for [stackHTTPHandler] (same stack as [ComposeStandardHTTP] / generation host).
type stackHTTPInput struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Security HTTPSecurityInput
	TraceGen *diag.TraceIDGenerator
	Inner    http.Handler
	HTTPProm *metrics.HTTPMetrics

	// testOuterWrap, if non-nil, wraps the composed handler before the final outer recovery
	// middleware. Used only from stdhttp tests to simulate panics above inner recovery.
	testOuterWrap func(http.Handler) http.Handler
}

// stackHTTPHandler assembles the same middleware stack as [ComposeStandardHTTP] (outer→inner:
// DownstreamServerMiddleware, final outer recovery, optional OpenTelemetry HTTP, optional
// Prometheus, trace + request ID, access log, inner recovery, transport auth, route mux).
// Innermost is the shared [http.ServeMux] from mounting.
//
// Panic containment: [RecoveryMiddleware] remains between access logging and transport auth so
// access logs and HTTP metrics still observe inner handler panics as 5xx. [outerRecoveryMiddleware]
// wraps the full composed stack as a last resort for panics in outer layers (access log, metrics,
// tracing, or future outer wrappers). [DownstreamServerMiddleware] is outermost and uses a thin
// commit-time ResponseWriter wrapper so Server policy wins on WriteHeader/Write/Flush (including
// HTTP 102 hold-alive) while preserving Flusher and ResponseController Unwrap.
func stackHTTPHandler(in stackHTTPInput) http.Handler {
	cfg, log, sec, traceGen, inner, httpProm := in.Cfg, in.Log, in.Security, in.TraceGen, in.Inner, in.HTTPProm
	h := stdauth.Middleware(log, sec.HTTPAuthProviders, inner)
	h = RecoveryMiddleware(log, h)
	h = accessLogMiddleware(cfg, log, h)
	h = corehttp.TraceMiddleware(corehttp.RequestIDMiddleware(traceGen, h))
	if httpProm != nil {
		h = httpProm.Middleware(h)
	}
	if cfg != nil && cfg.Observability.Tracing.Enabled {
		h = tracing.HTTPMiddleware(true, h)
	}
	if in.testOuterWrap != nil {
		h = in.testOuterWrap(h)
	}
	h = outerRecoveryMiddleware(log, h)
	return DownstreamServerMiddleware(cfg, h)
}
