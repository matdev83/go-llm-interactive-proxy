package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/prometheus/client_golang/prometheus"
)

// Bundle is a dedicated Prometheus registry plus handles used by stdhttp and runtimebundle.
type Bundle struct {
	Registry *prometheus.Registry
	HTTP     *HTTPMetrics
	Executor *ExecutorProm
	// ExtensionStages is non-nil when metrics are enabled; used for extension pipeline histograms/counters.
	ExtensionStages *ExtensionStageProm
	Upstream        *UpstreamProm
	sink            runtime.MetricsSink
}

// NewBundle builds a registry with Go/process, inbound HTTP, executor, and upstream series.
func NewBundle(cfg *config.Config) *Bundle {
	r := NewRegistry()
	exemplars := cfg != nil && cfg.Observability.Metrics.ExemplarsEnabled
	httpm := RegisterHTTPMetrics(r, exemplars)
	exec := RegisterExecutorProm(r)
	ext := RegisterExtensionStageProm(r)
	up := RegisterUpstreamProm(r, exemplars)
	return &Bundle{
		Registry:        r,
		HTTP:            httpm,
		Executor:        exec,
		ExtensionStages: ext,
		Upstream:        up,
		sink:            NewExecutorPromSink(exec),
	}
}

// ExecutorSink returns a [runtime.MetricsSink] backed by this bundle's executor metrics.
func (b *Bundle) ExecutorSink() runtime.MetricsSink {
	if b == nil {
		return nil
	}
	return b.sink
}

// ExtensionStageSink returns an [extensions.StageMetrics] backed by this bundle's extension-stage series.
func (b *Bundle) ExtensionStageSink() extensions.StageMetrics {
	if b == nil {
		return nil
	}
	return NewExtensionStageSink(b.ExtensionStages)
}
