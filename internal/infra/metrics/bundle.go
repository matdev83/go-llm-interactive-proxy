package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/prometheus/client_golang/prometheus"
)

// Bundle is a dedicated Prometheus registry plus handles used by stdhttp and runtimebundle.
type Bundle struct {
	Registry *prometheus.Registry
	HTTP     *HTTPMetrics
	Executor *ExecutorProm
	Upstream *UpstreamProm
	sink     runtime.MetricsSink
}

// NewBundle builds a registry with Go/process, inbound HTTP, executor, and upstream series.
func NewBundle(cfg *config.Config) *Bundle {
	r := NewRegistry()
	exemplars := cfg != nil && cfg.Observability.Metrics.ExemplarsEnabled
	httpm := RegisterHTTPMetrics(r, exemplars)
	exec := RegisterExecutorProm(r)
	up := RegisterUpstreamProm(r, exemplars)
	return &Bundle{
		Registry: r,
		HTTP:     httpm,
		Executor: exec,
		Upstream: up,
		sink:     NewExecutorPromSink(exec),
	}
}

// ExecutorSink returns a [runtime.MetricsSink] backed by this bundle's executor metrics.
func (b *Bundle) ExecutorSink() runtime.MetricsSink {
	if b == nil {
		return nil
	}
	return b.sink
}
