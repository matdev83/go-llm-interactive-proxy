package billing_test

// R7 (PR #659 adversarial repair, minimum double-charge blocker): a priced
// subset child of an aggregate parent overlaps a payable COMPLETE child
// partition of that same parent even when the aggregate parent itself carries no
// rule. The frozen schema declares input aggregate -> complete text/audio
// partition and input aggregate -> cached subset. When text, audio and cached
// are all priced (and the aggregate is not), the cached tokens are already
// inside the complete text/audio partition, so billing all three additively
// double-charges the cached share. The rater must fail closed with the typed
// overlap instead of emitting the additive total.
//
// These vectors use the production ReferenceRater over a real published
// billingcompose snapshot and neutral component names, so acceptance depends on
// the explicit frozen relationship and never on component-name or numerical
// coincidence.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func r7Key(component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionInput, Component: component, Unit: metering.UnitToken, SchemaID: b1SchemaID}
}

// r7SubsetPartitionSchema declares parent -> {part_a, part_b} as a complete
// partition and parent -> subset as an included subset.
func r7SubsetPartitionSchema(parent, partA, partB, subset metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partA},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subset},
		},
	}}
}

func r7Rater(t *testing.T, tariff economics.TariffSnapshot) *billing.ReferenceRater {
	t.Helper()
	rater, err := billing.NewReferenceRater(b1Resolve(t, tariff))
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	return rater
}

// TestR7PricedSubsetConflictsWithCompletePartitionWhenParentUnpriced is the
// primary RED vector. Before the repair the rater emitted 8*2 + 3*5 + 4*7 = 59
// as three payable lines; after the repair the subset vs complete-partition
// overlap is a typed conflict with no payable quantity lines.
func TestR7PricedSubsetConflictsWithCompletePartitionWhenParentUnpriced(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:aggregate_total")
	partA := r7Key("vendor:text_part")
	partB := r7Key("vendor:audio_part")
	subset := r7Key("vendor:cached_subset")
	tariff := b1Tariff(t, "r7-subset-partition", []economics.RatingRule{
		b1Rule(t, "text-rate", partA, "2"),
		b1Rule(t, "audio-rate", partB, "5"),
		b1Rule(t, "cached-rate", subset, "7"),
		// The aggregate parent is intentionally unpriced.
	}, r7SubsetPartitionSchema(parent, partA, partB, subset))
	rater := r7Rater(t, tariff)

	obs := b1Observation(t, "r7-subset-partition", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"), b1Measure(t, subset, "4"))
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{obs}))
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("overlap error=%v, want ErrSchemaOverlapConflict; lines=%+v totals=%+v", err, val.Lines, val.Totals)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("completeness=%q, want conflict", val.Completeness)
	}
	b1AssertNoPayableLines(t, val)
}

// TestR7PricedSubsetCompletePartitionPreservationCases pins the bounded rule:
// every case that is not a priced subset colliding with a payable complete
// partition stays exactly as before.
func TestR7PricedSubsetCompletePartitionPreservationCases(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:aggregate_total")
	partA := r7Key("vendor:text_part")
	partB := r7Key("vendor:audio_part")
	subset := r7Key("vendor:cached_subset")
	schema := r7SubsetPartitionSchema(parent, partA, partB, subset)

	newRater := func(t *testing.T, rules []economics.RatingRule) *billing.ReferenceRater {
		t.Helper()
		return r7Rater(t, b1Tariff(t, "r7-preserve", rules, schema))
	}
	rate := func(t *testing.T, rater *billing.ReferenceRater, measures ...metering.Measure) (economics.Valuation, error) {
		t.Helper()
		obs := b1Observation(t, "r7-preserve", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
		return rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{obs}))
	}

	t.Run("complete_partition_child_only_without_priced_subset_stays_31", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"))
		if err != nil {
			t.Fatalf("partition child-only must rate, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "31/0" {
			t.Fatalf("child-only partition total=%s, want disjoint 31", total)
		}
	})

	t.Run("subset_only_partial_keeps_subset_payable", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{b1Rule(t, "cached-rate", subset, "7")})
		val, err := rate(t, rater, b1Measure(t, parent, "11"), b1Measure(t, subset, "4"))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("subset-only must not be a conflict: %v", err)
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("subset-only completeness=%q, want partial", val.Completeness)
		}
		if total := b1PayableTotal(t, val); total != "28/0" {
			t.Fatalf("subset-only total=%s, want 28", total)
		}
	})

	t.Run("merely_present_unpriced_subset_is_not_a_conflict", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"), b1Measure(t, subset, "4"))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("unpriced present subset must not be a conflict: %v", err)
		}
		if val.Completeness == economics.CompletenessConflict {
			t.Fatalf("unpriced present subset completeness=%q, must not be conflict", val.Completeness)
		}
		if total := b1PayableTotal(t, val); total != "31/0" {
			t.Fatalf("unpriced present subset total=%s, want 31", total)
		}
	})

	t.Run("zero_quantity_priced_subset_stays_31", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", subset, "7"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"), b1Measure(t, subset, "0"))
		if err != nil {
			t.Fatalf("zero-quantity subset must not conflict, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "31/0" {
			t.Fatalf("zero-quantity subset total=%s, want 31", total)
		}
	})

	t.Run("unavailable_subset_stays_31", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", subset, "7"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"), b1UnavailableMeasure(subset))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("unavailable subset must not be a conflict: %v", err)
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("unavailable subset completeness=%q, want partial", val.Completeness)
		}
	})
}

// TestR7PricedSubsetPartitionScopeAndDirectionBounds proves the new rule is
// bounded by scope, direction and unit: only a subset and a complete partition
// in the exact same source/B-leg scope and economic direction conflict.
func TestR7PricedSubsetPartitionScopeAndDirectionBounds(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:aggregate_total")
	partA := r7Key("vendor:text_part")
	partB := r7Key("vendor:audio_part")
	inputSubset := r7Key("vendor:cached_subset")
	outputSubset := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:cached_subset", Unit: metering.UnitToken, SchemaID: b1SchemaID}
	outputParent := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:aggregate_total", Unit: metering.UnitToken, SchemaID: b1SchemaID}

	t.Run("different_direction_stays_additive", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "out-cached-rate", outputSubset, "7"),
		}
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partA},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipSubset, Parent: outputParent, Child: outputSubset},
			},
		}}
		rater := r7Rater(t, b1Tariff(t, "r7-direction", rules, schema))
		obs := b1Observation(t, "r7-direction", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"), b1Measure(t, outputSubset, "4"))
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{obs}))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("cross-direction subset must not conflict: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "59/0" {
			t.Fatalf("cross-direction total=%s, want additive 59", total)
		}
	})

	t.Run("independent_b_leg_scopes_stay_additive", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", inputSubset, "7"),
		}
		schema := r7SubsetPartitionSchema(parent, partA, partB, inputSubset)
		rater := r7Rater(t, b1Tariff(t, "r7-scope", rules, schema))
		partitionObs := b1Observation(t, "r7-scope-partition", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, parent, "11"), b1Measure(t, partA, "8"), b1Measure(t, partB, "3"))
		subsetObs := b1Observation(t, "r7-scope-subset", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, inputSubset, "4"))
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{partitionObs, subsetObs}))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("independent B-leg scopes must not conflict: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "59/0" {
			t.Fatalf("independent scopes total=%s, want additive 59", total)
		}
	})
}

// TestR7ObservedZeroPartitionChildConflict is the zero-boundary RED vector
// Astra cited. The complete-partition proof is structural membership plus
// rateability, not positive payable amount: an explicitly observed zero audio
// partition child still completes the text/audio partition. A positively
// payable cached subset colliding with that complete partition is the same
// double charge as the nonzero case and must fail closed. The partition
// membership proof must never be conflated with the positive-payable subset
// proof, and an absent/null child must not be mistaken for an observed zero.
func TestR7ObservedZeroPartitionChildConflict(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:aggregate_total")
	partA := r7Key("vendor:text_part")
	partB := r7Key("vendor:audio_part")
	subset := r7Key("vendor:cached_subset")
	schema := r7SubsetPartitionSchema(parent, partA, partB, subset)
	newRater := func(t *testing.T, rules []economics.RatingRule) *billing.ReferenceRater {
		t.Helper()
		return r7Rater(t, b1Tariff(t, "r7-zero-audio", rules, schema))
	}
	rate := func(t *testing.T, rater *billing.ReferenceRater, measures ...metering.Measure) (economics.Valuation, error) {
		t.Helper()
		obs := b1Observation(t, "r7-zero-audio", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
		return rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{obs}))
	}

	t.Run("explicit_zero_audio_with_priced_subset_is_conflict", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", subset, "7"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "8"), b1Measure(t, partA, "8"), b1Measure(t, partB, "0"), b1Measure(t, subset, "4"))
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("observed zero audio error=%v, want ErrSchemaOverlapConflict; total=%v lines=%+v", err, val.Totals, val.Lines)
		}
		if val.Completeness != economics.CompletenessConflict {
			t.Fatalf("observed zero audio completeness=%q, want conflict", val.Completeness)
		}
		b1AssertNoPayableLines(t, val)
	})

	t.Run("explicit_zero_audio_without_priced_subset_rate_16_complete", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "8"), b1Measure(t, partA, "8"), b1Measure(t, partB, "0"))
		if err != nil {
			t.Fatalf("child-only zero-audio partition must be covered complete, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("child-only zero-audio completeness=%q, want complete", val.Completeness)
		}
		if total := b1PayableTotal(t, val); total != "16/0" {
			t.Fatalf("child-only zero-audio total=%s, want 16", total)
		}
	})

	t.Run("absent_audio_does_not_complete_partition", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", subset, "7"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "8"), b1Measure(t, partA, "8"), b1Measure(t, subset, "4"))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("absent audio must not prove a complete partition: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "44/0" {
			t.Fatalf("absent audio total=%s, want additive 44", total)
		}
	})

	t.Run("null_audio_does_not_complete_partition", func(t *testing.T) {
		t.Parallel()
		rater := newRater(t, []economics.RatingRule{
			b1Rule(t, "text-rate", partA, "2"),
			b1Rule(t, "audio-rate", partB, "5"),
			b1Rule(t, "cached-rate", subset, "7"),
		})
		val, err := rate(t, rater, b1Measure(t, parent, "8"), b1Measure(t, partA, "8"), b1UnavailableMeasure(partB), b1Measure(t, subset, "4"))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("null audio must not prove a complete partition: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "44/0" {
			t.Fatalf("null audio total=%s, want additive 44", total)
		}
	})
}
