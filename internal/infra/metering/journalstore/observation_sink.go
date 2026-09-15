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

type durableObservationSink struct {
	store *DurableStore
}

var _ metering.ObservationSink = durableObservationSink{}
var _ metering.AtomicObservationSink = durableObservationSink{}

func (s durableObservationSink) Append(ctx context.Context, observation metering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	return s.store.AppendObservation(ctx, observation)
}

func (s durableObservationSink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	return s.store.AppendObservations(ctx, observations)
}
