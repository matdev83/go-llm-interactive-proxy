package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// MetricsSink receives coarse executor-level observations (Prometheus, etc.) when non-nil.
type MetricsSink interface {
	OnAttemptRecorded(outcome lipapi.AttemptOutcome, backend string)
	OnBackendOpenDuration(backend string, seconds float64)
}
