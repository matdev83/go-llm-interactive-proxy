package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// mountMetricsInput carries inputs for [mountMetrics].
type mountMetricsInput struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Built  *runtimebundle.Built
}

// mountMetrics mounts the Prometheus metrics endpoint when observability.metrics.enabled is
// true and returns the HTTP metrics handle (nil when disabled) for the outer middleware stack.
// On misconfiguration (enabled without built.Metrics) it returns the same error the inline
// block previously returned so [RunWithRuntime]'s error wrapping stays identical.
func mountMetrics(in mountMetricsInput) (*metrics.HTTPMetrics, error) {
	cfg, log, built, mux := in.Cfg, in.Log, in.Built, in.Mux
	if !cfg.Observability.Metrics.Enabled {
		return nil, nil
	}
	if built.Metrics == nil || built.Metrics.Registry == nil {
		return nil, fmt.Errorf("stdhttp: observability.metrics.enabled requires built.Metrics from runtimebundle.Build")
	}
	promReg := built.Metrics.Registry
	httpProm := built.Metrics.HTTP
	mp := strings.TrimSpace(cfg.Observability.Metrics.Path)
	if mp == "" {
		mp = "/metrics"
	}
	om := cfg.Observability.Metrics.ExemplarsEnabled
	mux.Handle(mp, diag.WrapDiagnosticsProtect(cfg.Diagnostics.SharedSecret, metrics.MetricsHandler(promReg, om)))
	log.InfoContext(in.LogCtx, "prometheus metrics mounted", "path", mp, "open_metrics", om)
	return httpProm, nil
}
