package aggregate_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestApply_DeltaCumulativeCorrectionReplacement(t *testing.T) {
	t.Parallel()
	base := fact("f1", 1, metering.FactKindDelta, qty(metering.ComponentInputToken, 10), nil)
	cum := fact("f2", 2, metering.FactKindCumulative, qty(metering.ComponentInputToken, 12), nil)
	corr := fact("f3", 3, metering.FactKindCorrection, qty(metering.ComponentOutputToken, 2), []string{"f1"})
	repl := fact("f4", 4, metering.FactKindAuthoritativeReplacement, qty(metering.ComponentTotalToken, 20), []string{"f2", "f3"})

	snap, err := aggregate.Apply([]metering.Fact{repl, corr, cum, base}) // out of order input
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentTotalToken] != 20 {
		t.Fatalf("quantities=%v", snap.Quantities)
	}
	if _, ok := snap.Superseded["f2"]; !ok {
		t.Fatal("expected superseded f2")
	}
	// Restart replay equals live aggregate.
	again, err := aggregate.Apply([]metering.Fact{base, cum, corr, repl})
	if err != nil {
		t.Fatal(err)
	}
	if again.Quantities[metering.ComponentTotalToken] != snap.Quantities[metering.ComponentTotalToken] {
		t.Fatalf("restart mismatch %v vs %v", again.Quantities, snap.Quantities)
	}
}

func TestApply_UnavailableTrackedAndIdempotentFactID(t *testing.T) {
	t.Parallel()
	u := fact("u1", 1, metering.FactKindUnavailable, nil, nil)
	u.Presence = metering.PresenceUnknown
	d := fact("d1", 2, metering.FactKindDelta, qty(metering.ComponentInputToken, 3), nil)
	snap, err := aggregate.Apply([]metering.Fact{u, d, d})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentInputToken] != 3 {
		t.Fatalf("double-count? %v", snap.Quantities)
	}
	if len(snap.Unavailable) != 1 || snap.Unavailable[0] != "u1" {
		t.Fatalf("unavailable=%v", snap.Unavailable)
	}
}

func fact(id string, seq int64, kind metering.FactKind, qs []metering.Quantity, supersedes []string) metering.Fact {
	f := metering.Fact{
		FactID:      id,
		StreamID:    "stream-agg",
		Sequence:    seq,
		Kind:        kind,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		Quantities:  qs,
		Supersedes:  supersedes,
		RecordedAt:  time.Unix(1, 0).UTC(),
	}
	return f
}

func qty(component string, v int64) []metering.Quantity {
	return []metering.Quantity{{Component: component, Unit: metering.UnitToken, Value: v, Present: true}}
}
