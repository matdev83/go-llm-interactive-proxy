package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// Bundle is a dedicated Prometheus registry plus handles used by stdhttp and runtimebundle.
type Bundle struct {
	Registry *prometheus.Registry
	HTTP     *HTTPMetrics
	Executor *ExecutorProm
	// SecureSession is non-nil when metrics are enabled; secure-session begin/recorder/touch series.
	SecureSession *SecureSessionProm
	// ExtensionStages is non-nil when metrics are enabled; used for extension pipeline histograms/counters.
	ExtensionStages     *ExtensionStageProm
	Upstream            *UpstreamProm
	TokenAccounting     *TokenAccountingProm
	sink                runtime.MetricsSink
	tokenAccountingSink *TokenAccountingPromSink
}

// NewBundle builds a registry with Go/process, inbound HTTP, executor, and upstream series.
func NewBundle(cfg *config.Config) *Bundle {
	r := NewRegistry()
	exemplars := cfg != nil && cfg.Observability.Metrics.ExemplarsEnabled
	httpm := RegisterHTTPMetrics(r, exemplars)
	exec := RegisterExecutorProm(r)
	ss := RegisterSecureSessionProm(r)
	ext := RegisterExtensionStageProm(r)
	up := RegisterUpstreamProm(r, exemplars)
	tok := RegisterTokenAccountingProm(r)
	return &Bundle{
		Registry:            r,
		HTTP:                httpm,
		Executor:            exec,
		SecureSession:       ss,
		ExtensionStages:     ext,
		Upstream:            up,
		TokenAccounting:     tok,
		sink:                NewExecutorPromSink(exec),
		tokenAccountingSink: NewTokenAccountingPromSink(tok),
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

// SecureSessionMetricsSink returns a [runtime.SecureSessionMetrics] backed by this bundle, or nil when b is nil.
func (b *Bundle) SecureSessionMetricsSink() runtime.SecureSessionMetrics {
	if b == nil {
		return nil
	}
	return SecureSessionMetricsSink(b.SecureSession)
}

// TokenAccountingObservabilitySink returns a Prometheus sink for bounded token-accounting observations.
func (b *Bundle) TokenAccountingObservabilitySink() *TokenAccountingPromSink {
	if b == nil {
		return nil
	}
	return b.tokenAccountingSink
}

var _ interface {
	Record(accountingobs.Observation)
} = (*TokenAccountingPromSink)(nil)
