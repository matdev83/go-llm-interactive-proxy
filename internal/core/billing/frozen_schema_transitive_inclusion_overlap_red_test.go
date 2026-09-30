package billing_test

// F2 (PR #666 adversarial re-review, transitive containment double-charge): a
// frozen, same-direction/unit/scope, acyclic inclusion chain A includes B and B
// includes C makes C transitively included in A. When A and C carry positive
// linear rules and the intermediate B is unpriced (or explicitly free, or even
// absent), the direct parent/child overlap check only inspects A->B and B->C,
// so neither payable pair conflicts and both A and C post additive lines. The
// correct fail-closed result for an overlapping additive charge with no explicit
// surcharge contract is the typed ErrSchemaOverlapConflict with no positive
// monetary posting.
//
// These vectors run the production ReferenceRater over a real published
// billingcompose snapshot and neutral schema-qualified component names, so the
// decision depends only on the explicit frozen relationship, never on component
// names or coincidental numbers.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func f2SubsetEdge(parent, child metering.ComponentKey) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: parent, Child: child}
}

func f2TransformEdge(parent, child metering.ComponentKey) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: metering.RelationshipTransform, Parent: parent, Child: child}
}

func f2Schema(relationships ...metering.ComponentRelationship) []metering.ComponentSchema {
	return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: relationships}}
}

func f2Resolve(t *testing.T, refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	return b1Resolve(t, b1Tariff(t, refID, rules, schemas))
}

// f2AssertNoPositivePosting is the zero-monetary-posting proof: a rejected
// valuation may retain an explicit-free zero-valued evidence line, but no line
// and no total may carry strictly positive money.
func f2AssertNoPositivePosting(t *testing.T, val economics.Valuation) {
	t.Helper()
	for _, line := range val.Lines {
		if line.Amount != nil && line.Amount.CanonicalString() != "0/0" {
			t.Fatalf("rejected transitive overlap posted positive money: %+v", line)
		}
	}
	for _, total := range val.Totals {
		if total.Amount != nil && total.Amount.CanonicalString() != "0/0" {
			t.Fatalf("rejected transitive overlap posted a positive total: %+v", total)
		}
	}
}

func f2AssertComponentNotPositive(t *testing.T, val economics.Valuation, key metering.ComponentKey) {
	t.Helper()
	for _, line := range val.Lines {
		if line.Component == nil || line.Component.Component != key.Component {
			continue
		}
		if line.Amount != nil && line.Amount.CanonicalString() != "0/0" {
			t.Fatalf("transitively overlapping component %q must not be positive: %+v", key.Component, line)
		}
	}
}

// TestF2TransitiveInclusionOverlapRejected is the primary counterexample vector.
// A=100 (rate 0.01 => 1.00) and C=40 (rate 0.01 => 0.40) are both positive
// linear rules reachable only through the unpriced middle B. Before the fix the
// rater emitted 1.40 as two additive lines; after the fix the transitive
// containment is a typed conflict with no positive posting.
func TestF2TransitiveInclusionOverlapRejected(t *testing.T) {
	t.Parallel()
	a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
	b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
	c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitToken)
	schema := f2Schema(f2SubsetEdge(a, b), f2SubsetEdge(b, c))

	cases := []struct {
		name     string
		measures []metering.Measure
		rules    []economics.RatingRule
	}{
		{
			name:     "middle_present_unpriced",
			measures: []metering.Measure{b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40")},
			rules:    []economics.RatingRule{b1Rule(t, "ancestor-rate", a, "0.01"), b1Rule(t, "leaf-rate", c, "0.01")},
		},
		{
			name:     "middle_absent",
			measures: []metering.Measure{b1Measure(t, a, "100"), b1Measure(t, c, "40")},
			rules:    []economics.RatingRule{b1Rule(t, "ancestor-rate", a, "0.01"), b1Rule(t, "leaf-rate", c, "0.01")},
		},
		{
			name:     "middle_explicitly_free",
			measures: []metering.Measure{b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40")},
			rules: []economics.RatingRule{
				b1Rule(t, "ancestor-rate", a, "0.01"),
				b1Rule(t, "middle-free", b, "0"),
				b1Rule(t, "leaf-rate", c, "0.01"),
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := f2Resolve(t, "f2-transitive-"+tc.name, tc.rules, schema)
			obs := b1Observation(t, "f2-transitive-"+tc.name, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, tc.measures...)
			rater, err := billing.NewReferenceRater(resolved)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
			if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
				t.Fatalf("transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
			}
			if val.Completeness != economics.CompletenessConflict {
				t.Fatalf("completeness=%q, want conflict", val.Completeness)
			}
			f2AssertComponentNotPositive(t, val, a)
			f2AssertComponentNotPositive(t, val, c)
			f2AssertNoPositivePosting(t, val)
		})
	}
}

// TestF2TransitiveInclusionDeeperChainAndDeclarationOrder proves bounded
// reachability handles chains deeper than two hops and that the declaration
// order of the frozen relationships cannot change the decision.
func TestF2TransitiveInclusionDeeperChainAndDeclarationOrder(t *testing.T) {
	t.Parallel()
	a := b1Key(metering.DirectionInput, "vendor:level_a", metering.UnitToken)
	b := b1Key(metering.DirectionInput, "vendor:level_b", metering.UnitToken)
	c := b1Key(metering.DirectionInput, "vendor:level_c", metering.UnitToken)
	d := b1Key(metering.DirectionInput, "vendor:level_d", metering.UnitToken)
	rules := []economics.RatingRule{b1Rule(t, "a-rate", a, "0.01"), b1Rule(t, "d-rate", d, "0.01")}
	measures := []metering.Measure{b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "60"), b1Measure(t, d, "40")}

	for _, tc := range []struct {
		name          string
		relationships []metering.ComponentRelationship
	}{
		{name: "forward_order", relationships: []metering.ComponentRelationship{f2SubsetEdge(a, b), f2SubsetEdge(b, c), f2SubsetEdge(c, d)}},
		{name: "reversed_order", relationships: []metering.ComponentRelationship{f2SubsetEdge(c, d), f2SubsetEdge(b, c), f2SubsetEdge(a, b)}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := f2Resolve(t, "f2-deep-"+tc.name, rules, f2Schema(tc.relationships...))
			obs := b1Observation(t, "f2-deep-"+tc.name, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
			rater, err := billing.NewReferenceRater(resolved)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
			if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
				t.Fatalf("deep transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
			}
			if val.Completeness != economics.CompletenessConflict {
				t.Fatalf("completeness=%q, want conflict", val.Completeness)
			}
			f2AssertNoPositivePosting(t, val)
		})
	}
}

// TestF2TransitiveInclusionSplitSchemas proves the reachability is over the
// whole frozen schema set: a chain whose two edges are declared in separate
// schemas is still one declared transitive containment.
func TestF2TransitiveInclusionSplitSchemas(t *testing.T) {
	t.Parallel()
	a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
	b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
	c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitToken)
	schemas := []metering.ComponentSchema{
		{ID: b1SchemaID + ".one", Version: "1", Relationships: []metering.ComponentRelationship{f2SubsetEdge(a, b)}},
		{ID: b1SchemaID + ".two", Version: "1", Relationships: []metering.ComponentRelationship{f2SubsetEdge(b, c)}},
	}
	resolved := f2Resolve(t, "f2-split-schemas", []economics.RatingRule{
		b1Rule(t, "ancestor-rate", a, "0.01"),
		b1Rule(t, "leaf-rate", c, "0.01"),
	}, schemas)
	obs := b1Observation(t, "f2-split-schemas", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("split-schema transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
	}
	f2AssertNoPositivePosting(t, val)
}

// TestF2TransitiveInclusionNegativeBounds pins that the reachability is bounded
// by the same identity dimensions as the direct check: transform edges are a
// separately governed derivation, cross-direction and cross-scope chains never
// collapse, and unit-distinct edges are transforms. Each case stays additive.
func TestF2TransitiveInclusionNegativeBounds(t *testing.T) {
	t.Parallel()

	t.Run("transform_chain_stays_additive", func(t *testing.T) {
		t.Parallel()
		a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
		b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
		c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitToken)
		resolved := f2Resolve(t, "f2-transform-chain", []economics.RatingRule{
			b1Rule(t, "a-rate", a, "1"),
			b1Rule(t, "b-rate", b, "1"),
			b1Rule(t, "c-rate", c, "2"),
		}, f2Schema(f2TransformEdge(a, b), f2TransformEdge(b, c)))
		obs := b1Observation(t, "f2-transform-chain", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"), b1Measure(t, b, "8"), b1Measure(t, c, "4"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
		if err != nil {
			t.Fatalf("transform chain must stay additive, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "26/0" {
			t.Fatalf("transform chain total=%s, want additive 26", total)
		}
	})

	t.Run("distinct_unit_transform_stays_additive", func(t *testing.T) {
		t.Parallel()
		a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
		b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitSecond)
		c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitCount)
		resolved := f2Resolve(t, "f2-unit-transform", []economics.RatingRule{
			b1Rule(t, "a-rate", a, "1"),
			b1Rule(t, "b-rate", b, "1"),
			b1Rule(t, "c-rate", c, "2"),
		}, f2Schema(f2TransformEdge(a, b), f2TransformEdge(b, c)))
		obs := b1Observation(t, "f2-unit-transform", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"), b1Measure(t, b, "8"), b1Measure(t, c, "4"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
		if err != nil {
			t.Fatalf("distinct-unit transform chain must stay additive, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "26/0" {
			t.Fatalf("distinct-unit transform total=%s, want additive 26", total)
		}
	})

	t.Run("cross_direction_chain_stays_additive", func(t *testing.T) {
		t.Parallel()
		a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
		b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
		c := b1Key(metering.DirectionOutput, "vendor:leaf_part", metering.UnitToken)
		resolved := f2Resolve(t, "f2-cross-direction", []economics.RatingRule{
			b1Rule(t, "a-rate", a, "1"),
			b1Rule(t, "c-rate", c, "2"),
		}, f2Schema(f2SubsetEdge(a, b), f2SubsetEdge(b, c)))
		obs := b1Observation(t, "f2-cross-direction", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"), b1Measure(t, b, "8"), b1Measure(t, c, "4"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
		if err != nil {
			t.Fatalf("cross-direction chain must stay additive, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "18/0" {
			t.Fatalf("cross-direction total=%s, want additive 18", total)
		}
	})

	t.Run("cross_scope_chain_stays_additive", func(t *testing.T) {
		t.Parallel()
		a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
		b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
		c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitToken)
		resolved := f2Resolve(t, "f2-cross-scope", []economics.RatingRule{
			b1Rule(t, "a-rate", a, "1"),
			b1Rule(t, "c-rate", c, "2"),
		}, f2Schema(f2SubsetEdge(a, b), f2SubsetEdge(b, c)))
		firstScope := b1Observation(t, "f2-cross-scope-a", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"), b1Measure(t, b, "8"))
		secondScope := b1Observation(t, "f2-cross-scope-b", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, c, "4"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{firstScope, secondScope}))
		if err != nil {
			t.Fatalf("cross-scope chain must stay additive, got %v; lines=%+v", err, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "18/0" {
			t.Fatalf("cross-scope total=%s, want additive 18", total)
		}
	})
}

// TestF2TransitiveInclusionReferenceAndRetailComposition proves the conflict is
// surfaced through the E operator, Q provider-quantity, and customer retail
// seams, with no positive monetary posting on the rejected result.
func TestF2TransitiveInclusionReferenceAndRetailComposition(t *testing.T) {
	t.Parallel()
	a := b1Key(metering.DirectionInput, "vendor:ancestor_total", metering.UnitToken)
	b := b1Key(metering.DirectionInput, "vendor:middle_part", metering.UnitToken)
	c := b1Key(metering.DirectionInput, "vendor:leaf_part", metering.UnitToken)
	resolved := f2Resolve(t, "f2-composition", []economics.RatingRule{
		b1Rule(t, "ancestor-rate", a, "0.01"),
		b1Rule(t, "leaf-rate", c, "0.01"),
	}, f2Schema(f2SubsetEdge(a, b), f2SubsetEdge(b, c)))

	t.Run("e_operator", func(t *testing.T) {
		t.Parallel()
		obs := b1Observation(t, "f2-e", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("E transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
		}
		f2AssertNoPositivePosting(t, val)
	})

	t.Run("q_provider_quantity", func(t *testing.T) {
		t.Parallel()
		obs := b1Observation(t, "f2-q", "b-leg-1", metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40"))
		input := b1OperatorInput(t, resolved, []metering.Observation{obs})
		input.Basis = economics.BasisProviderQuantityLocal
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, err := rater.Rate(context.Background(), input)
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("Q transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
		}
		f2AssertNoPositivePosting(t, val)
	})

	t.Run("customer_retail", func(t *testing.T) {
		t.Parallel()
		obs := b1Observation(t, "f2-retail", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveCustomer,
			b1Measure(t, a, "100"), b1Measure(t, b, "80"), b1Measure(t, c, "40"))
		val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("retail transitive overlap error=%v, want ErrSchemaOverlapConflict; totals=%+v lines=%+v", err, val.Totals, val.Lines)
		}
		f2AssertNoPositivePosting(t, val)
	})
}
