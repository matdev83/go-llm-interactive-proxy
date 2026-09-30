package billing_test

// N1 (PR #666 reviewer residual): a proven complete partition cover of an
// unpriced aggregate parent conflicts with a payable component that is only
// TRANSITIVELY included in that parent through a declared subset chain whose
// intermediate is absent. The exact graph is:
//
//	A --partition--> B, A --partition--> C, A --subset--> D, D --subset--> E
//
// With A=100, B=60, C=40, E=20, D absent, the B/C partition is structural and
// conserved (so A's absent rule is excused) while E is a priced descendant of
// A's subset child D. B/C cover A and E is a subset of A, so B/C/E together
// double-count E. The direct edge check sees no payable pair (A and D are
// unpriced) and the complete-partition/subset check only inspects DIRECT subset
// children (D, which is absent), so before the repair the rater returns a
// COMPLETE $120. The required result is the typed ErrSchemaOverlapConflict with
// a non-complete valuation and no complete monetary input, through both the
// operator E and customer-policy R seams. The rater must never guess a price
// winner or silently drop E as though the overlap were resolved.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// n1SubsetChainSchema declares the N1 graph: a complete A -> {partB, partC}
// partition plus the subset chain A -> mid -> leaf.
func n1SubsetChainSchema(parent, partB, partC, mid, leaf metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
			{Kind: metering.RelationshipSubset, Parent: mid, Child: leaf},
		},
	}}
}

func n1RateOperator(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error) {
	t.Helper()
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	return rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
}

func n1RateCustomer(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error) {
	t.Helper()
	return billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
}

func n1Obs(t *testing.T, id string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	return b1Observation(t, id, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
}

func n1Amount(val economics.Valuation, key metering.ComponentKey) string {
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Direction == key.Direction && line.Component.Component == key.Component &&
			line.Component.Unit == key.Unit && line.Component.SchemaID == key.SchemaID {
			if line.Amount == nil {
				return "<nil>"
			}
			return line.Amount.CanonicalString()
		}
	}
	return "<absent>"
}

func n1Totals(val economics.Valuation) string {
	parts := make([]string, 0, len(val.Totals))
	for _, total := range val.Totals {
		if total.Amount == nil {
			parts = append(parts, "<nil>")
			continue
		}
		parts = append(parts, total.Amount.CanonicalString())
	}
	return strings.Join(parts, ",")
}

// n1AssertOverlapConflict requires the typed overlap classification, a
// non-complete valuation and no payable line, and reports the B/C/E amounts and
// completeness on failure so the pre-fix $120 complete result is visible.
func n1AssertOverlapConflict(t *testing.T, seam string, val economics.Valuation, err error, partB, partC, leaf metering.ComponentKey) {
	t.Helper()
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("%s: err=%v, want ErrSchemaOverlapConflict; completeness=%q B=%s C=%s E=%s totals=%s lines=%+v",
			seam, err, val.Completeness, n1Amount(val, partB), n1Amount(val, partC), n1Amount(val, leaf), n1Totals(val), val.Lines)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("%s: completeness=%q, want conflict; B=%s C=%s E=%s totals=%s lines=%+v",
			seam, val.Completeness, n1Amount(val, partB), n1Amount(val, partC), n1Amount(val, leaf), n1Totals(val), val.Lines)
	}
	b1AssertNoPayableLines(t, val)
}

// TestN1CompletePartitionTransitiveSubsetOverlapRejected is the primary RED
// vector. Before the repair both seams returned complete $120 (B 60 + C 40 +
// E 20); after the repair both must return the typed conflict with no payable
// line.
func TestN1CompletePartitionTransitiveSubsetOverlapRejected(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:n1_total")
	partB := r7Key("vendor:n1_part_b")
	partC := r7Key("vendor:n1_part_c")
	mid := r7Key("vendor:n1_mid")
	leaf := r7Key("vendor:n1_leaf")
	resolved := b1Resolve(t, b1Tariff(t, "n1-transitive", []economics.RatingRule{
		b1Rule(t, "n1-b-rate", partB, "1"),
		b1Rule(t, "n1-c-rate", partC, "1"),
		b1Rule(t, "n1-leaf-rate", leaf, "1"),
	}, n1SubsetChainSchema(parent, partB, partC, mid, leaf)))
	obs := n1Obs(t, "n1-transitive",
		b1Measure(t, parent, "100"),
		b1Measure(t, partB, "60"),
		b1Measure(t, partC, "40"),
		b1Measure(t, leaf, "20"))

	for _, seam := range []struct {
		name string
		rate func(*testing.T, economics.TariffSnapshot, metering.Observation) (economics.Valuation, error)
	}{
		{name: "operator_E", rate: n1RateOperator},
		{name: "customer_policy_R", rate: n1RateCustomer},
	} {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			n1AssertOverlapConflict(t, seam.name, val, err, partB, partC, leaf)
		})
	}
}

// TestN1TransitiveOverlapRobustness covers declaration-order independence, a
// deeper chain with multiple absent intermediates, an absent optional partition
// member, and the structural controls where no payable descendant exists.
func TestN1TransitiveOverlapRobustness(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:n1_total")
	partB := r7Key("vendor:n1_part_b")
	partC := r7Key("vendor:n1_part_c")
	mid := r7Key("vendor:n1_mid")
	leaf := r7Key("vendor:n1_leaf")

	t.Run("reversed_declaration_order_conflicts", func(t *testing.T) {
		t.Parallel()
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: mid, Child: leaf},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			},
		}}
		resolved := b1Resolve(t, b1Tariff(t, "n1-reversed", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, schema))
		obs := n1Obs(t, "n1-reversed",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, leaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		n1AssertOverlapConflict(t, "reversed", val, err, partB, partC, leaf)
	})

	t.Run("deep_chain_with_absent_intermediates_conflicts", func(t *testing.T) {
		t.Parallel()
		deeper := r7Key("vendor:n1_mid2")
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
				{Kind: metering.RelationshipSubset, Parent: mid, Child: deeper},
				{Kind: metering.RelationshipSubset, Parent: deeper, Child: leaf},
			},
		}}
		resolved := b1Resolve(t, b1Tariff(t, "n1-deep", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, schema))
		obs := n1Obs(t, "n1-deep",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, leaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		n1AssertOverlapConflict(t, "deep", val, err, partB, partC, leaf)
	})

	t.Run("absent_optional_member_still_conflicts", func(t *testing.T) {
		t.Parallel()
		optional := r7Key("vendor:n1_optional")
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: optional, Optional: true},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
				{Kind: metering.RelationshipSubset, Parent: mid, Child: leaf},
			},
		}}
		resolved := b1Resolve(t, b1Tariff(t, "n1-optional-absent", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, schema))
		obs := n1Obs(t, "n1-optional-absent",
			b1Measure(t, parent, "60"),
			b1Measure(t, partB, "60"),
			b1Measure(t, leaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("absent optional member: err=%v, want ErrSchemaOverlapConflict; completeness=%q B=%s E=%s lines=%+v",
				err, val.Completeness, n1Amount(val, partB), n1Amount(val, leaf), val.Lines)
		}
		if val.Completeness != economics.CompletenessConflict {
			t.Fatalf("absent optional member completeness=%q, want conflict", val.Completeness)
		}
		b1AssertNoPayableLines(t, val)
	})

	t.Run("child_only_partition_without_payable_descendant_stays_complete", func(t *testing.T) {
		t.Parallel()
		resolved := b1Resolve(t, b1Tariff(t, "n1-child-only", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
		}, n1SubsetChainSchema(parent, partB, partC, mid, leaf)))
		obs := n1Obs(t, "n1-child-only",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"))
		val, err := n1RateOperator(t, resolved, obs)
		if err != nil {
			t.Fatalf("child-only complete partition must rate, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("child-only completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "100/0" {
			t.Fatalf("child-only total=%s, want 100", total)
		}
	})

	t.Run("zero_quantity_leaf_is_not_a_conflict", func(t *testing.T) {
		t.Parallel()
		resolved := b1Resolve(t, b1Tariff(t, "n1-zero-leaf", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, n1SubsetChainSchema(parent, partB, partC, mid, leaf)))
		obs := n1Obs(t, "n1-zero-leaf",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, leaf, "0"))
		val, err := n1RateOperator(t, resolved, obs)
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("zero-quantity leaf must not conflict: %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "100/0" {
			t.Fatalf("zero-quantity leaf total=%s, want 100", total)
		}
	})

	t.Run("explicit_free_leaf_is_not_a_conflict", func(t *testing.T) {
		t.Parallel()
		resolved := b1Resolve(t, b1Tariff(t, "n1-free-leaf", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-free", leaf, "0"),
		}, n1SubsetChainSchema(parent, partB, partC, mid, leaf)))
		obs := n1Obs(t, "n1-free-leaf",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, leaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("explicit-free leaf must not conflict: %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "100/0" {
			t.Fatalf("explicit-free leaf total=%s, want 100", total)
		}
	})
}

// TestN1TransitiveOverlapScopeDirectionTransformBounds proves the transitive
// rule keeps the same identity bounds as the direct and declared-transitive
// checks: a leaf linked only across direction, via a transform, or in an
// independent scope stays additive.
func TestN1TransitiveOverlapScopeDirectionTransformBounds(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:n1_total")
	partB := r7Key("vendor:n1_part_b")
	partC := r7Key("vendor:n1_part_c")
	mid := r7Key("vendor:n1_mid")
	leaf := r7Key("vendor:n1_leaf")

	t.Run("cross_direction_stays_additive", func(t *testing.T) {
		t.Parallel()
		outLeaf := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:n1_leaf", Unit: metering.UnitToken, SchemaID: b1SchemaID}
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
				{Kind: metering.RelationshipSubset, Parent: mid, Child: outLeaf},
			},
		}}
		resolved := b1Resolve(t, b1Tariff(t, "n1-cross-direction", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", outLeaf, "1"),
		}, schema))
		obs := n1Obs(t, "n1-cross-direction",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, outLeaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("cross-direction descendant must not conflict: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "120/0" {
			t.Fatalf("cross-direction total=%s, want additive 120", total)
		}
	})

	t.Run("transform_edge_stays_additive", func(t *testing.T) {
		t.Parallel()
		schema := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
				{Kind: metering.RelationshipTransform, Parent: mid, Child: leaf},
			},
		}}
		resolved := b1Resolve(t, b1Tariff(t, "n1-transform", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, schema))
		obs := n1Obs(t, "n1-transform",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"),
			b1Measure(t, leaf, "20"))
		val, err := n1RateOperator(t, resolved, obs)
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("transform descendant must not conflict: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "120/0" {
			t.Fatalf("transform total=%s, want additive 120", total)
		}
	})

	t.Run("independent_scope_stays_additive", func(t *testing.T) {
		t.Parallel()
		resolved := b1Resolve(t, b1Tariff(t, "n1-cross-scope", []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-rate", leaf, "1"),
		}, n1SubsetChainSchema(parent, partB, partC, mid, leaf)))
		partitionObs := b1Observation(t, "n1-scope-partition", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, parent, "100"), b1Measure(t, partB, "60"), b1Measure(t, partC, "40"))
		leafObs := b1Observation(t, "n1-scope-leaf", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, leaf, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{partitionObs, leafObs}))
		if errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("independent B-leg scopes must not conflict: %v", err)
		}
		if total := b1PayableTotal(t, val); total != "120/0" {
			t.Fatalf("independent scopes total=%s, want additive 120", total)
		}
	})
}

// n1ForkedSubsetRelationships declares the forked N1 graph: the complete
// A -> {partB, partC} partition plus A -> mid with two priced sibling subset
// leaves mid -> {leafE, leafF}. Both siblings are contained in A's proven
// partition, so a rejected overlap must suppress both, independent of
// declaration order.
func n1ForkedSubsetRelationships(parent, partB, partC, mid, leafE, leafF metering.ComponentKey) []metering.ComponentRelationship {
	return []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
		{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
		{Kind: metering.RelationshipSubset, Parent: parent, Child: mid},
		{Kind: metering.RelationshipSubset, Parent: mid, Child: leafE},
		{Kind: metering.RelationshipSubset, Parent: mid, Child: leafF},
	}
}

// n1AssertAllRejected proves the typed conflict, the non-complete valuation and
// that EVERY supplied component is left without a positive payable line,
// reporting the exact surviving payable leaves plus totals and error otherwise.
func n1AssertAllRejected(t *testing.T, label string, val economics.Valuation, err error, keys ...metering.ComponentKey) {
	t.Helper()
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("%s: err=%v, want ErrSchemaOverlapConflict; completeness=%q totals=%s lines=%+v",
			label, err, val.Completeness, n1Totals(val), val.Lines)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("%s: completeness=%q, want conflict; totals=%s lines=%+v", label, val.Completeness, n1Totals(val), val.Lines)
	}
	var surviving []string
	for _, key := range keys {
		amount := b2aComponentAmount(t, val, key)
		if amount == nil {
			continue
		}
		rat, ratErr := amount.ToRat()
		if ratErr != nil || rat.Sign() <= 0 {
			continue
		}
		surviving = append(surviving, key.Component+"="+amount.CanonicalString())
	}
	if len(surviving) != 0 {
		t.Fatalf("%s: rejected overlap still left payable leaves [%s]; completeness=%q err=%v totals=%s lines=%+v",
			label, strings.Join(surviving, " "), val.Completeness, err, n1Totals(val), val.Lines)
	}
	b1AssertNoPayableLines(t, val)
}

// TestN1ForkedPayableSubsetLeavesAllSuppressed is the branch-generalization
// regression: D has two priced subset siblings E and F, both inside A's
// complete priced partition. A first-hit-only traversal suppresses one leaf and
// leaves the other payable, and the surviving leaf flips with declaration order.
// Both seams must reject the whole overlap with no payable line in either order.
func TestN1ForkedPayableSubsetLeavesAllSuppressed(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:n1_total")
	partB := r7Key("vendor:n1_part_b")
	partC := r7Key("vendor:n1_part_c")
	mid := r7Key("vendor:n1_mid")
	leafE := r7Key("vendor:n1_leaf_e")
	leafF := r7Key("vendor:n1_leaf_f")

	forward := n1ForkedSubsetRelationships(parent, partB, partC, mid, leafE, leafF)
	reversed := make([]metering.ComponentRelationship, len(forward))
	for i, rel := range forward {
		reversed[len(forward)-1-i] = rel
	}
	obs := n1Obs(t, "n1-forked",
		b1Measure(t, parent, "100"),
		b1Measure(t, partB, "60"),
		b1Measure(t, partC, "40"),
		b1Measure(t, leafE, "20"),
		b1Measure(t, leafF, "10"))

	for _, order := range []struct {
		name string
		rels []metering.ComponentRelationship
	}{
		{name: "forward_order", rels: forward},
		{name: "reversed_order", rels: reversed},
	} {
		order := order
		resolved := b1Resolve(t, b1Tariff(t, "n1-forked-"+order.name, []economics.RatingRule{
			b1Rule(t, "n1-b-rate", partB, "1"),
			b1Rule(t, "n1-c-rate", partC, "1"),
			b1Rule(t, "n1-leaf-e-rate", leafE, "1"),
			b1Rule(t, "n1-leaf-f-rate", leafF, "1"),
		}, []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: order.rels,
		}}))
		for _, seam := range []struct {
			name string
			rate func(*testing.T, economics.TariffSnapshot, metering.Observation) (economics.Valuation, error)
		}{
			{name: "operator_E", rate: n1RateOperator},
			{name: "customer_policy_R", rate: n1RateCustomer},
		} {
			seam := seam
			t.Run(order.name+"/"+seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				n1AssertAllRejected(t, order.name+"/"+seam.name, val, err, partB, partC, leafE, leafF)
			})
		}
	}
}
