package runtimebundle

import (
	"net/http"

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

// buildObservabilityRuntime builds the metrics bundle (when enabled) and the
// shared upstream HTTP client, wrapping its transport with upstream metrics and
// optional OpenTelemetry propagation. Behavior matches the inline block formerly
// in [Build].
func buildObservabilityRuntime(bctx buildContext) observabilityRuntime {
	cfg := bctx.Cfg
	opts := bctx.Opts
	var bundle *metrics.Bundle
	if cfg.Observability.Metrics.Enabled {
		bundle = metrics.NewBundle(cfg)
	}
	tune := httpclient.TransportTuneFromConfig(cfg)
	upstream := httpclient.StandardWithTune(cfg.EffectiveTrustEnvironmentProxy(), tune)
	if opts.Infra.HTTPClient != nil {
		upstream = opts.Infra.HTTPClient
	}
	upstream = wrapUpstreamClient(upstream, bundle, opts.Infra.OutboundTracing)
	return observabilityRuntime{Bundle: bundle, Upstream: upstream}
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
