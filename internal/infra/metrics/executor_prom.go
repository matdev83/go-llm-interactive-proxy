package metrics

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/prometheus/client_golang/prometheus"
)

// ExecutorProm holds Prometheus collectors for executor attempt and open latency.
type ExecutorProm struct {
	attempts           *prometheus.CounterVec
	openDur            *prometheus.HistogramVec
	transportDecisions *prometheus.CounterVec
	cancellations      *prometheus.CounterVec
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
		transportDecisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_transport_negotiations_total",
				Help:      "Transport negotiation decisions before backend open (bounded operation, mode, and outcome labels).",
			},
			[]string{"operation", "mode", "outcome"},
		),
		cancellations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "executor_cancellations_total",
				Help:      "Executor cancellations recorded with low-cardinality cause class, mode, phase, and fallback.",
			},
			[]string{"cause_class", "mode", "phase", "fallback"},
		),
	}
	reg.MustRegister(m.attempts, m.openDur, m.transportDecisions, m.cancellations)
	return m
}

// ObserveCancellation records a cancellation event with strictly bounded low-cardinality labels.
func (m *ExecutorProm) ObserveCancellation(cause lipapi.CancelCause, mode lipapi.CancelMode, phase string, fallback string) {
	if m == nil || m.cancellations == nil {
		return
	}
	causeClass := boundedCauseClass(cause)
	cancelMode := boundedCancelMode(mode)
	cancelPhase := boundedCancelPhase(phase)
	cancelFallback := boundedCancelFallback(fallback)
	m.cancellations.WithLabelValues(causeClass, cancelMode, cancelPhase, cancelFallback).Inc()
}

func boundedCauseClass(cause lipapi.CancelCause) string {
	switch cause.Kind {
	case lipapi.CancelExplicit:
		return "explicit"
	case lipapi.CancelClientGone:
		return "client_gone"
	case lipapi.CancelContextDone:
		return "context_done"
	case lipapi.CancelRaceLoser:
		return "race_loser"
	case "":
		return "none"
	default:
		return "other"
	}
}

func boundedCancelMode(mode lipapi.CancelMode) string {
	switch mode {
	case lipapi.CancelModeNone:
		return "none"
	case lipapi.CancelModeProvider:
		return "provider"
	case lipapi.CancelModeTransport:
		return "transport"
	case lipapi.CancelModeCloseOnly:
		return "close_only"
	case "":
		return "none"
	default:
		return "other"
	}
}

func boundedCancelPhase(phase string) string {
	switch phase {
	case "requested", "outcome", "terminal", "forced":
		return phase
	case "":
		return "none"
	default:
		return "other"
	}
}

func boundedCancelFallback(fallback string) string {
	switch fallback {
	case "negotiated", "legacy", "none":
		return fallback
	case "":
		return "none"
	default:
		return "other"
	}
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

func (s *executorPromSink) OnTransportNegotiation(operation lipapi.Operation, mode lipapi.TransportMode, outcome string) {
	if s == nil || s.p == nil {
		return
	}
	op := string(operation)
	if op == "" {
		op = "unknown"
	}
	m := string(mode)
	if m == "" {
		m = "unknown"
	}
	o := outcome
	switch o {
	case "accept", "reject":
	case "":
		o = "unknown"
	default:
		o = "other"
	}
	s.p.transportDecisions.WithLabelValues(op, m, o).Inc()
}

func (s *executorPromSink) OnCancellation(causeClass string, mode lipapi.CancelMode, phase string, fallback string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.ObserveCancellation(lipapi.CancelCause{Kind: lipapi.CancelKind(causeClass)}, mode, phase, fallback)
}
