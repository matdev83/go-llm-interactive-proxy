package stdhttp

import (
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	geoipingress "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/geoip"
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
// security headers, DownstreamServerMiddleware, final outer recovery, optional OpenTelemetry HTTP,
// optional Prometheus, trace + request ID, access log, inner recovery, transport auth, route mux).
// Innermost is the shared [http.ServeMux] from mounting.
//
// Panic containment: [RecoveryMiddleware] remains between access logging and transport auth so
// access logs and HTTP metrics still observe inner handler panics as 5xx. [outerRecoveryMiddleware]
// wraps the full composed stack as a last resort for panics in outer layers (access log, metrics,
// tracing, or future outer wrappers). [DownstreamServerMiddleware] uses a thin commit-time
// ResponseWriter wrapper so Server policy wins on WriteHeader/Write/Flush (including HTTP 102
// hold-alive) while preserving Flusher and ResponseController Unwrap. [corehttp.SecurityHeadersMiddleware]
// is outermost so every response — including panic-generated 500s written by [outerRecoveryMiddleware]
// before it delegates — carries the security headers.
func stackHTTPHandler(in stackHTTPInput) http.Handler {
	cfg, log, sec, traceGen, inner, httpProm := in.Cfg, in.Log, in.Security, in.TraceGen, in.Inner, in.HTTPProm
	h := stdauth.Middleware(log, sec.HTTPAuthProviders, inner)
	h = RecoveryMiddleware(log, h)
	h = accessLogMiddleware(cfg, log, h)
	traceNames := []string{corehttp.HeaderTraceID}
	if cfg != nil {
		if names := cfg.HTTPHeaders.Effective().Trace; len(names) > 0 {
			traceNames = names
		}
	}
	h = corehttp.TraceMiddlewareHeaders(traceNames, corehttp.RequestIDMiddlewareHeaders(traceGen, traceNames, h))
	if httpProm != nil {
		h = httpProm.Middleware(h)
	}
	if cfg != nil && cfg.Observability.Tracing.Enabled {
		h = tracing.HTTPMiddleware(true, h)
	}
	if sec.GeoIP.Policy != nil {
		geo := sec.GeoIP
		h = geoipingress.Middleware(geoipingress.Input{
			Policy:   geo.Policy,
			Lookup:   geo.Lookup,
			Resolver: geoipingress.ResolverConfig{Source: geoipingress.Source(geo.Resolver.Source), TrustedProxies: append([]netip.Prefix(nil), geo.Resolver.TrustedProxies...)},
			Observer: geo.Observer,
		}, h)
	}
	if in.testOuterWrap != nil {
		h = in.testOuterWrap(h)
	}
	h = outerRecoveryMiddleware(log, h)
	h = DownstreamServerMiddleware(cfg, h)
	return corehttp.SecurityHeadersMiddleware(h)
}
