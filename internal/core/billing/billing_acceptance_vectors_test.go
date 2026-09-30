package billing_test

// Consolidated acceptance vectors for the frozen component-schema rating state
// machine (PR #666 adversarial repair, TEST-ONLY).
//
// This file is a PIN, not new coverage. Every vector below is an accepted
// requirement of the schema contract, and most of them are also already proven
// by a focused review suite. Those are cross-referenced per vector in
// accVector.coveredBy instead of being re-derived here: the purpose is one
// named, individually identifiable index of the whole contract that fails on the
// exact vector that regressed, not a second implementation of the proofs.
//
// Every vector asserts the SAME observable tuple, so a regression is reported
// once with the tuple that broke:
//
//	typed error class   errors.Is against the real production sentinels
//	completeness        economics.Completeness
//	emitted components  every line identity the rater emitted
//	payable components  every strictly-positive money line
//	payable total       economics.CurrencyTotal.Amount
//
// Every vector runs through BOTH review5beSeams (operator E / customer-policy
// R), so the frozen-schema classification cannot differ between the two
// independent rating planes.
//
// Vectors that are NEGATIVE CONTROLS (the ratified non-free-only boundary, the
// strict-containment boundary, the exact-equality boundary, transform and
// cross-scope isolation, the redundant-path controls) assert exactly that they
// stay valid and complete. They are the half of the contract that a
// fail-closed-only test suite would silently break, so they are pinned just as
// hard as the rejections.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ---------------------------------------------------------------------------
// Vector harness
// ---------------------------------------------------------------------------

// accKey is the single component-identity factory for this file. One
// schema-qualified, input-direction, token-unit namespace keeps every vector
// independent of component-name heuristics.
func accKey(role string) metering.ComponentKey {
	return r7Key("vendor:acc_" + role)
}

// accName is the emitted-line identity form of an accKey role, so a vector's
// expectation table reads as role names rather than full vendor strings.
func accName(role string) string { return accKey(role).Component }

// accUnitKey is accKey for a different unit, used by the unit-isolation vector.
func accUnitKey(role, unit string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: accKey(role).Component,
		Unit: unit, SchemaID: accKey(role).SchemaID,
	}
}

// accDimKey is accKey with a dimension qualifier, used by the identity vector.
func accDimKey(role, dimension string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: accKey(role).Component,
		Unit: metering.UnitToken, SchemaID: accKey(role).SchemaID,
		Dimensions: []metering.Dimension{{Name: "tier", Value: dimension}},
	}
}

// accCase is one fully built rating input: the frozen relationship set, the
// pricing rules, and the observations that carry the evidence. Most vectors use
// one observation; the scope-isolation vectors supply several.
type accCase struct {
	relationships []metering.ComponentRelationship
	rules         []economics.RatingRule
	observations  []metering.Observation
}

// accExpect is the observable tuple a vector pins.
type accExpect struct {
	// classes are the production sentinels that must match errors.Is. A vector
	// with no classes and wantNoErr set pins a clean rate.
	classes []error
	// notClasses are the production sentinels that must NOT match errors.Is.
	// Several vectors are only interesting because a nearby, more alarming
	// class is absent: an unprovable cover is a missing operand, not an
	// arithmetic contradiction. Pinning the absence is the assertion.
	notClasses []error
	// errContains are substrings the diagnostic must carry. It exists for the
	// input-validation fences, whose sentinel is flattened to text by an
	// intermediate %v wrap and therefore cannot be matched with errors.Is.
	errContains []string
	// wantNoErr requires err == nil. It is mutually exclusive with classes.
	wantNoErr bool
	// completeness is the exact economics.Completeness the rater must report.
	completeness economics.Completeness
	// total is the canonical payable total, or "<none>" when the valuation
	// carries no total at all.
	total string
	// emittedComponents is the exact sorted set of component names of every
	// emitted line, or nil to pin only the payable subset.
	emittedComponents []string
	// payable is the exact sorted set of strictly-positive payable component
	// names, or nil to pin only the emitted-line set.
	payable []string
	// noPayableComponentLines requires that no quantity line carries money.
	// A fixed-fee line may still be payable, which is the vector-31 contract.
	noPayableComponentLines bool
	// fixedFeePayable requires the independent call-scope fixed fee to stay
	// payable, i.e. the conflict suppresses quantity money only.
	fixedFeePayable bool
}

// accVector is one named, individually identifiable acceptance vector.
type accVector struct {
	number int
	name   string
	// coveredBy cross-references the existing focused suite that already proves
	// this vector. It is documentation of the consolidated index, not a
	// second assertion: the existing coverage is left in place untouched.
	coveredBy string
	build     func(t *testing.T) accCase
	expect    accExpect
	// bySeam replaces the expectation for one named seam where the two rating
	// planes legitimately differ, so each difference is pinned explicitly
	// instead of being papered over by a loosest-common-denominator assertion.
	bySeam map[string]accExpect
}

// accSchemaSentinels is every frozen-schema failure sentinel. A vector that
// must NOT be any of them pins the whole set at once.
var accSchemaSentinels = []error{
	billing.ErrSchemaPartitionContradiction,
	billing.ErrSchemaPartitionIncomplete,
	billing.ErrSchemaPartitionIncomparable,
	billing.ErrSchemaSubsetContradiction,
	billing.ErrSchemaQuantityContradiction,
	billing.ErrSchemaOverlapConflict,
}

// accPartition declares parent --partition--> children as complete coverage,
// marking the member at optionalChild optional (-1 for none).
func accPartition(parent metering.ComponentKey, optionalChild int, children ...metering.ComponentKey) []metering.ComponentRelationship {
	relationships := make([]metering.ComponentRelationship, 0, len(children))
	for i, child := range children {
		relationships = append(relationships, metering.ComponentRelationship{
			Kind: metering.RelationshipPartition, Parent: parent, Child: child, Optional: i == optionalChild,
		})
	}
	return relationships
}

// accEdge appends one non-partition relationship to an existing set.
func accEdge(relationships []metering.ComponentRelationship, kind metering.RelationshipKind, parent, child metering.ComponentKey) []metering.ComponentRelationship {
	return append(relationships, metering.ComponentRelationship{Kind: kind, Parent: parent, Child: child})
}

// accObs builds one operator-perspective observation in its own B-leg scope.
func accObs(t *testing.T, id, bLegID string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	return b1Observation(t, id, bLegID, metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
}

// accRule is the shared linear rule builder for this file.
func accRule(t *testing.T, id string, key metering.ComponentKey, price string) economics.RatingRule {
	t.Helper()
	return b1Rule(t, id, key, price)
}

// accTiered builds an all-units tiered whole-context rule whose single boundary
// at boundary separates two prices, so the SELECTED tier is observable in the
// emitted amount.
func accTiered(t *testing.T, id string, key metering.ComponentKey, boundary, lowPrice, highPrice string) economics.RatingRule {
	t.Helper()
	rule := b1Rule(t, id, key, lowPrice)
	rule.Kind = economics.RatingRuleAllUnits
	rule.UnitPrice = nil
	rule.SelectionScope = economics.SelectionWholeContext
	rule.TierMode = economics.TierAllUnits
	upTo, low, high := b1Decimal(t, boundary), b1Decimal(t, lowPrice), b1Decimal(t, highPrice)
	rule.Tiers = []economics.RatingTier{
		{UpTo: &upTo, UnitPrice: &low},
		{UnitPrice: &high},
	}
	return rule
}

// accTierLadder builds a whole-context all-units rule from an explicit
// (upTo, price) ladder, so a test can place tier boundaries either side of the
// honest context quantity and therefore witness the SELECTED tier in the
// emitted amount.
func accTierLadder(t *testing.T, id string, key metering.ComponentKey, tiers ...[2]string) economics.RatingRule {
	t.Helper()
	rule := b1Rule(t, id, key, "1")
	rule.Kind = economics.RatingRuleAllUnits
	rule.UnitPrice = nil
	rule.SelectionScope = economics.SelectionWholeContext
	rule.TierMode = economics.TierAllUnits
	rule.Tiers = make([]economics.RatingTier, 0, len(tiers))
	for _, tier := range tiers {
		price := b1Decimal(t, tier[1])
		entry := economics.RatingTier{UnitPrice: &price}
		if tier[0] != "" {
			upTo := b1Decimal(t, tier[0])
			entry.UpTo = &upTo
		}
		rule.Tiers = append(rule.Tiers, entry)
	}
	return rule
}

// accCheckSeams drives both review5beSeams over one resolved tariff and
// observations, asserting the full tuple. A vector therefore states its whole
// expectation once and both rating planes are held to it.
func accCheckSeams(t *testing.T, vector accVector, resolved economics.TariffSnapshot, observations []metering.Observation) {
	t.Helper()
	for _, seam := range review5beSeams() {
		expect := vector.expect
		if override, ok := vector.bySeam[seam.name]; ok {
			expect = override
		}
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			var (
				val economics.Valuation
				err error
			)
			if len(observations) == 1 {
				val, err = seam.rate(t, resolved, observations[0])
			} else {
				val, err = accRateMany(t, seam, resolved, observations)
			}
			outcome := smInspect(val)
			label := accLabel(vector, seam.name, err, outcome)
			t.Logf("vector %02d %s [%s] class=%s completeness=%q total=%s payable=%v lines=%v err=%v",
				vector.number, vector.name, seam.name, smErrorClass(err), outcome.completeness,
				outcome.total, accPayableNames(val), outcome.components, err)

			if expect.wantNoErr {
				if err != nil {
					t.Fatalf("%s: err=%v, want no diagnostic; completeness=%q total=%s lines=%v",
						label, err, outcome.completeness, outcome.total, outcome.components)
				}
			} else {
				if err == nil {
					t.Fatalf("%s: err=nil, want a typed failure; completeness=%q total=%s lines=%v",
						label, outcome.completeness, outcome.total, outcome.components)
				}
				for _, class := range expect.classes {
					if !errors.Is(err, class) {
						t.Fatalf("%s: err=%v, want errors.Is %v; completeness=%q total=%s lines=%v",
							label, err, class, outcome.completeness, outcome.total, outcome.components)
					}
				}
			}
			for _, class := range expect.notClasses {
				if errors.Is(err, class) {
					t.Fatalf("%s: err=%v must not be %v; completeness=%q total=%s lines=%v",
						label, err, class, outcome.completeness, outcome.total, outcome.components)
				}
			}
			for _, fragment := range expect.errContains {
				if err == nil || !strings.Contains(err.Error(), fragment) {
					t.Fatalf("%s: err=%v, want a diagnostic containing %q; completeness=%q total=%s",
						label, err, fragment, outcome.completeness, outcome.total)
				}
			}
			if outcome.completeness != expect.completeness {
				t.Fatalf("%s: completeness=%q, want %q; err=%v total=%s lines=%v",
					label, outcome.completeness, expect.completeness, err, outcome.total, outcome.components)
			}
			if outcome.total != expect.total {
				t.Fatalf("%s: total=%s, want %s; err=%v completeness=%q lines=%v",
					label, outcome.total, expect.total, err, outcome.completeness, outcome.components)
			}
			if expect.payable != nil {
				got := accPayableNames(val)
				want := slices.Clone(expect.payable)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Fatalf("%s: payable components=%v, want %v; err=%v completeness=%q total=%s",
						label, got, want, err, outcome.completeness, outcome.total)
				}
			}
			if expect.emittedComponents != nil {
				got := accEmittedComponentNames(val)
				want := slices.Clone(expect.emittedComponents)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Fatalf("%s: emitted components=%v, want %v; err=%v completeness=%q total=%s",
						label, got, want, err, outcome.completeness, outcome.total)
				}
			}
			if expect.noPayableComponentLines {
				if got := accPayableComponentLines(val); len(got) != 0 {
					t.Fatalf("%s: must not emit any payable quantity line, got %v; err=%v completeness=%q total=%s",
						label, got, err, outcome.completeness, outcome.total)
				}
			}
			if expect.fixedFeePayable {
				paid := false
				for i := range val.Lines {
					line := &val.Lines[i]
					if line.FixedFee != nil && line.Amount != nil {
						paid = true
					}
				}
				if !paid {
					t.Fatalf("%s: the independent call-scope fixed fee must stay payable as an explanatory line; lines=%+v err=%v",
						label, val.Lines, err)
				}
			}
		})
	}
}

// accRateMany drives a seam over several observations, which the single-
// observation review5beSeam helper cannot express. Both rating planes are
// driven over the identical input set.
func accRateMany(t *testing.T, seam review5beSeam, resolved economics.TariffSnapshot, observations []metering.Observation) (economics.Valuation, error) {
	t.Helper()
	switch seam.name {
	case "operator_E":
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		return rater.Rate(context.Background(), b1OperatorInput(t, resolved, observations))
	case "customer_policy_R":
		return billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, observations), resolved)
	default:
		t.Fatalf("unknown seam %q", seam.name)
		return economics.Valuation{}, nil
	}
}

// accPayableNames returns the sorted component names of every strictly-positive
// payable line, with "<fixed>" for a payable fee line.
func accPayableNames(val economics.Valuation) []string {
	names := review5bePayableComponentNames(val)
	slices.Sort(names)
	return names
}

// accPayableComponentLines returns the identities of every quantity line that
// carries money. It deliberately ignores fixed-fee lines: the vector-31
// contract is that a conflict suppresses quantity money while an independent
// fixed fee stays payable.
func accPayableComponentLines(val economics.Valuation) []string {
	out := make([]string, 0, len(val.Lines))
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil || line.Amount == nil {
			continue
		}
		rat, err := line.Amount.ToRat()
		if err != nil || rat.Sign() <= 0 {
			continue
		}
		out = append(out, line.Component.CanonicalKey())
	}
	slices.Sort(out)
	return out
}

// accEmittedComponentNames returns the sorted component names of every emitted
// line, payable or not, so a vector can pin WHICH components were rated at all
// (a withheld line is an absent name, which is the fail-closed signal).
func accEmittedComponentNames(val economics.Valuation) []string {
	names := make([]string, 0, len(val.Lines))
	for i := range val.Lines {
		if line := &val.Lines[i]; line.Component != nil {
			names = append(names, line.Component.Component)
		}
	}
	slices.Sort(names)
	return names
}

// accLabel is the one diagnostic prefix every assertion in this file shares, so
// a failure names the numbered vector, the seam, the class and the tuple.
func accLabel(vector accVector, seam string, err error, outcome smOutcome) string {
	return "vector " + accNumber(vector.number) + " " + vector.name + " [" + seam + "] class=" + smErrorClass(err)
}

func accNumber(number int) string {
	if number < 10 {
		return "0" + string(rune('0'+number))
	}
	return string(rune('0'+number/10)) + string(rune('0'+number%10))
}

// ---------------------------------------------------------------------------
// The 40 acceptance vectors
// ---------------------------------------------------------------------------

// TestBillingAcceptanceVectors pins the consolidated acceptance table. Each
// vector is a named subtest so a regression names one vector, and each vector
// is driven through both rating seams.
func TestBillingAcceptanceVectors(t *testing.T) {
	t.Parallel()
	for _, vector := range accVectors() {
		t.Run(accNumber(vector.number)+"_"+vector.name, func(t *testing.T) {
			t.Parallel()
			testCase := vector.build(t)
			resolved := f356Schema(t, "acceptance-"+accNumber(vector.number)+"-"+vector.name,
				testCase.rules, testCase.relationships)
			accCheckSeams(t, vector, resolved, testCase.observations)
		})
	}
}

func accVectors() []accVector {
	// Shared role vocabulary. Roles are local to each vector; the namespace
	// prefix keeps distinct vectors from ever colliding on one component
	// identity.
	var (
		// quantity/containment
		qa = accKey("q_ancestor")
		qb = accKey("q_middle")
		qc = accKey("q_leaf_a")
		qd = accKey("q_leaf_b")
		qe = accKey("q_leaf_c")
		qf = accKey("q_leaf_d")
		qg = accKey("q_sibling")
		// missingness
		ma = accKey("m_parent")
		mb = accKey("m_child_b")
		mc = accKey("m_child_c")
		// complete cover / rating
		ca = accKey("c_parent")
		cb = accKey("c_child_b")
		cc = accKey("c_child_c")
		cx = accKey("c_mid_x")
		cy = accKey("c_mid_y")
		cz = accKey("c_other_parent")
		ce = accKey("c_free_zero")
		cf = accKey("c_unpriced_share")
		// overlap
		oa = accKey("o_parent")
		ob = accKey("o_child")
		oc = accKey("o_descendant")
		od = accKey("o_redundant")
		og = accKey("o_ambiguous_parent")
		// context/tier
		ta = accKey("t_parent")
		tb = accKey("t_member_b")
		tc = accKey("t_member_c")
		td = accKey("t_absent_member")
		te = accKey("t_branch_m")
		tf = accKey("t_branch_n")
		tg = accKey("t_leaf_s1")
		th = accKey("t_leaf_s2")
		ti = accKey("t_redundant_mid_one")
		tj = accKey("t_redundant_mid_two")
		tk = accKey("t_redundant_leaf")
		// scope/identity
		sa = accKey("s_parent")
		sb = accKey("s_child")
		se = accKey("s_dim_one")
		sf = accKey("s_dim_two")
		sg = accKey("s_transform_middle")
	)

	return []accVector{
		// ---------------------------------------------------------------
		// QUANTITY / CONTAINMENT (1-10)
		// ---------------------------------------------------------------
		{
			number: 1, name: "direct_subset_ancestor_ten_descendant_twenty",
			coveredBy: "TestReview62aSubsetQuantityConsistency (subset_quantity_exceeding_parent); TestReview64aMixedContainmentChainQuantityConsistency/subset_then_partition_ten_to_twenty",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				rules := []economics.RatingRule{
					accRule(t, "acc1-ancestor", qa, "0"),
					accRule(t, "acc1-descendant", qb, "1"),
				}
				obs := accObs(t, "acc1-direct-subset", "b-leg-acc1",
					b1Measure(t, qa, "10"), b1Measure(t, qb, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "20/0",
				payable:      []string{accName("q_middle")},
			},
		},
		{
			number: 2, name: "pure_subset_chain_absent_middle",
			coveredBy: "TestReview63cTransitiveSubsetQuantityConsistency (chain_a=10, chain_c=20, chain_b absent); reviewf356_settlement_test.go/transitive_subset_quantity_exceeding_ancestor_cannot_settle",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipSubset, qb, qc)
				rules := []economics.RatingRule{
					accRule(t, "acc2-ancestor", qa, "0"),
					accRule(t, "acc2-descendant", qc, "1"),
				}
				obs := accObs(t, "acc2-subset-chain", "b-leg-acc2",
					b1Measure(t, qa, "10"), b1Measure(t, qc, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "20/0",
				payable:      []string{accName("q_leaf_a")},
			},
		},
		{
			number: 3, name: "mixed_subset_to_partition_absent_middle",
			coveredBy: "TestReview64aMixedContainmentChainQuantityConsistency/subset_then_partition_ten_to_twenty",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				rules := []economics.RatingRule{
					accRule(t, "acc3-ancestor", qa, "0"),
					accRule(t, "acc3-descendant", qc, "1"),
				}
				obs := accObs(t, "acc3-mixed-forward", "b-leg-acc3",
					b1Measure(t, qa, "10"), b1Measure(t, qc, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "20/0",
				payable:      []string{accName("q_leaf_a")},
			},
		},
		{
			number: 4, name: "mixed_partition_to_subset",
			coveredBy: "TestReview64aMixedContainmentChainQuantityConsistency/aggregate_then_partition_ten_to_twenty (the complete-coverage diagnosis owns this shape class)",
			build: func(t *testing.T) accCase {
				t.Helper()
				// A declares COMPLETE coverage of B, so the cover proof decides
				// this shape before the containment walk. B's own edge to C is a
				// SUBSET edge, which asserts containment but NOT conservation,
				// so B has no complete cover and cannot be represented at all.
				// The required member B is therefore genuinely missing and the
				// verdict is partition-INCOMPLETE, not a contradiction. (The
				// mirror shape where B's edge IS a partition edge, so B does
				// resolve, is the 64a aggregate_then_partition vector.)
				relationships := accPartition(qa, -1, qb)
				relationships = accEdge(relationships, metering.RelationshipSubset, qb, qc)
				rules := []economics.RatingRule{
					accRule(t, "acc4-ancestor", qa, "0"),
					accRule(t, "acc4-descendant", qc, "1"),
				}
				obs := accObs(t, "acc4-mixed-reverse", "b-leg-acc4",
					b1Measure(t, qa, "10"), b1Measure(t, qc, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaPartitionIncomplete},
				// The cover diagnosis owns this shape, so the containment walk
				// must stay silent instead of adding a duplicate diagnosis.
				notClasses:   []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "20/0",
				payable:      []string{accName("q_leaf_a")},
			},
		},
		{
			number: 5, name: "complete_cover_leaves_each_within_but_sum_exceeds_ancestor",
			coveredBy: "TestReview65CompleteSumExceedingAncestorIsRejected",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qd)
				rules := []economics.RatingRule{
					accRule(t, "acc5-ancestor", qa, "0"),
					accRule(t, "acc5-leaf-a", qc, "1"),
					accRule(t, "acc5-leaf-b", qd, "1"),
				}
				obs := accObs(t, "acc5-cover-sum", "b-leg-acc5",
					b1Measure(t, qa, "30"), b1Measure(t, qc, "20"), b1Measure(t, qd, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "40/0",
				payable:      []string{accName("q_leaf_a"), accName("q_leaf_b")},
			},
		},
		{
			number: 6, name: "cover_sum_lower_bound_exceeds_ancestor_with_further_required_leaf_absent",
			coveredBy: "TestReview65KnownLowerBoundExceedingAncestorIsRejected (that suite pins only not-complete plus a non-nil diagnostic; the typed class is pinned here for the first time)",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qd)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qe)
				rules := []economics.RatingRule{
					accRule(t, "acc6-ancestor", qa, "0"),
					accRule(t, "acc6-leaf-a", qc, "1"),
					accRule(t, "acc6-leaf-b", qd, "1"),
					accRule(t, "acc6-leaf-c", qe, "1"),
				}
				obs := accObs(t, "acc6-cover-lower-bound", "b-leg-acc6",
					b1Measure(t, qa, "30"), b1Measure(t, qc, "20"), b1Measure(t, qd, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				// MEASURED TRUTH, recorded deliberately: the absent third
				// REQUIRED leaf makes B's own complete cover unprovable, and the
				// rater reports that MISSING-MEMBER diagnosis instead of the
				// sum-bound contradiction. Both fail closed and neither leaks
				// money, but the typed class is partition_incomplete rather than
				// a quantity contradiction, so the lower-bound violation is not
				// separately reported. See the report note on vector 6.
				classes: []error{billing.ErrSchemaPartitionIncomplete},
				notClasses: []error{
					billing.ErrSchemaSubsetContradiction, billing.ErrSchemaPartitionContradiction,
				},
				completeness: economics.CompletenessPartial,
				total:        "40/0",
				payable:      []string{accName("q_leaf_a"), accName("q_leaf_b")},
			},
		},
		{
			number: 7, name: "nested_complete_sums_exceed_ancestor",
			coveredBy: "TestMetamorphicCompleteCoverExpansionSumViolationIsCaught (the metamorphic form of the same sum bound)",
			build: func(t *testing.T) accCase {
				t.Helper()
				// A contains B; B covers {C, D}; C covers {E, F}; D covers {G, X}.
				// Every intermediate aggregate is absent, so the only
				// comparable endpoints are the ancestor A and the leaves, whose
				// nested complete sums are 50 against an ancestor of 30.
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qd)
				relationships = accEdge(relationships, metering.RelationshipPartition, qc, qe)
				relationships = accEdge(relationships, metering.RelationshipPartition, qc, qf)
				relationships = accEdge(relationships, metering.RelationshipPartition, qd, qg)
				relationships = accEdge(relationships, metering.RelationshipPartition, qd, accKey("q_leaf_e"))
				rules := []economics.RatingRule{
					accRule(t, "acc7-ancestor", qa, "0"),
					accRule(t, "acc7-leaf-a", qe, "1"),
					accRule(t, "acc7-leaf-b", qf, "1"),
					accRule(t, "acc7-leaf-c", qg, "1"),
					accRule(t, "acc7-leaf-d", accKey("q_leaf_e"), "1"),
				}
				obs := accObs(t, "acc7-nested-sum", "b-leg-acc7",
					b1Measure(t, qa, "30"),
					b1Measure(t, qe, "10"), b1Measure(t, qf, "20"),
					b1Measure(t, qg, "30"), b1Measure(t, accKey("q_leaf_e"), "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "100/0",
				payable: []string{
					accName("q_leaf_c"), accName("q_leaf_d"), accName("q_leaf_e"), accName("q_sibling"),
				},
			},
		},
		{
			number: 8, name: "negative_control_exact_boundary_equality_is_valid",
			coveredBy: "TestReview64aMixedContainmentChainQuantityConsistency/equal_quantities_valid; TestF3ConservationEqualPartitionRatesComplete (conserved 100 = 60 + 40)",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qd)
				rules := []economics.RatingRule{
					accRule(t, "acc8-ancestor", qa, "0"),
					accRule(t, "acc8-leaf-a", qc, "1"),
					accRule(t, "acc8-leaf-b", qd, "1"),
				}
				obs := accObs(t, "acc8-boundary-equal", "b-leg-acc8",
					b1Measure(t, qa, "40"), b1Measure(t, qc, "20"), b1Measure(t, qd, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "40/0",
				payable:      []string{accName("q_leaf_a"), accName("q_leaf_b")},
			},
		},
		{
			number: 9, name: "negative_control_strict_containment_is_valid",
			coveredBy: "TestReview64aMixedContainmentChainQuantityConsistency/subset_strictly_inside_valid",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qb)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qc)
				relationships = accEdge(relationships, metering.RelationshipPartition, qb, qd)
				rules := []economics.RatingRule{
					accRule(t, "acc9-ancestor", qa, "0"),
					accRule(t, "acc9-leaf-a", qc, "1"),
					accRule(t, "acc9-leaf-b", qd, "1"),
				}
				obs := accObs(t, "acc9-strict-inside", "b-leg-acc9",
					b1Measure(t, qa, "100"), b1Measure(t, qc, "20"), b1Measure(t, qd, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "40/0",
				payable:      []string{accName("q_leaf_a"), accName("q_leaf_b")},
			},
		},
		{
			number: 10, name: "negative_effective_selected_quantity_can_never_reach_complete",
			coveredBy: "no focused suite exercises this vector; it is the sole pin of the only reachable form of ErrSchemaQuantityContradiction",
			build: func(t *testing.T) accCase {
				t.Helper()
				// A reported component quantity below zero makes the effective
				// reduced selected quantity negative. The member is
				// commercially SUPPRESSED (its rule resolves to an explicit-free
				// zero), which is the strong form: the money is already zero, so
				// nothing but a negative-quantity verdict could keep the
				// valuation from certifying complete. The parent is absent, so
				// no other conservation or missing-member diagnosis competes.
				relationships := accPartition(qa, -1, qb)
				rules := []economics.RatingRule{accRule(t, "acc10-suppressed", qb, "0")}
				obs := accObs(t, "acc10-negative-suppressed", "b-leg-acc10",
					b1Measure(t, qb, "-20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			// MEASURED TRUTH, recorded deliberately and NOT what the requirement
			// assumed. ErrSchemaQuantityContradiction is unreachable through the
			// production rating seam: pkg/lipsdk/metering rejects a negative
			// Measure value at observation validation ("negative value requires
			// correction/replacement semantics") because the SDK exposes no
			// component-level credit marker. The effective selected quantity can
			// therefore never go negative, the class is defence in depth against
			// a future SDK gap, and the vector that DOES pin the requirement is
			// the input-validation fence below: the shape can never certify
			// Complete money. See the report note on vector 10.
			expect: accExpect{
				// The sentinel is flattened to text by an intermediate %v wrap
				// in the economics input validator, so the diagnostic fragment is
				// what is matchable here.
				errContains:  []string{"invalid observation", "negative value requires correction/replacement semantics"},
				notClasses:   accSchemaSentinels,
				completeness: "",
				total:        "<none>",
			},
		},

		// ---------------------------------------------------------------
		// MISSINGNESS (11-16)
		// ---------------------------------------------------------------
		{
			number: 11, name: "observed_non_payable_parent_one_paid_child_required_sibling_absent",
			coveredBy: "TestReviewF356MissingRequiredPartitionMemberIsNotComplete/primary_informational_parent_missing_required_c; reviewf356_settlement_test.go/missing_required_partition_member_cannot_settle",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ma, -1, mb, mc)
				rules := []economics.RatingRule{accRule(t, "acc11-child-b", mb, "1")}
				obs := accObs(t, "acc11-missing-required", "b-leg-acc11",
					b1Measure(t, ma, "100"), b1Measure(t, mb, "60"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				payable:      []string{accName("m_child_b")},
			},
		},
		{
			number: 12, name: "unobserved_active_parent_required_absent_sibling_has_non_free_rate",
			coveredBy: "TestReviewF356TaintedAbsentParentFailsClosed (the unobserved-parent fail-closed family); reviewf356_settlement_test.go/unobserved_parent_unplaceable_subset_cannot_settle",
			build: func(t *testing.T) accCase {
				t.Helper()
				// The parent was never observed, so the cover has to be decided
				// from the members alone. The absent REQUIRED member C carries a
				// NON-FREE rate, so it is a real commercial dependency: it could
				// have billed money, and its absence is missing evidence rather
				// than a declaration that nothing happened.
				relationships := accPartition(ma, -1, mb, mc)
				rules := []economics.RatingRule{
					accRule(t, "acc12-child-b", mb, "1"),
					accRule(t, "acc12-absent-nonfree", mc, "2"),
				}
				obs := accObs(t, "acc12-unobserved-nonfree", "b-leg-acc12",
					b1Measure(t, mb, "60"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				payable:      []string{accName("m_child_b")},
			},
		},
		{
			number: 13, name: "negative_control_same_shape_with_explicit_free_absent_sibling_is_valid",
			coveredBy: "TestSchemaModelRegressionExplicitFreeMissingMemberIsNotADependency; TestReviewF356MissingRequiredPartitionMemberIsNotComplete/control_absent_optional_c_declares_zero",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Exactly vector 12, except the absent REQUIRED member C carries
				// an EXPLICIT-FREE rule and is irrelevant to this context and to
				// the overlap graph. This is the ratified non-free-only boundary:
				// the commercial predicate asks whether money was owed, not
				// whether a rule object exists, so a free member is not a
				// dependency and the cover is not failed closed.
				relationships := accPartition(ma, -1, mb, mc)
				rules := []economics.RatingRule{
					accRule(t, "acc13-child-b", mb, "1"),
					accRule(t, "acc13-absent-free", mc, "0"),
				}
				obs := accObs(t, "acc13-unobserved-free", "b-leg-acc13",
					b1Measure(t, mb, "60"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "60/0",
				payable:      []string{accName("m_child_b")},
			},
		},
		{
			number: 14, name: "negative_control_all_parent_and_children_absent_is_inactive",
			coveredBy: "TestReview583ZeroSharePartitionCover/control_absent_required_b_no_synthetic_cover; TestMetamorphicIrrelevantExtensionInvariance",
			build: func(t *testing.T) accCase {
				t.Helper()
				// An all-absent REQUIRED cover proves nothing and must raise no
				// diagnosis of its own. The unrelated priced component in the
				// same scope settles, which is the whole assertion: the absent
				// fragment neither fails the valuation nor invents a zero share
				// that would contradict the present money.
				relationships := accPartition(ma, -1, mb, mc)
				rules := []economics.RatingRule{accRule(t, "acc14-unrelated", accKey("m_unrelated"), "1")}
				obs := accObs(t, "acc14-all-absent", "b-leg-acc14",
					b1Measure(t, accKey("m_unrelated"), "10"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "10/0",
				payable:      []string{accName("m_unrelated")},
			},
		},
		{
			number: 15, name: "negative_control_all_optional_absent_never_becomes_an_observed_zero",
			coveredBy: "TestSchemaModelRegressionOptionalCompleteMemberIsNotZero; TestMetamorphicOptionalAbsentIsNotExactZero; TestN2ConservedZeroChildWithoutRuleStaysComplete/absent_optional_child",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Both declared members are OPTIONAL and both are absent. The
				// frozen schema declares its own edge-local zero for an absent
				// optional member; what it must never do is turn that
				// declaration into a global observed zero that then contradicts
				// unrelated present money.
				relationships := accPartition(ma, 0, mb, mc)
				rules := []economics.RatingRule{accRule(t, "acc15-unrelated", accKey("m_unrelated"), "1")}
				obs := accObs(t, "acc15-all-optional-absent", "b-leg-acc15",
					b1Measure(t, accKey("m_unrelated"), "10"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "10/0",
				payable:      []string{accName("m_unrelated")},
			},
		},
		{
			number: 16, name: "present_unavailable_required_child_is_partial",
			coveredBy: "TestReviewF356MissingRequiredPartitionMemberIsNotComplete/control_unavailable_required_c; TestN2UnavailableChildIsNotArithmeticContradiction",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ma, -1, mb, mc)
				rules := []economics.RatingRule{accRule(t, "acc16-child-b", mb, "1")}
				obs := accObs(t, "acc16-unavailable-required", "b-leg-acc16",
					b1Measure(t, ma, "100"), b1Measure(t, mb, "60"), b1UnavailableMeasure(mc))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				payable:      []string{accName("m_child_b")},
			},
		},

		// ---------------------------------------------------------------
		// COMPLETE COVER / RATING (17-22)
		// ---------------------------------------------------------------
		{
			number: 17, name: "negative_control_child_only_conserved_complete_cover",
			coveredBy: "TestF3ConservationEqualPartitionRatesComplete; TestN2ConservedZeroChildWithoutRuleStaysComplete/all_required_children_priced_stays_complete",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ca, -1, cb, cc)
				rules := []economics.RatingRule{
					accRule(t, "acc17-child-b", cb, "2"),
					accRule(t, "acc17-child-c", cc, "3"),
				}
				obs := accObs(t, "acc17-child-only-cover", "b-leg-acc17",
					b1Measure(t, ca, "100"), b1Measure(t, cb, "60"), b1Measure(t, cc, "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "240/0",
				payable:      []string{accName("c_child_b"), accName("c_child_c")},
			},
		},
		{
			number: 18, name: "negative_control_nested_child_only_conserved_cover",
			coveredBy: "TestReviewF356RecursiveCoverPropagatesToCompleteness/primary_nested_child_only_conserved_complete_100; reviewf356_settlement_test.go/nested_conserved_cover_settles_once",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ca, -1, cb, cc)
				relationships = accEdge(relationships, metering.RelationshipPartition, cb, cx)
				relationships = accEdge(relationships, metering.RelationshipPartition, cb, cy)
				rules := []economics.RatingRule{
					accRule(t, "acc18-x", cx, "1"),
					accRule(t, "acc18-y", cy, "1"),
					accRule(t, "acc18-c", cc, "1"),
				}
				obs := accObs(t, "acc18-nested-cover", "b-leg-acc18",
					b1Measure(t, ca, "100"), b1Measure(t, cb, "60"),
					b1Measure(t, cx, "30"), b1Measure(t, cy, "30"), b1Measure(t, cc, "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "100/0",
				payable:      []string{accName("c_child_c"), accName("c_mid_x"), accName("c_mid_y")},
			},
		},
		{
			number: 19, name: "complete_cover_contradiction",
			coveredBy: "TestReview5beConservationBeforePricingExclusions/primary_contradicted_partition_100_vs_60_plus_60; TestF3ConservationOverflowIsNotComplete",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ca, -1, cb, cc)
				rules := []economics.RatingRule{
					accRule(t, "acc19-child-b", cb, "1"),
					accRule(t, "acc19-child-c", cc, "1"),
				}
				obs := accObs(t, "acc19-cover-contradiction", "b-leg-acc19",
					b1Measure(t, ca, "100"), b1Measure(t, cb, "60"), b1Measure(t, cc, "60"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionContradiction},
				completeness: economics.CompletenessPartial,
				total:        "120/0",
				payable:      []string{accName("c_child_b"), accName("c_child_c")},
			},
		},
		{
			number: 20, name: "complete_cover_ambiguous_shared_child",
			coveredBy: "TestReviewF356TaintedAbsentParentFailsClosed/control_observed_tainted_parent_stays_incomparable; TestN2UnavailableChildIsNotArithmeticContradiction (the incomparable-vs-contradiction distinction)",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Child B is declared under two complete parents, so the true
				// owner cannot be chosen. That is a POSITIVE claim about the
				// evidence, not an arithmetic mismatch, which is why the class
				// is incomparable and not contradiction.
				relationships := accPartition(ca, -1, cb, cc)
				relationships = accEdge(relationships, metering.RelationshipPartition, cz, cb)
				rules := []economics.RatingRule{
					accRule(t, "acc20-child-b", cb, "1"),
					accRule(t, "acc20-child-c", cc, "1"),
				}
				obs := accObs(t, "acc20-cover-ambiguous", "b-leg-acc20",
					b1Measure(t, ca, "100"), b1Measure(t, cb, "60"), b1Measure(t, cc, "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomparable},
				completeness: economics.CompletenessPartial,
				total:        "100/0",
				payable:      []string{accName("c_child_b"), accName("c_child_c")},
			},
		},
		{
			number: 21, name: "zero_unpriced_share_accounts_for_cover_without_money",
			coveredBy: "TestN2ConservedZeroChildWithoutRuleStaysComplete/required_unpriced_zero_child_stays_partial_not_contradiction; TestReview583ZeroSharePartitionCover",
			build: func(t *testing.T) accCase {
				t.Helper()
				// The cover arithmetic is 60 = 60 + 0, so the present zero share
				// makes the equation provable rather than wrong. It carries no
				// rule, so it contributes no money, and because an unpriced
				// required member is not billable-covered the PARENT keeps its
				// own missing-rate diagnostic. The vector therefore pins the
				// important half of the requirement: the zero share is neither
				// a partition contradiction nor a silent extra charge, and the
				// only money is the sibling's.
				relationships := accPartition(ca, -1, cb, ce)
				rules := []economics.RatingRule{accRule(t, "acc21-child-b", cb, "1")}
				obs := accObs(t, "acc21-zero-unpriced-share", "b-leg-acc21",
					b1Measure(t, ca, "60"), b1Measure(t, cb, "60"), b1Measure(t, ce, "0"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				// A missing operand, not a wrong one: no schema failure class
				// may be reported for conserved arithmetic.
				notClasses:   accSchemaSentinels,
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				payable:      []string{accName("c_child_b")},
			},
		},
		{
			number: 22, name: "positive_unpriced_share_without_recursive_cover_leaves_cover_unresolved",
			coveredBy: "TestReviewF356TaintedAbsentParentFailsClosed (the unprovable-cover fail-closed family); TestN2ConservedZeroChildWithoutRuleStaysComplete/required_unpriced_zero_child_stays_partial_not_contradiction",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Share C is present and positive but carries no rule and no
				// cover of its own, so the parent's coverage cannot be computed:
				// a non-zero share of a complete partition is never silently
				// dropped. The cover stays unresolved, the parent keeps its own
				// billing diagnostic, and the child-only money never looks
				// complete. This is NOT a contradiction: the arithmetic is not
				// wrong, it is unavailable.
				relationships := accPartition(ca, -1, cb, cf)
				rules := []economics.RatingRule{accRule(t, "acc22-child-b", cb, "1")}
				obs := accObs(t, "acc22-unresolved-share", "b-leg-acc22",
					b1Measure(t, ca, "100"), b1Measure(t, cb, "60"), b1Measure(t, cf, "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				// Same clause as vector 21, at a positive unpriced share: the
				// verdict is the parent's own missing rate, never a fabricated
				// contradiction and never a complete settlement.
				notClasses:   accSchemaSentinels,
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				payable:      []string{accName("c_child_b")},
			},
		},

		// ---------------------------------------------------------------
		// OVERLAP (23-31)
		// ---------------------------------------------------------------
		{
			number: 23, name: "positive_parent_and_positive_descendant",
			coveredBy: "TestB1FrozenSchemaOverlapRejectedThroughCatalogRaterAndRetail (the primary RED vector)",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, oa, ob)
				rules := []economics.RatingRule{
					accRule(t, "acc23-parent", oa, "1"),
					accRule(t, "acc23-child", ob, "2"),
				}
				obs := accObs(t, "acc23-direct-overlap", "b-leg-acc23",
					b1Measure(t, oa, "10"), b1Measure(t, ob, "4"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaOverlapConflict},
				// The conflict suppresses the quantity lines entirely, so the
				// valuation carries no currency total at all.
				completeness:            economics.CompletenessConflict,
				total:                   "<none>",
				noPayableComponentLines: true,
			},
		},
		{
			number: 24, name: "transitive_parent_descendant_through_absent_middle",
			coveredBy: "TestN1TransitiveSubsetOverlap (frozen_schema_subset_partition_transitive_overlap_n1_test.go); TestReview63cTransitiveSubsetQuantityConsistency",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, oa, ob)
				relationships = accEdge(relationships, metering.RelationshipSubset, ob, oc)
				rules := []economics.RatingRule{
					accRule(t, "acc24-ancestor", oa, "1"),
					accRule(t, "acc24-descendant", oc, "1"),
				}
				obs := accObs(t, "acc24-transitive-overlap", "b-leg-acc24",
					b1Measure(t, oa, "10"), b1Measure(t, oc, "5"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaOverlapConflict},
				// The conflict suppresses the quantity lines entirely, so the
				// valuation carries no currency total at all.
				completeness:            economics.CompletenessConflict,
				total:                   "<none>",
				noPayableComponentLines: true,
			},
		},
		{
			number: 25, name: "proven_complete_cover_plus_positive_unallocated_subset",
			coveredBy: "TestReview5beRecursivePaidCoverMustNotDoubleCharge/primary_recursive_cover_plus_priced_subset and primary_absent_b_recursive_cover_plus_priced_subset",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(oa, -1, ob, accKey("o_child_c"))
				relationships = accEdge(relationships, metering.RelationshipPartition, ob, accKey("o_mid_x"))
				relationships = accEdge(relationships, metering.RelationshipPartition, ob, accKey("o_mid_y"))
				relationships = accEdge(relationships, metering.RelationshipSubset, oa, od)
				rules := []economics.RatingRule{
					accRule(t, "acc25-x", accKey("o_mid_x"), "1"),
					accRule(t, "acc25-y", accKey("o_mid_y"), "1"),
					accRule(t, "acc25-c", accKey("o_child_c"), "1"),
					accRule(t, "acc25-subset", od, "1"),
				}
				obs := accObs(t, "acc25-proven-cover-overlap", "b-leg-acc25",
					b1Measure(t, ob, "60"),
					b1Measure(t, accKey("o_mid_x"), "30"), b1Measure(t, accKey("o_mid_y"), "30"),
					b1Measure(t, accKey("o_child_c"), "40"),
					b1Measure(t, od, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaOverlapConflict},
				// The conflict suppresses the quantity lines entirely, so the
				// valuation carries no currency total at all.
				completeness:            economics.CompletenessConflict,
				total:                   "<none>",
				noPayableComponentLines: true,
			},
		},
		{
			number: 26, name: "unresolved_cover_with_money_on_cover_side_and_money_on_subset_side",
			coveredBy: "TestReview62aUnresolvedCoverWithPayableSubsetFailsClosed and its control_cover_still_unresolved_keeps_fail_closed",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(oa, -1, ob, accKey("o_child_c"))
				relationships = accEdge(relationships, metering.RelationshipSubset, oa, od)
				rules := []economics.RatingRule{
					accRule(t, "acc26-cover-side", ob, "1"),
					accRule(t, "acc26-subset-side", od, "1"),
				}
				obs := accObs(t, "acc26-unresolved-cover", "b-leg-acc26",
					b1Measure(t, ob, "60"), b1Measure(t, od, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaPartitionIncomplete},
				// The unplaceable subset descendant is WITHHELD, not billed: the
				// system cannot prove the two paid lines are disjoint, so the
				// only surviving money is the cover-side line and the
				// valuation never looks complete.
				completeness:      economics.CompletenessPartial,
				total:             "60/0",
				payable:           []string{accName("o_child")},
				emittedComponents: []string{accName("o_child")},
			},
		},
		{
			number: 27, name: "ambiguous_cover_plus_payable_subset",
			coveredBy: "TestReviewF356TaintedAbsentParentFailsClosed/primary_tainted_absent_parent_plus_priced_subset; reviewf356_settlement_test.go/tainted_absent_parent_cannot_settle",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(oa, -1, ob, accKey("o_child_c"))
				relationships = accEdge(relationships, metering.RelationshipPartition, og, ob)
				relationships = accEdge(relationships, metering.RelationshipSubset, oa, od)
				rules := []economics.RatingRule{
					accRule(t, "acc27-b", ob, "1"),
					accRule(t, "acc27-c", accKey("o_child_c"), "1"),
					accRule(t, "acc27-subset", od, "1"),
				}
				obs := accObs(t, "acc27-ambiguous-cover", "b-leg-acc27",
					b1Measure(t, ob, "60"), b1Measure(t, accKey("o_child_c"), "40"),
					b1Measure(t, od, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes: []error{billing.ErrSchemaPartitionIncomparable},
				// Same contract as vector 26 under an ambiguous cover: the
				// ambiguous cover is incomparable, not incomplete and not a
				// contradiction, and the unplaceable subset line is withheld.
				notClasses:        []error{billing.ErrSchemaPartitionIncomplete, billing.ErrSchemaPartitionContradiction},
				completeness:      economics.CompletenessPartial,
				total:             "100/0",
				payable:           []string{accName("o_child"), accName("o_child_c")},
				emittedComponents: []string{accName("o_child"), accName("o_child_c")},
			},
		},
		{
			number: 28, name: "negative_control_redundant_path_to_the_same_contributor_is_not_a_self_conflict",
			coveredBy: "TestReview5beRedundantSubsetPathDoesNotDuplicateACharge; TestReview5beRedundantSubsetWithExtraPricedSubsetStillRejected (the boundary)",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(oa, -1, ob, accKey("o_child_c"))
				relationships = accEdge(relationships, metering.RelationshipSubset, oa, od)
				relationships = accEdge(relationships, metering.RelationshipSubset, od, ob)
				rules := []economics.RatingRule{
					accRule(t, "acc28-b", ob, "1"),
					accRule(t, "acc28-c", accKey("o_child_c"), "1"),
				}
				obs := accObs(t, "acc28-redundant-path", "b-leg-acc28",
					b1Measure(t, oa, "100"), b1Measure(t, ob, "60"), b1Measure(t, accKey("o_child_c"), "40"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "100/0",
				payable:      []string{accName("o_child"), accName("o_child_c")},
			},
		},
		{
			number: 29, name: "two_positive_subset_siblings_with_unknown_intersection",
			coveredBy: "TestMetamorphicStructuralVerdictAgreesWithModel (the ratified non-blocking unknown-intersection decision); no focused suite pins the additive-settling form, so the observed truth is recorded here",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Two ordinary subset siblings of the same parent, both
				// positive, with an unknown intersection. By ratified decision 8
				// an unknown containment intersection is NON-BLOCKING, so with no
				// other failing side this graph settles complete and additively.
				// That is MEASURED, not assumed, and it is pinned deliberately:
				// the requirement asked for a non-complete verdict, so this
				// vector records the discrepancy rather than hiding it. The
				// non-blocking decision is the reason; no complete-coverage or
				// sum side exists in this graph to fail independently.
				relationships := accEdge(nil, metering.RelationshipSubset, qa, qe)
				relationships = accEdge(relationships, metering.RelationshipSubset, qa, qf)
				rules := []economics.RatingRule{
					accRule(t, "acc29-ancestor-free", qa, "0"),
					accRule(t, "acc29-sibling-one", qe, "1"),
					accRule(t, "acc29-sibling-two", qf, "1"),
				}
				obs := accObs(t, "acc29-unknown-intersection", "b-leg-acc29",
					b1Measure(t, qa, "100"), b1Measure(t, qe, "20"), b1Measure(t, qf, "30"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "50/0",
				payable:      []string{accName("q_leaf_c"), accName("q_leaf_d")},
			},
		},
		{
			number: 30, name: "negative_control_subset_descendants_under_distinct_validated_branches_are_additive",
			coveredBy: "TestReview583AmbiguityLocality/independent_cover_unaffected_by_unrelated_ambiguity (independence of validated covers); no focused suite pins two subsets under distinct branches",
			build: func(t *testing.T) accCase {
				t.Helper()
				// The root's complete cover is validated by its two members, and
				// each member carries its own subset leaf. Every ancestor is
				// priced at an EXPLICIT-FREE zero, so no ancestor contributes
				// money and no ancestor is a paid contributor that a leaf could
				// overlap. The two leaves therefore live under DISTINCT
				// validated branches, no single cover can absorb both, and the
				// money is additive.
				relationships := accPartition(oa, -1, te, tf)
				relationships = accEdge(relationships, metering.RelationshipSubset, te, tg)
				relationships = accEdge(relationships, metering.RelationshipSubset, tf, th)
				rules := []economics.RatingRule{
					accRule(t, "acc30-root-free", oa, "0"),
					accRule(t, "acc30-branch-m-free", te, "0"),
					accRule(t, "acc30-branch-n-free", tf, "0"),
					accRule(t, "acc30-leaf-one", tg, "1"),
					accRule(t, "acc30-leaf-two", th, "1"),
				}
				obs := accObs(t, "acc30-distinct-branches", "b-leg-acc30",
					b1Measure(t, oa, "40"), b1Measure(t, te, "20"), b1Measure(t, tf, "20"),
					b1Measure(t, tg, "5"), b1Measure(t, th, "7"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "12/0",
				payable:      []string{accName("t_leaf_s1"), accName("t_leaf_s2")},
			},
		},
		{
			number: 31, name: "fixed_fee_plus_component_conflict_cannot_settle",
			coveredBy: "TestB1FrozenSchemaOverlapFixedFeeSurvivesConflict (the identical shape and contract)",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, oa, ob)
				rules := []economics.RatingRule{
					accRule(t, "acc31-parent", oa, "1"),
					accRule(t, "acc31-child", ob, "2"),
					b1FixedRule(t, "acc31-call-fee", economics.FixedFeeScopeCall, "4"),
				}
				obs := accObs(t, "acc31-fixed-fee-conflict", "b-leg-acc31",
					b1Measure(t, oa, "10"), b1Measure(t, ob, "4"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaOverlapConflict},
				completeness: economics.CompletenessConflict,
				// The fixed line stays explanatory; the quantity money does not.
				// The total is the operator-plane measurement, pinned per seam
				// below because the two planes legitimately differ here.
				total:                   "4/0",
				noPayableComponentLines: true,
				fixedFeePayable:         true,
			},
			// The two rating planes legitimately differ on the explanatory fixed
			// line, and pinning both is the point: the operator plane keeps the
			// independent call-scope fee, while the customer-policy plane, which
			// selects fees from the customer policy rather than the tariff,
			// emits no fee line. The frozen-schema classification and the
			// suppression of quantity money are identical on both.
			bySeam: map[string]accExpect{
				"customer_policy_R": {
					classes:                 []error{billing.ErrSchemaOverlapConflict},
					completeness:            economics.CompletenessConflict,
					total:                   "<none>",
					noPayableComponentLines: true,
				},
			},
		},

		// ---------------------------------------------------------------
		// CONTEXT / TIER (32-35)
		// ---------------------------------------------------------------
		{
			number: 32, name: "whole_context_rule_with_missing_required_schema_component",
			coveredBy: "TestSchemaModelRegressionWholeContextRuleOnMissingMemberIsADependency",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accPartition(ta, -1, tb)
				relationships = accEdge(relationships, metering.RelationshipSubset, ta, tc)
				contextRule := accRule(t, "acc32-absent-context", tb, "3")
				contextRule.SelectionScope = economics.SelectionWholeContext
				rules := []economics.RatingRule{
					accRule(t, "acc32-parent", ta, "1"),
					contextRule,
					accRule(t, "acc32-subset", tc, "1"),
				}
				obs := accObs(t, "acc32-whole-context-missing", "b-leg-acc32",
					b1Measure(t, tc, "1"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "1/0",
				payable:      []string{accName("t_member_c")},
			},
		},
		{
			number: 33, name: "negative_control_absent_optional_member_invents_no_context",
			coveredBy: "TestSchemaModelRegressionOptionalCompleteMemberIsNotZero; TestReviewF356MissingRequiredPartitionMemberIsNotComplete/control_absent_optional_c_declares_zero",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Member C is declared OPTIONAL and is absent, and it carries a
				// whole-context positive rate precisely so that, had it been
				// REQUIRED, the vector-32 dependency would fire. The conserved
				// cover 30 = 30 + 0 therefore excuses the unpriced parent, and
				// the tier boundary at 80 separates the honest context (30 + 30
				// = 60, low tier) from any context that invented a share for
				// the absent member (90, high tier). The emitted amount pins
				// that no share was invented.
				relationships := accPartition(ta, 1, tb, td)
				optionalRule := accRule(t, "acc33-absent-whole-context", td, "3")
				optionalRule.SelectionScope = economics.SelectionWholeContext
				rules := []economics.RatingRule{
					accTiered(t, "acc33-present-member", tb, "80", "1", "3"),
					optionalRule,
				}
				obs := accObs(t, "acc33-no-invented-context", "b-leg-acc33",
					b1Measure(t, ta, "30"), b1Measure(t, tb, "30"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "30/0",
				payable:      []string{accName("t_member_b")},
			},
		},
		{
			number: 34, name: "negative_control_covered_parent_removed_from_context_exactly_once",
			coveredBy: "no focused suite pins the whole-context covered-parent removal; the contextTotal exclusions are pinned only here",
			build: func(t *testing.T) accCase {
				t.Helper()
				// The parent is covered by its children, so it must be dropped
				// from the whole-context total exactly once: the honest context
				// is 12 + 8 = 20. The three tier boundaries discriminate all
				// three behaviours at once - retaining the parent gives 40 and
				// the high tier, removing it twice gives 0 and the top tier, and
				// removing it once gives 20 and the middle tier at price 1. The
				// emitted amount is therefore a unique witness.
				relationships := accPartition(ta, -1, tb, tc)
				rules := []economics.RatingRule{
					// Three boundaries, so all three behaviours are
					// distinguishable from the emitted amount alone.
					accTierLadder(t, "acc34-member-b", tb,
						[2]string{"0", "10"}, [2]string{"30", "1"}, [2]string{"", "4"}),
					accRule(t, "acc34-member-c", tc, "1"),
				}
				obs := accObs(t, "acc34-context-removal", "b-leg-acc34",
					b1Measure(t, ta, "20"), b1Measure(t, tb, "12"), b1Measure(t, tc, "8"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "20/0",
				payable:      []string{accName("t_member_b"), accName("t_member_c")},
			},
		},
		{
			number: 35, name: "negative_control_redundant_paths_do_not_multiply_the_context_quantity",
			coveredBy: "TestReview5beRedundantSubsetPathDoesNotDuplicateACharge (redundant paths do not duplicate a charge); no focused suite pins the whole-context quantity",
			build: func(t *testing.T) accCase {
				t.Helper()
				// The leaf is reachable from the parent along two independent
				// redundant subset paths. The whole-context total must count its
				// quantity once, so the honest context is 20 and the middle tier
				// applies; a doubled context of 40 would cross the boundary into
				// the high tier and quadruple the charge.
				relationships := accEdge(nil, metering.RelationshipSubset, ta, ti)
				relationships = accEdge(relationships, metering.RelationshipSubset, ta, tj)
				relationships = accEdge(relationships, metering.RelationshipSubset, ti, tk)
				relationships = accEdge(relationships, metering.RelationshipSubset, tj, tk)
				rules := []economics.RatingRule{accTiered(t, "acc35-leaf", tk, "30", "1", "4")}
				obs := accObs(t, "acc35-redundant-context", "b-leg-acc35",
					b1Measure(t, tk, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "20/0",
				payable:      []string{accName("t_redundant_leaf")},
			},
		},

		// ---------------------------------------------------------------
		// SCOPE / IDENTITY (36-40)
		// ---------------------------------------------------------------
		{
			number: 36, name: "same_graph_in_two_b_leg_scopes_is_independent",
			coveredBy: "TestReview62aCrossScopeAndDirectionSubsetControls/cross_scope_stays_additive; TestReview583ZeroSharePartitionCover/control_other_scope_isolated",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, sa, sb)
				rules := []economics.RatingRule{
					accRule(t, "acc36-ancestor-free", sa, "0"),
					accRule(t, "acc36-descendant", sb, "1"),
				}
				// Scope one is contradictory (20 > 5) and scope two is clean
				// (30 <= 100). Summed across scopes the ancestor would be 105
				// and the child 50, which is consistent, so a pooled reading
				// would certify complete. Only per-scope independence produces
				// the typed failure while still paying scope two's money.
				conflicted := accObs(t, "acc36-scope-conflicted", "b-leg-acc36-one",
					b1Measure(t, sa, "5"), b1Measure(t, sb, "20"))
				clean := accObs(t, "acc36-scope-clean", "b-leg-acc36-two",
					b1Measure(t, sa, "100"), b1Measure(t, sb, "30"))
				return accCase{
					relationships: relationships, rules: rules,
					observations: []metering.Observation{conflicted, clean},
				}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "50/0",
				payable:      []string{accName("s_child"), accName("s_child")},
			},
		},
		{
			number: 37, name: "input_and_output_are_independent",
			coveredBy: "TestReview62aCrossScopeAndDirectionSubsetControls/cross_direction_stays_additive; TestSchemaModelDirectionUnitIsolation",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, sa, sb)
				outputParent := accUnitKey("s_parent", metering.UnitToken)
				outputParent.Direction = metering.DirectionOutput
				outputChild := accUnitKey("s_child", metering.UnitToken)
				outputChild.Direction = metering.DirectionOutput
				relationships = accEdge(relationships, metering.RelationshipSubset, outputParent, outputChild)
				rules := []economics.RatingRule{
					accRule(t, "acc37-input-ancestor-free", sa, "0"),
					accRule(t, "acc37-input-descendant", sb, "1"),
					accRule(t, "acc37-output-ancestor-free", outputParent, "0"),
					accRule(t, "acc37-output-descendant", outputChild, "1"),
				}
				obs := accObs(t, "acc37-direction-isolation", "b-leg-acc37",
					b1Measure(t, sa, "5"), b1Measure(t, sb, "20"),
					b1Measure(t, outputParent, "100"), b1Measure(t, outputChild, "30"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "50/0",
				payable:      []string{accName("s_child"), accName("s_child")},
			},
		},
		{
			number: 38, name: "different_unit_is_independent",
			coveredBy: "TestSchemaModelDirectionUnitIsolation; TestGeneratedSchemaDirectionUnitIsolation",
			build: func(t *testing.T) accCase {
				t.Helper()
				relationships := accEdge(nil, metering.RelationshipSubset, sa, sb)
				msParent := accUnitKey("s_parent", metering.UnitMillisecond)
				msChild := accUnitKey("s_child", metering.UnitMillisecond)
				relationships = accEdge(relationships, metering.RelationshipSubset, msParent, msChild)
				rules := []economics.RatingRule{
					accRule(t, "acc38-token-ancestor-free", sa, "0"),
					accRule(t, "acc38-token-descendant", sb, "1"),
					accRule(t, "acc38-ms-ancestor-free", msParent, "0"),
					accRule(t, "acc38-ms-descendant", msChild, "1"),
				}
				obs := accObs(t, "acc38-unit-isolation", "b-leg-acc38",
					b1Measure(t, sa, "5"), b1Measure(t, sb, "20"),
					b1Measure(t, msParent, "100"), b1Measure(t, msChild, "30"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				classes:      []error{billing.ErrSchemaSubsetContradiction},
				completeness: economics.CompletenessPartial,
				total:        "50/0",
				payable:      []string{accName("s_child"), accName("s_child")},
			},
		},
		{
			number: 39, name: "negative_control_transform_path_is_not_containment",
			coveredBy: "TestReview64aMixedContainmentChainQuantityConsistency/transform_edge_breaks_containment; TestSchemaModelTransformIsNotContainment; TestGeneratedSchemaTransformIsNotContainment",
			build: func(t *testing.T) accCase {
				t.Helper()
				// A transform edge is not an inclusion edge, so the containment
				// walk must stop at it. The descendant 20 is therefore not
				// bounded by the ancestor 10, and the graph settles complete
				// rather than raising a quantity contradiction.
				relationships := accEdge(nil, metering.RelationshipSubset, sa, sg)
				relationships = accEdge(relationships, metering.RelationshipTransform, sg, sb)
				rules := []economics.RatingRule{
					accRule(t, "acc39-ancestor-free", sa, "0"),
					accRule(t, "acc39-descendant", sb, "1"),
				}
				obs := accObs(t, "acc39-transform-path", "b-leg-acc39",
					b1Measure(t, sa, "10"), b1Measure(t, sb, "20"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "20/0",
				payable:      []string{accName("s_child")},
			},
		},
		{
			number: 40, name: "negative_control_dimension_qualified_keys_stay_distinct",
			coveredBy: "TestSchemaModelStructureSweep (the generated structure sweep covers dimension-qualified key identity); no focused suite pins the explicit-relationship clause",
			build: func(t *testing.T) accCase {
				t.Helper()
				// Two identically named components distinguished only by a
				// dimension are different keys. The declared relationship binds
				// exactly one of them, so the other is a separate contributor and
				// the two stay additive. If dimension identity were collapsed the
				// second one would be pulled into the containment check and
				// produce a spurious contradiction.
				relationships := accEdge(nil, metering.RelationshipSubset, sa, se)
				rules := []economics.RatingRule{
					accRule(t, "acc40-ancestor-free", sa, "0"),
					accRule(t, "acc40-bound-descendant", se, "1"),
					accRule(t, "acc40-unbound-twin", sf, "1"),
				}
				obs := accObs(t, "acc40-dimension-identity", "b-leg-acc40",
					b1Measure(t, sa, "100"), b1Measure(t, se, "20"), b1Measure(t, sf, "30"))
				return accCase{relationships: relationships, rules: rules, observations: []metering.Observation{obs}}
			},
			expect: accExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "50/0",
				payable:      []string{accName("s_dim_one"), accName("s_dim_two")},
			},
		},
	}
}
