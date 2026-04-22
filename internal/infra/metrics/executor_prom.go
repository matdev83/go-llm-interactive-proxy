package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/prometheus/client_golang/prometheus"
)

// ExecutorProm holds Prometheus collectors for executor attempt and open latency.
type ExecutorProm struct {
	attempts *prometheus.CounterVec
	openDur  *prometheus.HistogramVec
}

var openAttemptBuckets = []float64{
	0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

// RegisterExecutorProm registers lip_executor_* series on reg.
func RegisterExecutorProm(reg prometheus.Registerer) *ExecutorProm {
	m := &ExecutorProm{
		attempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_attempts_total",
				Help:      "B-leg attempts recorded to continuity (labels: bounded outcome + backend instance id).",
			},
			[]string{"outcome", "backend"},
		),
		openDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "executor_backend_open_seconds",
				Help:      "Time from B-leg allocation until backend Open returns (labels: backend instance id).",
				Buckets:   openAttemptBuckets,
			},
			[]string{"backend"},
		),
	}
	reg.MustRegister(m.attempts, m.openDur)
	return m
}

type executorPromSink struct {
	p *ExecutorProm
}

// NewExecutorPromSink adapts [ExecutorProm] to [runtime.MetricsSink].
func NewExecutorPromSink(p *ExecutorProm) runtime.MetricsSink {
	if p == nil {
		return nil
	}
	return &executorPromSink{p: p}
}

func (s *executorPromSink) OnAttemptRecorded(outcome lipapi.AttemptOutcome, backend string) {
	if s == nil || s.p == nil {
		return
	}
	b := backend
	if b == "" {
		b = "unknown"
	}
	s.p.attempts.WithLabelValues(string(outcome), b).Inc()
}

func (s *executorPromSink) OnBackendOpenDuration(backend string, seconds float64) {
	if s == nil || s.p == nil {
		return
	}
	b := backend
	if b == "" {
		b = "unknown"
	}
	s.p.openDur.WithLabelValues(b).Observe(seconds)
}
