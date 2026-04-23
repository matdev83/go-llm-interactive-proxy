package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/prometheus/client_golang/prometheus"
)

// ExtensionStageProm holds Prometheus series for the legal extension pipeline (stage four).
//
// PromQL (p99 latency per stage):
//
//	histogram_quantile(0.99, sum by (le, stage) (rate(lip_extension_stage_duration_seconds_bucket[5m])))
//
// PromQL (fail-open skip rate):
//
//	sum by (stage) (rate(lip_extension_stage_fail_open_skips_total[5m]))
type ExtensionStageProm struct {
	stageDur    *prometheus.HistogramVec
	failOpenSkp *prometheus.CounterVec
}

var extensionStageBuckets = []float64{
	0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
}

// RegisterExtensionStageProm registers lip_extension_stage_* collectors on reg.
func RegisterExtensionStageProm(reg prometheus.Registerer) *ExtensionStageProm {
	m := &ExtensionStageProm{
		stageDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "extension_stage_duration_seconds",
				Help:      "Wall time for one extension stage run (labels: stage + outcome; bounded stage names).",
				Buckets:   extensionStageBuckets,
			},
			[]string{"stage", "outcome"},
		),
		failOpenSkp: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "extension_stage_fail_open_skips_total",
				Help:      "Fail-open handler errors skipped during an extension stage (label: stage only).",
			},
			[]string{"stage"},
		),
	}
	reg.MustRegister(m.stageDur, m.failOpenSkp)
	return m
}

type extensionStageSink struct {
	p *ExtensionStageProm
}

// NewExtensionStageSink adapts [ExtensionStageProm] to [extensions.StageMetrics].
func NewExtensionStageSink(p *ExtensionStageProm) extensions.StageMetrics {
	if p == nil {
		return nil
	}
	return &extensionStageSink{p: p}
}

func (s *extensionStageSink) ObserveStage(stage, outcome string, seconds float64) {
	if s == nil || s.p == nil {
		return
	}
	if stage == "" {
		stage = "unknown"
	}
	if outcome == "" {
		outcome = "unknown"
	}
	s.p.stageDur.WithLabelValues(stage, outcome).Observe(seconds)
}

func (s *extensionStageSink) IncFailOpenSkip(stage string) {
	if s == nil || s.p == nil {
		return
	}
	if stage == "" {
		stage = "unknown"
	}
	s.p.failOpenSkp.WithLabelValues(stage).Inc()
}
