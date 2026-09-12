package metering

import "context"

// ObservationSink is the narrow durable-capture seam. Implementations own
// storage and transaction behavior; callers provide one complete immutable
// Observation and never pass provider-shaped or raw-content payloads.
type ObservationSink interface {
	Append(ctx context.Context, observation Observation) error
}
