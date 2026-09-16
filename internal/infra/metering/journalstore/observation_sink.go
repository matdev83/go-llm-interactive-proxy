package journalstore

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// NewObservationSink adapts the V2 observation journal to the SDK capture
// ports. The returned adapter implements both ObservationSink for source
// compatibility and AtomicObservationSink for retry-safe checkpoint batches.
// DurableStore already has a legacy Fact Append method, so the adapter keeps
// the two append contracts distinct.
func NewObservationSink(store *DurableStore) metering.ObservationSink {
	if store == nil {
		return nil
	}
	return durableObservationSink{store: store}
}

// NewObservationSinkWithOutbox adapts the V2 observation journal and enables
// the atomic durable economic trigger outbox. It is deliberately a separate
// constructor so disabled or non-economic metering remains a no-op with no
// relay rows.
func NewObservationSinkWithOutbox(store *DurableStore) metering.ObservationSink {
	if store == nil {
		return nil
	}
	return durableObservationSink{store: store, outbox: true}
}

type durableObservationSink struct {
	store  *DurableStore
	outbox bool
}

var _ metering.ObservationSink = durableObservationSink{}
var _ metering.AtomicObservationSink = durableObservationSink{}

func (s durableObservationSink) Append(ctx context.Context, observation metering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	if s.outbox {
		return s.store.AppendObservationsWithOutbox(ctx, []metering.Observation{observation})
	}
	return s.store.AppendObservation(ctx, observation)
}

func (s durableObservationSink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	if s.outbox {
		return s.store.AppendObservationsWithOutbox(ctx, observations)
	}
	return s.store.AppendObservations(ctx, observations)
}
