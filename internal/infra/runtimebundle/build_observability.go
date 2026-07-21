package runtimebundle

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
)

// observabilityRuntime holds the metrics bundle and the shared upstream HTTP
// client produced by [buildObservabilityRuntime]. Neither resource owns a closer.
type observabilityRuntime struct {
	Bundle   *metrics.Bundle
	Upstream *http.Client
}

// buildProcessMetricsBundle constructs the process-owned Prometheus metrics bundle
// when metrics are enabled. Call site for metrics.NewBundle remains here so the
// live uniqueness gate keeps a single process construction site.
func buildProcessMetricsBundle(cfg *config.Config, poolStats func() []sql.DBStats) *metrics.Bundle {
	if cfg == nil || !cfg.Observability.Metrics.Enabled {
		return nil
	}
	return metrics.NewBundle(cfg, poolStats)
}

// buildGenerationObservability builds the generation-owned upstream HTTP client,
// wrapping it with the shared process metrics bundle and optional OTEL propagation.
// It does not construct a metrics registry.
// Internally created transports are ledgered for idle cleanup; caller-injected
// Infra.HTTPClient transports are never claimed.
func buildGenerationObservability(bctx buildContext, bundle *metrics.Bundle) observabilityRuntime {
	cfg := bctx.Cfg
	opts := bctx.Opts
	tune := httpclient.TransportTuneFromConfig(cfg)
	ownedIdle := idleTransportCloser(nil)
	var upstream *http.Client
	if opts != nil && opts.Infra.HTTPClient != nil {
		upstream = opts.Infra.HTTPClient
	} else {
		upstream = httpclient.StandardWithTune(cfg.EffectiveTrustEnvironmentProxy(), tune)
		ownedIdle = idleTransportCloser(upstream.Transport)
	}
	outbound := false
	if opts != nil {
		outbound = opts.Infra.OutboundTracing
	}
	upstream = wrapUpstreamClient(upstream, bundle, outbound)
	if ownedIdle != nil && bctx.Ledger != nil {
		bctx.Ledger.Add("upstream-idle-transport", PhaseClose, func(context.Context) error {
			ownedIdle()
			return nil
		})
	}
	return observabilityRuntime{Bundle: bundle, Upstream: upstream}
}

// buildObservabilityRuntime builds the metrics bundle (when enabled) and the
// shared upstream HTTP client. Kept for compatibility with callers that still
// assemble both together; prefer process metrics + [buildGenerationObservability].
func buildObservabilityRuntime(bctx buildContext) observabilityRuntime {
	bundle := buildProcessMetricsBundle(bctx.Cfg, bctx.PostgresPools.Stats)
	return buildGenerationObservability(bctx, bundle)
}

// wrapUpstreamClient wraps an upstream [http.Client] transport with the metrics
// bundle's upstream round-tripper (when present) and OpenTelemetry HTTP
// propagation (when outboundTracing is true). When HTTPClient is non-nil the
// caller-owned client is cloned before wrapping so it is not mutated.
func wrapUpstreamClient(client *http.Client, bundle *metrics.Bundle, outboundTracing bool) *http.Client {
	if client == nil {
		return nil
	}
	rt := client.Transport
	if rt == nil {
		rt = httpclient.DefaultTransport()
	}
	wrapped := rt
	if bundle != nil && bundle.Upstream != nil {
		wrapped = bundle.Upstream.WrapUpstreamRoundTripper(wrapped)
	}
	if outboundTracing {
		wrapped = tracing.WrapTransport(true, wrapped)
	}
	if wrapped == rt {
		return client
	}
	c := *client
	c.Transport = wrapped
	return &c
}

// idleTransportCloser returns a CloseIdleConnections callback for an owned
// transport without going through metrics/otel wrappers that may hide the hook.
func idleTransportCloser(rt http.RoundTripper) func() {
	if rt == nil {
		return nil
	}
	if tr, ok := rt.(*http.Transport); ok {
		return tr.CloseIdleConnections
	}
	type idleCloser interface{ CloseIdleConnections() }
	if c, ok := rt.(idleCloser); ok {
		return c.CloseIdleConnections
	}
	return nil
}
