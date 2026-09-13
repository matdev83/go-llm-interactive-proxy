package metering

import "context"

// ObservationSink is the narrow durable-capture seam. Implementations own
// storage and transaction behavior; callers provide one complete immutable
// Observation and never pass provider-shaped or raw-content payloads.
type ObservationSink interface {
	Append(ctx context.Context, observation Observation) error
}

// ObservationSource is the optional host-only sideband drain implemented by
// backend streams. A source returns canonical observations, never client
// events; callers own replay/conflict handling at the B-leg terminal boundary.
type ObservationSource interface {
	DrainEconomicObservations() []Observation
}
