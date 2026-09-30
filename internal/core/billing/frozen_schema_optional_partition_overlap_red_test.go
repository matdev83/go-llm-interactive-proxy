package billing_test

// R7 follow-up (Astra reviewer rejection): the generic frozen-schema subset
// overlap guard must treat a PRESENT optional partition member as part of the
// parent's proven coverage. The accepted contract for the OpenAI image member
// is P->C partition Optional:true plus P->S subset (same scope/direction/unit).
// When the only complete-coverage child is the optional member C and it is
// present and priced, a priced subset S is contained in the same parent and the
// two lines double charge. The guard must not skip the partition proof merely
// because every declared member is optional; it may skip only when no member is
// present. Conversely, an absent optional member must remain absent (no fake
// zero, no fake coverage): the subset then stays independently billable.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// b1OptionalPartitionSchema is the accepted P->C partition Optional:true plus
// P->S subset contract in one schema, with the parent, optional member and
// subset all sharing direction/unit/scope as the reviewer specified.
func b1OptionalPartitionSchema(parent, member, subset metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: member, Optional: true},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subset},
		},
	}}
}

// TestB1PresentOptionalPartitionMemberParticipatesInSubsetOverlapGuard is the
// RED vector: P=10 informational/no rule, C=10 present priced optional member,
// S=2 present priced subset. The optional member is the only child and it is
// present, so the parent's coverage is proven and the priced subset is an
// additive double charge; the rater must fail closed with no payable lines.
func TestB1PresentOptionalPartitionMemberParticipatesInSubsetOverlapGuard(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	member := b1Key(metering.DirectionInput, "vendor:optional_member", metering.UnitToken)
	subset := b1Key(metering.DirectionInput, "vendor:included_subset", metering.UnitToken)

	tariff := b1Tariff(t, "b1-optional-partition-overlap", []economics.RatingRule{
		b1Rule(t, "member-rate", member, "1"),
		b1Rule(t, "subset-rate", subset, "2"),
	}, b1OptionalPartitionSchema(parent, member, subset))
	resolved := b1Resolve(t, tariff)

	obs := b1Observation(t, "b1-optional-partition-overlap", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, member, "10"), b1Measure(t, subset, "2"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("present optional member + priced subset err=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v",
			err, val.Totals, val.Lines)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("completeness=%q, want conflict", val.Completeness)
	}
	b1AssertNoPayableLines(t, val)
}

// TestB1AbsentOptionalPartitionMemberDoesNotFakeCoverage proves the boundary:
// with the identical schema but the optional member absent, the parent has no
// member to account for its share, so its missing rule is not excused (no fake
// coverage), no synthetic zero member line appears, and the priced subset stays
// independently billable rather than being misclassified as an overlap.
func TestB1AbsentOptionalPartitionMemberDoesNotFakeCoverage(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	member := b1Key(metering.DirectionInput, "vendor:optional_member", metering.UnitToken)
	subset := b1Key(metering.DirectionInput, "vendor:included_subset", metering.UnitToken)

	tariff := b1Tariff(t, "b1-optional-partition-absent", []economics.RatingRule{
		b1Rule(t, "member-rate", member, "1"),
		b1Rule(t, "subset-rate", subset, "2"),
	}, b1OptionalPartitionSchema(parent, member, subset))
	resolved := b1Resolve(t, tariff)

	obs := b1Observation(t, "b1-optional-partition-absent", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, subset, "2"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("absent optional member must not create a fake overlap: %v", err)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("absent optional member completeness=%q, want partial (no fake coverage)", val.Completeness)
	}
	for _, line := range val.Lines {
		if line.Component != nil && line.Component.Component == member.Component {
			t.Fatalf("absent optional member must not synthesize a member line: %+v", line)
		}
	}
	amount := b1FindLineAmount(val, subset)
	if amount == nil || amount.CanonicalString() != "4/0" {
		t.Fatalf("absent optional member subset amount=%v, want independently payable 4/0", amount)
	}
}

func b1FindLineAmount(val economics.Valuation, key metering.ComponentKey) *metering.Decimal {
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Direction == key.Direction && line.Component.Component == key.Component && line.Component.Unit == key.Unit {
			return line.Amount
		}
	}
	return nil
}
