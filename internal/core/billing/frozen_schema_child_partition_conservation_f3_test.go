package billing_test

// F3 (PR #666 adversarial re-review): a genuinely declared complete
// same-scope/direction/unit partition is still only a *claim*. Before it may
// excuse an unpriced aggregate parent, the rater must verify exact arithmetic
// conservation of the comparable effective reduced quantities: the parent's
// reduced quantity must equal the sum of the present, complete and rateable
// declared children measured in the exact same reduction scope.
//
// An absent optional member contributes zero (the schema's own optional-partition
// semantics), but any present member -- optional or explicit zero -- participates
// in the sum. A contradiction must fail closed as partial/incomparable rather
// than return a complete payable valuation, per parent requirement 3.5: "If
// cache, modality, or reasoning classifications are unavailable or inconsistent,
// the accounting system shall expose partial or incomparable evidence rather
// than assign the residual to an invented category."
//
// The RED counterexample is A -> {B, C} complete with A=100, B=60, C=60 and only
// B and C priced: the structural coverage proof alone excused the unpriced A and
// returned a false complete valuation even though 100 != 60 + 60.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	f3Parent = "vendor:f3_parent_total"
	f3ChildB = "vendor:f3_child_b"
	f3ChildC = "vendor:f3_child_c"
	f3Stream = "f3-conservation-stream"
)

func f3ParentKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, f3Parent, metering.UnitToken)
}

func f3ChildBKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, f3ChildB, metering.UnitToken)
}

func f3ChildCKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, f3ChildC, metering.UnitToken)
}

// f3PartitionSchema declares a complete parent -> {B, C} partition. C is an
// optional member when optionalC is set; B is always required.
func f3PartitionSchema(optionalC bool) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: f3ChildBKey()},
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: f3ChildCKey(), Optional: optionalC},
		},
	}}
}

// f3ChildOnlyRules prices the two children with deliberately distinct rates and
// deliberately omits the aggregate parent.
func f3ChildOnlyRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "f3-b-rate", f3ChildBKey(), "2"),
		b1Rule(t, "f3-c-rate", f3ChildCKey(), "3"),
	}
}

func f3Resolved(t *testing.T, refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	return b1Resolve(t, b1Tariff(t, refID, rules, schemas))
}

// f3RateOperator drives the E-plane (BasisLocalExpected) seam.
func f3RateOperator(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error) {
	t.Helper()
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	return rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
}

// f3RateCustomer drives the customer-policy composition seam over the same
// frozen schema and reduction.
func f3RateCustomer(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error) {
	t.Helper()
	return billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
}

func f3Observation(t *testing.T, id string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	return b1Observation(t, id, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
}

// TestF3ConservationEqualPartitionRatesComplete pins the preserved contract: a
// genuinely complete, conserved partition still excuses the unpriced parent and
// rates the children only. parent 100 = 60 + 40; child-only total 60*2 + 40*3 =
// 240/0.
func TestF3ConservationEqualPartitionRatesComplete(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-equal", f3ChildOnlyRules(t), f3PartitionSchema(false))
	obs := f3Observation(t, "f3-conservation-equal",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "40"))

	for _, seam := range []struct {
		name string
		rate func(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error)
	}{
		{name: "operator_E", rate: f3RateOperator},
		{name: "customer_policy", rate: f3RateCustomer},
	} {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if err != nil {
				t.Fatalf("conserved partition must rate completely, got %v; lines=%+v", err, val.Lines)
			}
			if val.Completeness != economics.CompletenessComplete {
				t.Fatalf("completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
			}
			if total := b1PayableTotal(t, val); total != "240/0" {
				t.Fatalf("total=%s, want 240 (60*2 + 40*3)", total)
			}
			f3AssertNoParentLine(t, val)
			b2aAssertIntactEvidenceRetained(t, obs, val)
		})
	}
}

// TestF3ConservationOverflowIsNotComplete is the exact RED counterexample:
// parent 100 but children 60 + 60 = 120. The declared complete partition is
// arithmetically contradicted, so the unpriced parent must keep its missing-rate
// diagnostic and the valuation must not report a complete payable total.
func TestF3ConservationOverflowIsNotComplete(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-overflow", f3ChildOnlyRules(t), f3PartitionSchema(false))
	obs := f3Observation(t, "f3-conservation-overflow",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))

	for _, seam := range []struct {
		name string
		rate func(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error)
	}{
		{name: "operator_E", rate: f3RateOperator},
		{name: "customer_policy", rate: f3RateCustomer},
	} {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if err == nil {
				t.Fatalf("contradicted partition must not be complete; total=%s lines=%+v", b1PayableTotal(t, val), val.Lines)
			}
			if val.Completeness != economics.CompletenessPartial {
				t.Fatalf("completeness=%q, want partial (no invented residual)", val.Completeness)
			}
			f3AssertParentMissing(t, val)
			if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
				t.Fatalf("child B amount=%v, want 120", amount)
			}
			if amount := b2aComponentAmount(t, val, f3ChildCKey()); amount == nil || amount.CanonicalString() != "180/0" {
				t.Fatalf("child C amount=%v, want 180", amount)
			}
			b2aAssertIntactEvidenceRetained(t, obs, val)
		})
	}
}

// TestF3ConservationUnderflowIsNotComplete is the under-reported counterpart:
// parent 100 but children 60 + 30 = 90.
func TestF3ConservationUnderflowIsNotComplete(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-underflow", f3ChildOnlyRules(t), f3PartitionSchema(false))
	obs := f3Observation(t, "f3-conservation-underflow",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "30"))

	val, err := f3RateOperator(t, resolved, obs)
	if err == nil {
		t.Fatalf("under-reported partition must not be complete; total=%s lines=%+v", b1PayableTotal(t, val), val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	f3AssertParentMissing(t, val)
}

// TestF3ConservationFractionalExactUnitsRatesComplete proves the comparison is
// exact over fractional quantities. UnitSecond accepts non-integer values (token
// units require exact integers): parent 1.5 = 0.75 + 0.75.
func TestF3ConservationFractionalExactUnitsRatesComplete(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:f3_seconds_parent", metering.UnitSecond)
	childB := b1Key(metering.DirectionInput, "vendor:f3_seconds_b", metering.UnitSecond)
	childC := b1Key(metering.DirectionInput, "vendor:f3_seconds_c", metering.UnitSecond)
	rules := []economics.RatingRule{
		b1Rule(t, "f3-seconds-b", childB, "2"),
		b1Rule(t, "f3-seconds-c", childC, "3"),
	}
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: childB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: childC},
		},
	}}
	resolved := f3Resolved(t, "f3-conservation-fractional", rules, schemas)
	obs := f3Observation(t, "f3-conservation-fractional",
		b1Measure(t, parent, "1.5"),
		b1Measure(t, childB, "0.75"),
		b1Measure(t, childC, "0.75"))

	val, err := f3RateOperator(t, resolved, obs)
	if err != nil {
		t.Fatalf("fractional conserved partition must rate completely, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete", val.Completeness)
	}
	want := b1Decimal(t, "3.75").CanonicalString()
	if total := b1PayableTotal(t, val); total != want {
		t.Fatalf("fractional total=%s, want %s", total, want)
	}
}

// TestF3ConservationExplicitZeroParticipates proves an explicitly observed zero
// child is a real member: parent 100 = 100 + 0 is conserved and complete, while
// parent 100 != 100 + 1 is contested even though the extra member is a single
// fractional unit.
func TestF3ConservationExplicitZeroParticipates(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-zero", f3ChildOnlyRules(t), f3PartitionSchema(false))

	equal := f3Observation(t, "f3-conservation-zero-equal",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "100"),
		b1Measure(t, f3ChildCKey(), "0"))
	val, err := f3RateOperator(t, resolved, equal)
	if err != nil {
		t.Fatalf("explicit-zero conserved partition must be complete, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("explicit-zero completeness=%q, want complete", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "200/0" {
		t.Fatalf("explicit-zero total=%s, want 200", total)
	}

	contested := f3Observation(t, "f3-conservation-zero-contested",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "100"),
		b1Measure(t, f3ChildCKey(), "1"))
	cval, cerr := f3RateOperator(t, resolved, contested)
	if cerr == nil {
		t.Fatalf("contested explicit-zero partition must not be complete; total=%s", b1PayableTotal(t, cval))
	}
	if cval.Completeness != economics.CompletenessPartial {
		t.Fatalf("contested completeness=%q, want partial", cval.Completeness)
	}
}

// TestF3ConservationOptionalMemberSemantics pins the R7 optional rule: an absent
// optional member contributes zero and preserves complete coverage, but a
// present optional member participates in the sum. The three subcases reuse one
// optional-partition schema so absence, conserved presence and contested
// presence are directly comparable.
func TestF3ConservationOptionalMemberSemantics(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-optional", f3ChildOnlyRules(t), f3PartitionSchema(true))

	t.Run("absent_optional_is_zero", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "f3-optional-absent",
			b1Measure(t, f3ParentKey(), "100"),
			b1Measure(t, f3ChildBKey(), "100"))
		val, err := f3RateOperator(t, resolved, obs)
		if err != nil {
			t.Fatalf("absent optional member must keep complete coverage, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete", val.Completeness)
		}
		if total := b1PayableTotal(t, val); total != "200/0" {
			t.Fatalf("total=%s, want 200 (100*2, optional absent)", total)
		}
	})

	t.Run("present_optional_zero_conserved", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "f3-optional-zero",
			b1Measure(t, f3ParentKey(), "100"),
			b1Measure(t, f3ChildBKey(), "100"),
			b1Measure(t, f3ChildCKey(), "0"))
		val, err := f3RateOperator(t, resolved, obs)
		if err != nil {
			t.Fatalf("present optional zero must stay complete, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete", val.Completeness)
		}
	})

	t.Run("present_optional_participates_in_sum", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "f3-optional-present",
			b1Measure(t, f3ParentKey(), "100"),
			b1Measure(t, f3ChildBKey(), "100"),
			b1Measure(t, f3ChildCKey(), "30"))
		val, err := f3RateOperator(t, resolved, obs)
		if err == nil {
			t.Fatalf("present optional member must participate in the sum; total=%s", b1PayableTotal(t, val))
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("completeness=%q, want partial", val.Completeness)
		}
		f3AssertParentMissing(t, val)
	})
}

// TestF3ConservationMissingOrUnavailableChildStaysPartial pins the existing
// structural fail-closed boundary independent of arithmetic: a required absent
// child and an unavailable child can never prove conservation.
func TestF3ConservationMissingOrUnavailableChildStaysPartial(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-gap", f3ChildOnlyRules(t), f3PartitionSchema(false))

	t.Run("missing_required_child", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "f3-missing-child",
			b1Measure(t, f3ParentKey(), "100"),
			b1Measure(t, f3ChildBKey(), "100"))
		val, err := f3RateOperator(t, resolved, obs)
		if err == nil {
			t.Fatalf("missing required child must stay partial; total=%s", b1PayableTotal(t, val))
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("completeness=%q, want partial", val.Completeness)
		}
	})

	t.Run("unavailable_child", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "f3-unavailable-child",
			b1Measure(t, f3ParentKey(), "100"),
			b1Measure(t, f3ChildBKey(), "100"),
			b1UnavailableMeasure(f3ChildCKey()))
		val, err := f3RateOperator(t, resolved, obs)
		if err == nil {
			t.Fatalf("unavailable child must stay partial; total=%s", b1PayableTotal(t, val))
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("completeness=%q, want partial", val.Completeness)
		}
	})
}

// TestF3PricedParentInformationalChildrenUnaffected proves conservation never
// suppresses a genuinely priced parent: when the aggregate parent owns the rate
// and the children are informational (no resolving rule), the parent is billed
// on its own quantity and children are not independently rated. No coverage
// proof is needed or applied.
func TestF3PricedParentInformationalChildrenUnaffected(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{b1Rule(t, "f3-parent-rate", f3ParentKey(), "2")}
	resolved := f3Resolved(t, "f3-priced-parent", rules, f3PartitionSchema(false))
	obs := f3Observation(t, "f3-priced-parent",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "40"))

	val, err := f3RateOperator(t, resolved, obs)
	if err != nil {
		t.Fatalf("priced parent must rate normally, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "200/0" {
		t.Fatalf("total=%s, want 200 (100*2 parent only)", total)
	}
	if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount != nil && amount.CanonicalString() != "0/0" {
		t.Fatalf("informational child B must not be billed separately: %v", amount)
	}
}

// TestF3ConservationLaterCorrectedRevision proves the comparison uses the
// effective reduced quantities, not stale history: the historical mismatch
// parent 100 = 60 + 60 is superseded by a correction parent 100 = 60 + 40, so
// the effective partition is conserved and rates completely.
func TestF3ConservationLaterCorrectedRevision(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-correction", f3ChildOnlyRules(t), f3PartitionSchema(false))

	historical := f3Observation(t, "f3-conservation-historical",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))
	historical.Revision = 1
	historical.Sequence = 1
	historical.StreamID = f3Stream
	historicalRef, err := historical.Ref(historical.Subject.StoreID)
	if err != nil {
		t.Fatalf("historical ref: %v", err)
	}

	correction := f3Observation(t, "f3-conservation-correction",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "40"))
	correction.Semantics = metering.SemanticsReplacement
	correction.Supersedes = []metering.ObservationRef{historicalRef}
	correction.Revision = 2
	correction.Sequence = 2
	correction.StreamID = f3Stream

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{historical, correction}))
	if rateErr != nil {
		t.Fatalf("corrected conserved partition must rate completely, got %v; lines=%+v", rateErr, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "240/0" {
		t.Fatalf("corrected total=%s, want 240", total)
	}
	b2bAssertNoParentLine(t, val)
	b2aAssertAuditRefRetained(t, historical, val)
	b2aAssertAuditRefRetained(t, correction, val)
}

// TestF3ConservationProviderQuantitySeam proves the conservation check is not
// operator-only: the Q plane (BasisProviderQuantityLocal over provider-origin
// evidence) enforces the same exact sum, so a provider quantity contradiction
// stays partial and a conserved partition rates complete.
func TestF3ConservationProviderQuantitySeam(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-conservation-provider-quantity", f3ChildOnlyRules(t), f3PartitionSchema(false))
	rateQ := func(t *testing.T, obs metering.Observation) (economics.Valuation, error) {
		t.Helper()
		input := b1OperatorInput(t, resolved, []metering.Observation{obs})
		input.Basis = economics.BasisProviderQuantityLocal
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		return rater.Rate(context.Background(), input)
	}

	equal := b1Observation(t, "f3-q-equal", "b-leg-1", metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "40"))
	val, err := rateQ(t, equal)
	if err != nil {
		t.Fatalf("conserved Q partition must rate completely, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("Q completeness=%q, want complete", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "240/0" {
		t.Fatalf("Q total=%s, want 240", total)
	}

	contested := b1Observation(t, "f3-q-contested", "b-leg-1", metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))
	cval, cerr := rateQ(t, contested)
	if cerr == nil {
		t.Fatalf("contradicted Q partition must stay partial; total=%s", b1PayableTotal(t, cval))
	}
	if cval.Completeness != economics.CompletenessPartial {
		t.Fatalf("Q completeness=%q, want partial", cval.Completeness)
	}
	f3AssertParentMissing(t, cval)
}

// TestF3ZeroParentPartitionContradictionStaysPartial is the first Astra
// blocking vector: a genuine complete partition A=0, B=60, C=60 where only B and
// C are priced. The parent is complete and exactly zero, so component_rater's
// independent zero-quantity skip drops it before it can carry a missing-rate
// diagnostic. The declared partition is still arithmetically contradicted
// (0 != 120), so the valuation must be classified partial by the bounded
// partition-contradiction rule, never complete.
func TestF3ZeroParentPartitionContradictionStaysPartial(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-zero-contradiction", f3ChildOnlyRules(t), f3PartitionSchema(false))
	obs := f3Observation(t, "f3-zero-contradiction",
		b1Measure(t, f3ParentKey(), "0"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))

	for _, seam := range []struct {
		name string
		rate func(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error)
	}{
		{name: "operator_E", rate: f3RateOperator},
		{name: "customer_policy", rate: f3RateCustomer},
	} {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
				t.Fatalf("zero-parent contradiction err=%v, want ErrSchemaPartitionContradiction; completeness=%q lines=%+v", err, val.Completeness, val.Lines)
			}
			if val.Completeness != economics.CompletenessPartial {
				t.Fatalf("completeness=%q, want partial", val.Completeness)
			}
			if val.Completeness == economics.CompletenessComplete {
				t.Fatalf("zero-parent contradiction must not be complete")
			}
			f3AssertNoParentLine(t, val)
			if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
				t.Fatalf("child B amount=%v, want 120", amount)
			}
			if amount := b2aComponentAmount(t, val, f3ChildCKey()); amount == nil || amount.CanonicalString() != "180/0" {
				t.Fatalf("child C amount=%v, want 180", amount)
			}
			b2aAssertIntactEvidenceRetained(t, obs, val)
		})
	}
}

// TestF3InformationalParentPartitionContradictionStaysPartial is the second
// Astra blocking vector: an informational input_token_total parent A=100 with
// children B=60, C=60 priced. The informational parent is skipped from line
// emission by the rating loop, so no missing-rate diagnostic is produced; the
// partition contradiction must nonetheless fail the valuation closed as partial.
func TestF3InformationalParentPartitionContradictionStaysPartial(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: f3ChildBKey()},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: f3ChildCKey()},
		},
	}}
	resolved := f3Resolved(t, "f3-informational-contradiction", f3ChildOnlyRules(t), schemas)
	obs := f3Observation(t, "f3-informational-contradiction",
		b1Measure(t, parent, "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))

	val, err := f3RateOperator(t, resolved, obs)
	if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("informational-parent contradiction err=%v, want ErrSchemaPartitionContradiction; completeness=%q lines=%+v", err, val.Completeness, val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	for _, line := range val.Lines {
		if line.Component != nil && line.Component.Component == metering.ComponentInputTokenTotal {
			t.Fatalf("informational parent must not be emitted as a payable/diagnostic line: %+v", line)
		}
	}
	if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
		t.Fatalf("child B amount=%v, want 120", amount)
	}
	if amount := b2aComponentAmount(t, val, f3ChildCKey()); amount == nil || amount.CanonicalString() != "180/0" {
		t.Fatalf("child C amount=%v, want 180", amount)
	}
	b2aAssertIntactEvidenceRetained(t, obs, val)
}

// TestF3ZeroParentPartitionContradictionFailsRetailSettlement drives the same
// zero-parent contradiction through the selected-retail settlement seam: the
// customer charge must never be settled from a contradicted partition.
func TestF3ZeroParentPartitionContradictionFailsRetailSettlement(t *testing.T) {
	t.Parallel()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatalf("NewBillingCallID: %v", err)
	}
	obs := b2aRetailObservation(t, callID, "f3-zero-retail", []metering.Measure{
		b1Measure(t, f3ParentKey(), "0"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"),
	})
	call, leg, policy := b2aRetailCall(t, callID, obs)
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatalf("SelectRetailBLegEvidence: %v", err)
	}
	resolved := f3Resolved(t, b2aRetailTariffID, f3ChildOnlyRules(t), f3PartitionSchema(false))
	result, err := billing.RateSelectedRetailBLegs(context.Background(), billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: resolved, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("retail settlement err=%v, want ErrSchemaPartitionContradiction; completeness=%q lines=%+v", err, result.InferenceValuation.Completeness, result.InferenceValuation.Lines)
	}
	if !errors.Is(err, billing.ErrRetailRateIncomplete) {
		t.Fatalf("retail settlement err=%v, want ErrRetailRateIncomplete", err)
	}
	if result.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("retail completeness=%q, want partial", result.InferenceValuation.Completeness)
	}
	if result.CustomerCharge.Nano != 0 || result.CustomerCharge.Currency != "" {
		t.Fatalf("contradicted partition must not settle a customer charge: %+v", result.CustomerCharge)
	}
}

// TestF3AmbiguousSiblingDeclarationDoesNotBypassLocalContradiction is the
// remaining Astra bypass vector. The frozen schema declares an unrelated
// ambiguous pair (D->X and E->X share X) alongside an independent unambiguous
// complete partition A->{B,C}. A global ambiguity flag previously disabled the
// whole arithmetic proof, so A=0 with B=60 and C=60 was skipped as an unpriced
// zero parent and the children-only money returned complete. Ambiguity must stay
// conservative for coverage but be isolated per parent: A does not declare the
// shared child, so its bounded contradiction must still be diagnosed.
func TestF3AmbiguousSiblingDeclarationDoesNotBypassLocalContradiction(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "f3-ambiguous-sibling", f3ChildOnlyRules(t), f3AmbiguousSiblingSchema())
	obs := f3Observation(t, "f3-ambiguous-sibling",
		b1Measure(t, f3ParentKey(), "0"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "60"))

	val, err := f3RateOperator(t, resolved, obs)
	if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("ambiguous sibling must not bypass A's contradiction: err=%v, want ErrSchemaPartitionContradiction; completeness=%q lines=%+v",
			err, val.Completeness, val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	f3AssertNoParentLine(t, val)
	if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
		t.Fatalf("child B amount=%v, want 120", amount)
	}
	if amount := b2aComponentAmount(t, val, f3ChildCKey()); amount == nil || amount.CanonicalString() != "180/0" {
		t.Fatalf("child C amount=%v, want 180", amount)
	}
	b2aAssertIntactEvidenceRetained(t, obs, val)
}

// TestF3SharedChildInOwnPartitionStaysConservative pins the boundary of the
// per-parent isolation: when A itself declares the shared child, A's partition
// membership cannot be trusted and its child sum is not comparable, so the
// classification is explicitly partial/incomparable -- never an arithmetic
// contradiction -- and A is never falsely covered. The unpriced positive parent
// keeps its missing-rate diagnostic.
func TestF3SharedChildInOwnPartitionStaysConservative(t *testing.T) {
	t.Parallel()
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	other := b1Key(metering.DirectionInput, "vendor:f3_parent_d", metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: f3ChildBKey()},
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: shared},
			{Kind: metering.RelationshipPartition, Parent: other, Child: shared},
		},
	}}
	rules := []economics.RatingRule{
		b1Rule(t, "f3-b-rate", f3ChildBKey(), "2"),
		b1Rule(t, "f3-shared-rate", shared, "5"),
	}
	resolved := f3Resolved(t, "f3-shared-child-conservative", rules, schemas)
	obs := f3Observation(t, "f3-shared-child-conservative",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, shared, "30"))

	val, err := f3RateOperator(t, resolved, obs)
	if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("a shared-child partition must not be mislabelled an arithmetic contradiction: %v", err)
	}
	if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
		t.Fatalf("a shared-child partition err=%v, want ErrSchemaPartitionIncomparable", err)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial (no false coverage); lines=%+v", val.Completeness, val.Lines)
	}
	f3AssertParentMissing(t, val)
}

// f3TaintedParentSchema declares parent -> {B, shared} complete plus an
// unrelated D -> shared, so parent is tainted by shared-child ambiguity while D
// is unobserved.
func f3TaintedParentSchema(parent metering.ComponentKey) []metering.ComponentSchema {
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	other := b1Key(metering.DirectionInput, "vendor:f3_parent_d", metering.UnitToken)
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: f3ChildBKey()},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: shared},
			{Kind: metering.RelationshipPartition, Parent: other, Child: shared},
		},
	}}
}

func f3TaintedRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	return []economics.RatingRule{
		b1Rule(t, "f3-b-rate", f3ChildBKey(), "2"),
		b1Rule(t, "f3-shared-rate", shared, "5"),
	}
}

// TestF3TaintedZeroParentPartitionIncomparable is the Astra tainted-zero bypass
// vector: A=0 observed but tainted (it declares shared, which D also declares),
// B=60 and shared=30 priced. The zero parent is skipped from line emission, so
// the valuer must still emit an explicit partial/incomparable classification and
// must never report the child-only 270/0 as complete.
func TestF3TaintedZeroParentPartitionIncomparable(t *testing.T) {
	t.Parallel()
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	resolved := f3Resolved(t, "f3-tainted-zero", f3TaintedRules(t), f3TaintedParentSchema(f3ParentKey()))
	obs := f3Observation(t, "f3-tainted-zero",
		b1Measure(t, f3ParentKey(), "0"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, shared, "30"))

	val, err := f3RateOperator(t, resolved, obs)
	if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("shared-child partition must be incomparable, not an arithmetic contradiction: %v", err)
	}
	if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
		t.Fatalf("tainted zero parent err=%v, want ErrSchemaPartitionIncomparable; completeness=%q lines=%+v", err, val.Completeness, val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("tainted zero parent must not be complete")
	}
	f3AssertNoParentLine(t, val)
	if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
		t.Fatalf("child B amount=%v, want 120", amount)
	}
	if amount := b2aComponentAmount(t, val, shared); amount == nil || amount.CanonicalString() != "150/0" {
		t.Fatalf("shared child amount=%v, want 150", amount)
	}
	b2aAssertIntactEvidenceRetained(t, obs, val)
}

// TestF3TaintedInformationalParentPartitionIncomparable is the informational
// counterpart: the informational parent is skipped from line emission, but its
// tainted partition must still classify the valuation partial/incomparable.
func TestF3TaintedInformationalParentPartitionIncomparable(t *testing.T) {
	t.Parallel()
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	parent := b1Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	resolved := f3Resolved(t, "f3-tainted-informational", f3TaintedRules(t), f3TaintedParentSchema(parent))
	obs := f3Observation(t, "f3-tainted-informational",
		b1Measure(t, parent, "100"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, shared, "30"))

	val, err := f3RateOperator(t, resolved, obs)
	if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("informational shared-child partition must not be a contradiction: %v", err)
	}
	if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
		t.Fatalf("tainted informational parent err=%v, want ErrSchemaPartitionIncomparable; completeness=%q lines=%+v", err, val.Completeness, val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	for _, line := range val.Lines {
		if line.Component != nil && line.Component.Component == metering.ComponentInputTokenTotal {
			t.Fatalf("informational parent must not be emitted: %+v", line)
		}
	}
	if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "120/0" {
		t.Fatalf("child B amount=%v, want 120", amount)
	}
	if amount := b2aComponentAmount(t, val, shared); amount == nil || amount.CanonicalString() != "150/0" {
		t.Fatalf("shared child amount=%v, want 150", amount)
	}
}

// TestF3UnobservedTaintedParentDoesNotFailValuation is the control: an
// unobserved tainted parent (D -> shared and E -> shared, with D and E absent)
// must not, by itself, fail an otherwise complete valuation. The observed shared
// child is simply an independently priced component.
func TestF3UnobservedTaintedParentDoesNotFailValuation(t *testing.T) {
	t.Parallel()
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	first := b1Key(metering.DirectionInput, "vendor:f3_parent_d", metering.UnitToken)
	second := b1Key(metering.DirectionInput, "vendor:f3_parent_e", metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: first, Child: shared},
			{Kind: metering.RelationshipPartition, Parent: second, Child: shared},
		},
	}}
	rules := []economics.RatingRule{b1Rule(t, "f3-shared-rate", shared, "5")}
	resolved := f3Resolved(t, "f3-unobserved-tainted", rules, schemas)
	obs := f3Observation(t, "f3-unobserved-tainted", b1Measure(t, shared, "30"))

	val, err := f3RateOperator(t, resolved, obs)
	if err != nil {
		t.Fatalf("unobserved tainted schemas must not fail the valuation, got %v; completeness=%q", err, val.Completeness)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "150/0" {
		t.Fatalf("total=%s, want 150", total)
	}
}

// f3AmbiguousSiblingSchema pairs the independent A->{B,C} partition with an
// unrelated D->X / E->X pair that shares X. Only D and E are ambiguous; A is
// untainted.
func f3AmbiguousSiblingSchema() []metering.ComponentSchema {
	shared := b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken)
	first := b1Key(metering.DirectionInput, "vendor:f3_parent_d", metering.UnitToken)
	second := b1Key(metering.DirectionInput, "vendor:f3_parent_e", metering.UnitToken)
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: f3ChildBKey()},
			{Kind: metering.RelationshipPartition, Parent: f3ParentKey(), Child: f3ChildCKey()},
			{Kind: metering.RelationshipPartition, Parent: first, Child: shared},
			{Kind: metering.RelationshipPartition, Parent: second, Child: shared},
		},
	}}
}

// f3AssertNoParentLine proves the conserved unpriced aggregate parent is not
// emitted as a rate-missing or partial line.
func f3AssertNoParentLine(t *testing.T, val economics.Valuation) {
	t.Helper()
	for _, line := range val.Lines {
		if line.Component == nil {
			continue
		}
		if line.Component.Component == f3Parent {
			t.Fatalf("conserved aggregate parent line must not be emitted: %+v", line)
		}
	}
}

// f3AssertParentMissing proves the contradicted unpriced parent retains its
// explicit missing-rate diagnostic rather than being silently suppressed.
func f3AssertParentMissing(t *testing.T, val economics.Valuation) {
	t.Helper()
	line := b2aComponentLine(t, val, f3ParentKey())
	if line == nil {
		t.Fatalf("contradicted aggregate parent diagnostic missing; lines=%+v", val.Lines)
	}
	if line.Status != economics.RatingLineRateMissing {
		t.Fatalf("aggregate parent status=%q, want missing rate; line=%+v", line.Status, line)
	}
}
