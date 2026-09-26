package billing_test

// 5B (PR #666 adversarial re-review, TEST-ONLY baseline): four regressions over
// the frozen component-schema conservation and overlap semantics, each driven
// through the operator E seam (BasisLocalExpected) and the customer-policy R
// seam (BasisCustomerPolicy) over a real billingcompose SnapshotCatalog
// publication. All observations are one valid B-leg, one scope/direction/unit,
// complete and nonnegative, with no fees or surcharges. Expected values are
// stated from the handoff arithmetic, never from production helper booleans.
//
// Group 1 (5B-1 primary): arithmetic conservation must be checked before the
// priced-parent inclusion exclusions remove the children from the reduction. A
// priced aggregate A with a contradicted complete partition (100 != 60 + 60)
// must fail closed as ErrSchemaPartitionContradiction/partial, never settle as a
// complete $100. The conserved control (100 = 60 + 40) stays complete $100.
//
// Group 2 (5B-1 secondary): an unpriced, exactly-zero parent whose complete
// partition is ambiguous (a child shared with another declared parent) must not
// hide the inconsistency behind the zero-skip path. It must be a typed
// contradiction or incomparable and never a complete $60.
//
// Group 3 (5B-2): a payable component recursively included through an absent
// intermediate subset (A -> D, but B -> {X, Y} pays for B under A's complete
// partition) must not double charge. The rater must return the typed overlap
// conflict with no payable lines, not a complete $120. The control (no D) is the
// supported disjoint X + Y + C = $100.
//
// Group 4 (5B-3): a redundant subset path where the priced partition child is
// also its own subset descendant (A -> D -> B with A partition B) and the
// intermediate is absent does not duplicate A's charge. It must rate complete
// $100 with B and C emitted exactly once and no extra payable subset.

import (
	"errors"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// review5beSeam is one independent rating seam. The operator E seam and the
// customer-policy R seam must agree on the frozen-schema classification.
type review5beSeam struct {
	name string
	rate func(*testing.T, economics.TariffSnapshot, metering.Observation) (economics.Valuation, error)
}

func review5beSeams() []review5beSeam {
	return []review5beSeam{
		{name: "operator_E", rate: f3RateOperator},
		{name: "customer_policy_R", rate: f3RateCustomer},
	}
}

// review5bePartitionSchema declares parent --partition--> {partA, partB}.
func review5bePartitionSchema(parent, partA, partB metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partA},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
		},
	}}
}

// review5bePayableComponentNames returns the component names of every
// strictly-positive payable quantity line, so a test can prove exactly which
// components were billed.
func review5bePayableComponentNames(val economics.Valuation) []string {
	names := make([]string, 0, len(val.Lines))
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Amount == nil {
			continue
		}
		rat, err := line.Amount.ToRat()
		if err != nil || rat.Sign() <= 0 {
			continue
		}
		if line.Component == nil {
			names = append(names, "<fixed>")
			continue
		}
		names = append(names, line.Component.Component)
	}
	return names
}

// TestReview5beConservationBeforePricingExclusions is 5B-1 primary. The parent A
// is priced, so the included-child exclusion removes B and C from the
// reduction; the conservation check must still see the declared children and
// reject the contradicted partition before that exclusion can hide it.
func TestReview5beConservationBeforePricingExclusions(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:5be1_aggregate_total")
	childB := r7Key("vendor:5be1_part_b")
	childC := r7Key("vendor:5be1_part_c")
	rules := []economics.RatingRule{b1Rule(t, "5be1-parent-rate", parent, "1")}
	resolved := f3Resolved(t, "5be1-conservation-before-pricing", rules,
		review5bePartitionSchema(parent, childB, childC))

	t.Run("primary_contradicted_partition_100_vs_60_plus_60", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "5be1-conservation-contradicted",
			b1Measure(t, parent, "100"),
			b1Measure(t, childB, "60"),
			b1Measure(t, childC, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("%s: priced-parent contradicted partition err=%v, want ErrSchemaPartitionContradiction; completeness=%q totals=%v lines=%+v",
						seam.name, err, val.Completeness, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial (must not be a complete $100 settlement input); totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("%s: contradicted partition must never settle as complete", seam.name)
				}
				b2aAssertIntactEvidenceRetained(t, obs, val)
			})
		}
	})

	t.Run("control_conserved_partition_100_equals_60_plus_40", func(t *testing.T) {
		t.Parallel()
		obs := f3Observation(t, "5be1-conservation-control",
			b1Measure(t, parent, "100"),
			b1Measure(t, childB, "60"),
			b1Measure(t, childC, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: conserved priced-parent partition must rate completely, got %v; totals=%v lines=%+v",
						seam.name, err, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: conserved total=%s, want 100 (parent only; children included, not separately rated)", seam.name, total)
				}
				for _, child := range []metering.ComponentKey{childB, childC} {
					if amount := b2aComponentAmount(t, val, child); amount != nil && amount.CanonicalString() != "0/0" {
						t.Fatalf("%s: included child %s must not be separately rateable, got %s",
							seam.name, child.Component, amount.CanonicalString())
					}
				}
			})
		}
	})
}

// TestReview5beUnpricedZeroDoesNotHideAmbiguousInconsistency is 5B-1 secondary.
// A is a complete, exactly-zero parent whose partition declares C (unpriced,
// zero) and B, which is also declared as a complete child of the unobserved Z.
// The zero-skip path must not turn the ambiguous, contradicted partition into a
// complete $60.
func TestReview5beUnpricedZeroDoesNotHideAmbiguousInconsistency(t *testing.T) {
	t.Parallel()
	parentA := r7Key("vendor:5be2_zero_parent_a")
	parentZ := r7Key("vendor:5be2_zero_parent_z")
	childB := r7Key("vendor:5be2_shared_b")
	childC := r7Key("vendor:5be2_zero_c")
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parentA, Child: childB},
			{Kind: metering.RelationshipPartition, Parent: parentA, Child: childC},
			{Kind: metering.RelationshipPartition, Parent: parentZ, Child: childB},
		},
	}}
	rules := []economics.RatingRule{b1Rule(t, "5be2-shared-b-rate", childB, "1")}
	resolved := f3Resolved(t, "5be2-unpriced-zero-ambiguity", rules, schemas)
	obs := f3Observation(t, "5be2-unpriced-zero-ambiguity",
		b1Measure(t, parentA, "0"),
		b1Measure(t, childB, "60"),
		b1Measure(t, childC, "0"))

	for _, seam := range review5beSeams() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if !errors.Is(err, billing.ErrSchemaPartitionContradiction) && !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
				t.Fatalf("%s: unpriced-zero ambiguous inconsistency err=%v, want typed ErrSchemaPartitionContradiction or ErrSchemaPartitionIncomparable; completeness=%q totals=%v lines=%+v",
					seam.name, err, val.Completeness, val.Totals, val.Lines)
			}
			if val.Completeness == economics.CompletenessComplete {
				t.Fatalf("%s: unpriced-zero ambiguous inconsistency must never settle as complete $60; err=%v totals=%v lines=%+v",
					seam.name, err, val.Totals, val.Lines)
			}
		})
	}
}

// review5beRecursiveSchema declares the 5B-2 graph: A --partition--> {B, C},
// B --partition--> {X, Y}, and A --subset--> D.
func review5beRecursiveSchema(parent, partB, partC, midX, midY, subset metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipPartition, Parent: partB, Child: midX},
			{Kind: metering.RelationshipPartition, Parent: partB, Child: midY},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subset},
		},
	}}
}

// TestReview5beRecursivePaidCoverMustNotDoubleCharge is 5B-2. B is unpriced but
// is paid for by its complete X + Y partition (30 + 30 = 60), and C is priced
// (40), so A's complete partition is accounted for at $100. D is a priced
// subset of A (20), already inside that partition, so billing it additively
// double charges and must fail closed as ErrSchemaOverlapConflict with no
// payable lines, not a complete $120.
func TestReview5beRecursivePaidCoverMustNotDoubleCharge(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:5be3_total")
	partB := r7Key("vendor:5be3_part_b")
	partC := r7Key("vendor:5be3_part_c")
	midX := r7Key("vendor:5be3_mid_x")
	midY := r7Key("vendor:5be3_mid_y")
	subsetD := r7Key("vendor:5be3_subset_d")
	schemas := review5beRecursiveSchema(parent, partB, partC, midX, midY, subsetD)

	t.Run("primary_recursive_cover_plus_priced_subset", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "5be3-x-rate", midX, "1"),
			b1Rule(t, "5be3-y-rate", midY, "1"),
			b1Rule(t, "5be3-c-rate", partC, "1"),
			b1Rule(t, "5be3-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "5be3-recursive-cover", rules, schemas)
		obs := f3Observation(t, "5be3-recursive-cover",
			b1Measure(t, partB, "60"),
			b1Measure(t, midX, "30"),
			b1Measure(t, midY, "30"),
			b1Measure(t, partC, "40"),
			b1Measure(t, subsetD, "20"))

		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: recursive paid cover plus priced subset err=%v, want ErrSchemaOverlapConflict; completeness=%q totals=%v lines=%+v",
						seam.name, err, val.Completeness, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	t.Run("control_disjoint_x_y_c_complete_100", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "5be3-x-rate", midX, "1"),
			b1Rule(t, "5be3-y-rate", midY, "1"),
			b1Rule(t, "5be3-c-rate", partC, "1"),
		}
		resolved := f3Resolved(t, "5be3-recursive-cover-control", rules, schemas)
		obs := f3Observation(t, "5be3-recursive-cover-control",
			b1Measure(t, partB, "60"),
			b1Measure(t, midX, "30"),
			b1Measure(t, midY, "30"),
			b1Measure(t, partC, "40"))

		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: supported disjoint X + Y + C must rate completely, got %v; totals=%v lines=%+v",
						seam.name, err, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: disjoint total=%s, want 100 (X 30 + Y 30 + C 40)", seam.name, total)
				}
			})
		}
	})

	t.Run("primary_absent_b_recursive_cover_plus_priced_subset", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "5be3-x-rate", midX, "1"),
			b1Rule(t, "5be3-y-rate", midY, "1"),
			b1Rule(t, "5be3-c-rate", partC, "1"),
			b1Rule(t, "5be3-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "5be3-absent-b-recursive-cover", rules, schemas)
		obs := f3Observation(t, "5be3-absent-b-recursive-cover",
			b1Measure(t, midX, "30"),
			b1Measure(t, midY, "30"),
			b1Measure(t, partC, "40"),
			b1Measure(t, subsetD, "20"))

		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: absent B nested cover plus priced subset err=%v, want ErrSchemaOverlapConflict; completeness=%q totals=%v lines=%+v",
						seam.name, err, val.Completeness, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	t.Run("control_absent_b_disjoint_x_y_c_complete_100", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "5be3-x-rate", midX, "1"),
			b1Rule(t, "5be3-y-rate", midY, "1"),
			b1Rule(t, "5be3-c-rate", partC, "1"),
		}
		resolved := f3Resolved(t, "5be3-absent-b-recursive-cover-control", rules, schemas)
		obs := f3Observation(t, "5be3-absent-b-recursive-cover-control",
			b1Measure(t, midX, "30"),
			b1Measure(t, midY, "30"),
			b1Measure(t, partC, "40"))

		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: absent B disjoint X + Y + C must rate completely, got %v; totals=%v lines=%+v",
						seam.name, err, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: absent B disjoint total=%s, want 100 (X 30 + Y 30 + C 40)", seam.name, total)
				}
				for _, child := range []metering.ComponentKey{midX, midY, partC} {
					if count := review5beCountComponentLines(val, child); count != 1 {
						t.Fatalf("%s: component %s emitted %d times, want exactly once; lines=%+v",
							seam.name, child.Component, count, val.Lines)
					}
				}
			})
		}
	})

	t.Run("primary_absent_multi_level_cover_plus_priced_subset", func(t *testing.T) {
		t.Parallel()
		// A -> {B, C}; B -> {X, Y}; X -> {P, Q}; A -> D. The intermediate
		// aggregates B and X are both absent; the priced leaves P, Q, Y, C
		// account for A's complete partition through the bounded recursive
		// cover, so subset D is an extra double charge and must fail closed.
		nestedParent := r7Key("vendor:5be3m_total")
		nestedB := r7Key("vendor:5be3m_b")
		nestedC := r7Key("vendor:5be3m_c")
		nestedX := r7Key("vendor:5be3m_x")
		nestedY := r7Key("vendor:5be3m_y")
		nestedP := r7Key("vendor:5be3m_p")
		nestedQ := r7Key("vendor:5be3m_q")
		nestedD := r7Key("vendor:5be3m_d")
		nestedSchemas := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: nestedParent, Child: nestedB},
				{Kind: metering.RelationshipPartition, Parent: nestedParent, Child: nestedC},
				{Kind: metering.RelationshipPartition, Parent: nestedB, Child: nestedX},
				{Kind: metering.RelationshipPartition, Parent: nestedB, Child: nestedY},
				{Kind: metering.RelationshipPartition, Parent: nestedX, Child: nestedP},
				{Kind: metering.RelationshipPartition, Parent: nestedX, Child: nestedQ},
				{Kind: metering.RelationshipSubset, Parent: nestedParent, Child: nestedD},
			},
		}}
		rules := []economics.RatingRule{
			b1Rule(t, "5be3m-p-rate", nestedP, "1"),
			b1Rule(t, "5be3m-q-rate", nestedQ, "1"),
			b1Rule(t, "5be3m-y-rate", nestedY, "1"),
			b1Rule(t, "5be3m-c-rate", nestedC, "1"),
			b1Rule(t, "5be3m-d-rate", nestedD, "1"),
		}
		resolved := f3Resolved(t, "5be3-absent-multilevel-cover", rules, nestedSchemas)
		obs := f3Observation(t, "5be3-absent-multilevel-cover",
			b1Measure(t, nestedP, "10"),
			b1Measure(t, nestedQ, "20"),
			b1Measure(t, nestedY, "30"),
			b1Measure(t, nestedC, "40"),
			b1Measure(t, nestedD, "20"))

		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: multi-level absent cover plus priced subset err=%v, want ErrSchemaOverlapConflict; completeness=%q totals=%v lines=%+v",
						seam.name, err, val.Completeness, val.Totals, val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; totals=%v lines=%+v",
						seam.name, val.Completeness, val.Totals, val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})
}

// review5beRedundantSubsetSchema declares the 5B-3 graph: A --partition-->
// {B, C}, A --subset--> D, and D --subset--> B.
func review5beRedundantSubsetSchema(parent, partB, partC, subsetD metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
			{Kind: metering.RelationshipSubset, Parent: subsetD, Child: partB},
		},
	}}
}

// TestReview5beRedundantSubsetPathDoesNotDuplicateACharge is 5B-3. The priced
// partition child B is also reached through the redundant, absent subset path
// A -> D -> B, but that path reaches the same B already billed by A's complete
// partition, so there is no double charge. The valuation must be complete $100
// with B and C emitted exactly once and no extra payable component.
func TestReview5beRedundantSubsetPathDoesNotDuplicateACharge(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:5be4_total")
	partB := r7Key("vendor:5be4_part_b")
	partC := r7Key("vendor:5be4_part_c")
	subsetD := r7Key("vendor:5be4_subset_d")
	rules := []economics.RatingRule{
		b1Rule(t, "5be4-b-rate", partB, "1"),
		b1Rule(t, "5be4-c-rate", partC, "1"),
	}
	resolved := f3Resolved(t, "5be4-redundant-subset", rules,
		review5beRedundantSubsetSchema(parent, partB, partC, subsetD))
	obs := f3Observation(t, "5be4-redundant-subset",
		b1Measure(t, parent, "100"),
		b1Measure(t, partB, "60"),
		b1Measure(t, partC, "40"))

	for _, seam := range review5beSeams() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if err != nil {
				t.Fatalf("%s: redundant subset path must not duplicate the charge, got %v; completeness=%q totals=%v lines=%+v",
					seam.name, err, val.Completeness, val.Totals, val.Lines)
			}
			if val.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s: completeness=%q, want complete; totals=%v lines=%+v",
					seam.name, val.Completeness, val.Totals, val.Lines)
			}
			if total := b1PayableTotal(t, val); total != "100/0" {
				t.Fatalf("%s: total=%s, want 100 (B 60 + C 40, A covered)", seam.name, total)
			}
			for _, child := range []metering.ComponentKey{partB, partC} {
				if count := review5beCountComponentLines(val, child); count != 1 {
					t.Fatalf("%s: component %s emitted %d times, want exactly once; lines=%+v",
						seam.name, child.Component, count, val.Lines)
				}
			}
			payable := review5bePayableComponentNames(val)
			slices.Sort(payable)
			want := []string{partB.Component, partC.Component}
			slices.Sort(want)
			if !slices.Equal(payable, want) {
				t.Fatalf("%s: payable components=%v, want exactly %v (no additional payable subset); lines=%+v",
					seam.name, payable, want, val.Lines)
			}
		})
	}
}

// TestReview5beRedundantSubsetWithExtraPricedSubsetStillRejected is the 5B-3
// boundary control: the redundant A -> D -> B path reaches the already-paid
// contributor B and must not count as an extra, but a genuinely independent
// priced subset E of A is still a double charge. The contributor subtraction
// must ignore the redundant hit without suppressing the real extra, so both
// seams must fail closed with the typed overlap and no payable line.
func TestReview5beRedundantSubsetWithExtraPricedSubsetStillRejected(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:5be5_total")
	partB := r7Key("vendor:5be5_part_b")
	partC := r7Key("vendor:5be5_part_c")
	subsetD := r7Key("vendor:5be5_subset_d")
	extraE := r7Key("vendor:5be5_extra_e")
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
			{Kind: metering.RelationshipSubset, Parent: subsetD, Child: partB},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: extraE},
		},
	}}
	rules := []economics.RatingRule{
		b1Rule(t, "5be5-b-rate", partB, "1"),
		b1Rule(t, "5be5-c-rate", partC, "1"),
		b1Rule(t, "5be5-e-rate", extraE, "1"),
	}
	resolved := f3Resolved(t, "5be5-redundant-extra", rules, schemas)
	obs := f3Observation(t, "5be5-redundant-extra",
		b1Measure(t, parent, "100"),
		b1Measure(t, partB, "60"),
		b1Measure(t, partC, "40"),
		b1Measure(t, extraE, "20"))

	for _, seam := range review5beSeams() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
				t.Fatalf("%s: redundant path plus independent extra subset err=%v, want ErrSchemaOverlapConflict; completeness=%q totals=%v lines=%+v",
					seam.name, err, val.Completeness, val.Totals, val.Lines)
			}
			if val.Completeness != economics.CompletenessConflict {
				t.Fatalf("%s: completeness=%q, want conflict; totals=%v lines=%+v",
					seam.name, val.Completeness, val.Totals, val.Lines)
			}
			b1AssertNoPayableLines(t, val)
		})
	}
}

// review5beCountComponentLines counts every emitted line for one exact
// component identity, independent of its amount or status.
func review5beCountComponentLines(val economics.Valuation, key metering.ComponentKey) int {
	count := 0
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Direction == key.Direction && line.Component.Component == key.Component &&
			line.Component.Unit == key.Unit && line.Component.SchemaID == key.SchemaID {
			count++
		}
	}
	return count
}
