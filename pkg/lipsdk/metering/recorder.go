package metering

import "context"

// Recorder appends metering facts to a durable journal. Implementations must
// treat SameFactIdentity replays as idempotent no-ops and must not mutate
// prior fact bodies in place; corrections use dedicated FactKind values.
type Recorder interface {
	Append(ctx context.Context, fact Fact) error
}
