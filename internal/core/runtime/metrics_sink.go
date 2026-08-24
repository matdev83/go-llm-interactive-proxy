package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// MetricsSink receives coarse executor-level observations (Prometheus, etc.) when non-nil.
type MetricsSink interface {
	OnAttemptRecorded(outcome lipapi.AttemptOutcome, backend string)
	OnBackendOpenDuration(backend string, seconds float64)
	OnTransportNegotiation(operation lipapi.Operation, mode lipapi.TransportMode, outcome string)
	OnCancellation(obs CancellationObservation)
}

func (e *Executor) recordTransportNegotiation(operation lipapi.Operation, mode lipapi.TransportMode, outcome string) {
	if e == nil || e.Metrics == nil {
		return
	}
	e.Metrics.OnTransportNegotiation(operation, mode, outcome)
}

func (e *Executor) recordCancellation(obs CancellationObservation) {
	if e == nil || e.Metrics == nil {
		return
	}
	e.Metrics.OnCancellation(obs)
}
