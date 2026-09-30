package billing_test

// N2 (PR #666 reviewer residual): a frozen complete child partition's
// arithmetic evidence must be comparable independently of whether each child
// happens to own an economically relevant rating rule. The exact counterexample
// is an exhaustive partition A = B + C observed as complete A=0, B=60, C=0 in
// one reduction scope/direction/unit, where only B has a $1/unit rule and A/C
// have none. The zero-quantity child C contributes nothing to the sum, yet the
// pre-fix coverage proof stopped at C's missing rule *before* recording the
// child quantity, so the arithmetic contradiction 0 != 60 + 0 was never
// classified: A and C were skipped and B's children-only money looked complete.
//
// Physical consistency must not depend on the presence of an economically
// irrelevant rate. The same observation with a (zero-charge) rule added for C is
// the control: the arithmetic evidence is identical, so both configurations
// must fail closed with the typed partition contradiction. The conserved
// zero/optional-absent and unavailable-child controls pin that the repair does
// not invent a contradiction where comparison is impossible or the quantities
// genuinely agree.

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestN2UnpricedZeroChildPartitionStillContradicts is the exact N2 regression
// driven through both the operator E seam and the customer-policy R seam. The
// B-only configuration has no rule for the zero child C; the B+C control adds a
// zero-charge rule for C. Both are arithmetically identical (A=0, B=60, C=0)
// and must both be classified non-complete with a typed partition contradiction.
func TestN2UnpricedZeroChildPartitionStillContradicts(t *testing.T) {
	t.Parallel()

	bOnly := f3Resolved(t, "n2-unpriced-zero-b-only", []economics.RatingRule{
		b1Rule(t, "n2-b-rate", f3ChildBKey(), "1"),
	}, f3PartitionSchema(false))
	withC := f3Resolved(t, "n2-unpriced-zero-with-c", []economics.RatingRule{
		b1Rule(t, "n2-b-rate", f3ChildBKey(), "1"),
		b1Rule(t, "n2-c-rate", f3ChildCKey(), "3"),
	}, f3PartitionSchema(false))

	obs := f3Observation(t, "n2-unpriced-zero-contradiction",
		b1Measure(t, f3ParentKey(), "0"),
		b1Measure(t, f3ChildBKey(), "60"),
		b1Measure(t, f3ChildCKey(), "0"))

	seams := []struct {
		name string
		rate func(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation) (economics.Valuation, error)
	}{
		{name: "operator_E", rate: f3RateOperator},
		{name: "customer_policy", rate: f3RateCustomer},
	}
	rates := []struct {
		name     string
		resolved economics.TariffSnapshot
	}{
		{name: "b_only_rate", resolved: bOnly},
		{name: "b_plus_c_rate_control", resolved: withC},
	}
	for _, seam := range seams {
		for _, rate := range rates {
			seam, rate := seam, rate
			t.Run(seam.name+"/"+rate.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, rate.resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("unpriced zero child must not hide the contradiction: err=%v, want ErrSchemaPartitionContradiction; completeness=%q lines=%+v",
						err, val.Completeness, val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("completeness=%q, want partial", val.Completeness)
				}
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("contradicted partition must not be complete")
				}
				f3AssertNoParentLine(t, val)
				if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "60/0" {
					t.Fatalf("child B amount=%v, want 60", amount)
				}
				b2aAssertIntactEvidenceRetained(t, obs, val)
			})
		}
	}
}

// TestN2ConservedZeroChildWithoutRuleStaysComplete pins the controls where the
// arithmetic genuinely agrees, so removing the premature rule-resolution stop
// must not fabricate a contradiction. A conserved partition whose required zero
// child owns no rule is still not billable-covered (the parent keeps its
// missing-rate diagnostic), but it must never be a partition contradiction; the
// optional-absent and fully priced variants are conserved and complete.
func TestN2ConservedZeroChildWithoutRuleStaysComplete(t *testing.T) {
	t.Parallel()

	t.Run("required_unpriced_zero_child_stays_partial_not_contradiction", func(t *testing.T) {
		t.Parallel()
		resolved := f3Resolved(t, "n2-conserved-zero-b-only", []economics.RatingRule{
			b1Rule(t, "n2-b-rate", f3ChildBKey(), "1"),
		}, f3PartitionSchema(false))
		obs := f3Observation(t, "n2-conserved-zero-b-only",
			b1Measure(t, f3ParentKey(), "60"),
			b1Measure(t, f3ChildBKey(), "60"),
			b1Measure(t, f3ChildCKey(), "0"))
		val, err := f3RateOperator(t, resolved, obs)
		if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
			t.Fatalf("conserved 60 = 60 + 0 must not be a contradiction: %v", err)
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("completeness=%q, want partial (unpriced required child keeps parent billing diagnostic)", val.Completeness)
		}
		if amount := b2aComponentAmount(t, val, f3ChildBKey()); amount == nil || amount.CanonicalString() != "60/0" {
			t.Fatalf("child B amount=%v, want 60", amount)
		}
	})

	t.Run("all_required_children_priced_stays_complete", func(t *testing.T) {
		t.Parallel()
		resolved := f3Resolved(t, "n2-conserved-zero-priced", f3ChildOnlyRules(t), f3PartitionSchema(false))
		obs := f3Observation(t, "n2-conserved-zero-priced",
			b1Measure(t, f3ParentKey(), "60"),
			b1Measure(t, f3ChildBKey(), "60"),
			b1Measure(t, f3ChildCKey(), "0"))
		val, err := f3RateOperator(t, resolved, obs)
		if err != nil {
			t.Fatalf("conserved zero partition must stay complete, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete", val.Completeness)
		}
	})

	t.Run("absent_optional_child", func(t *testing.T) {
		t.Parallel()
		resolved := f3Resolved(t, "n2-conserved-optional-absent", []economics.RatingRule{
			b1Rule(t, "n2-b-rate", f3ChildBKey(), "1"),
		}, f3PartitionSchema(true))
		obs := f3Observation(t, "n2-conserved-optional-absent",
			b1Measure(t, f3ParentKey(), "60"),
			b1Measure(t, f3ChildBKey(), "60"))
		val, err := f3RateOperator(t, resolved, obs)
		if err != nil {
			t.Fatalf("absent optional child must stay complete, got %v; lines=%+v", err, val.Lines)
		}
		if val.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete", val.Completeness)
		}
		if total := b1PayableTotal(t, val); total != "60/0" {
			t.Fatalf("total=%s, want 60", total)
		}
	})
}

// TestN2UnavailableChildIsNotArithmeticContradiction pins that an unavailable
// child makes the comparison impossible, not contradictory: A=100, B=100 and an
// unavailable C must stay partial through the quantity/evidence diagnostic and
// must never be mislabelled as ErrSchemaPartitionContradiction.
func TestN2UnavailableChildIsNotArithmeticContradiction(t *testing.T) {
	t.Parallel()
	resolved := f3Resolved(t, "n2-unavailable-child", []economics.RatingRule{
		b1Rule(t, "n2-b-rate", f3ChildBKey(), "1"),
	}, f3PartitionSchema(false))
	obs := f3Observation(t, "n2-unavailable-child",
		b1Measure(t, f3ParentKey(), "100"),
		b1Measure(t, f3ChildBKey(), "100"),
		b1UnavailableMeasure(f3ChildCKey()))

	val, err := f3RateOperator(t, resolved, obs)
	if err == nil {
		t.Fatalf("unavailable child must stay partial; total=%s", b1PayableTotal(t, val))
	}
	if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
		t.Fatalf("unavailable child is incomparable, not an arithmetic contradiction: %v", err)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
}
