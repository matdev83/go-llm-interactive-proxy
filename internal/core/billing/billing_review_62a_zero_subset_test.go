package billing_test

// 62a (PR #666 adversarial re-review at head 62a48177, TEST-ONLY baseline for
// the two remaining P1 source-level holes in the frozen component-schema rating
// state machine).
//
// P1-A: a resolving parent rule was used as a proxy for "the parent line
// carries the commercial basis". Those are not equivalent: a rule can resolve
// while the parent's effective amount is exactly zero. A --partition--> {B, C}
// with A=0 (present, complete, priced at 1), B=60 (present, complete, priced at
// 1) and C ABSENT REQUIRED therefore settled as a false complete USD60 with
// error=nil. C is a missing required partition member and known B=60 already
// exceeds A=0, so the surviving money is children-only. The equivalent vector
// is A=100 with an explicit-free unit rate of 0, B=60, C absent.
//
// P1-B: RelationshipSubset carried no quantity-consistency validation at all.
// A --subset--> S with A=0 and S=20, both present, complete, same
// scope/direction/unit, both priced, settled as a false complete USD20 with
// error=nil. A subset quantity cannot exceed its parent quantity; 0 < 20 is
// inconsistent evidence. This is live on the stock OpenAI family, whose frozen
// schema declares real subset edges (input aggregate -> cache-read input
// tokens, output aggregate -> reasoning output tokens, input aggregate ->
// cache-write tokens) that the adapter accepts as independent non-negative
// counters with no such arithmetic check.
//
// Defensive matrix: A --partition--> {B, C} (C REQUIRED) plus A --subset--> D,
// with A absent, B and D payable and C absent. The cover is unresolved, and the
// schema does not allocate D to B or to C, so disjointness between the paid B
// and the paid D is unknown. Returning "could not prove cover" must not be read
// as permission to additively sum B + D.
import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// f356A1Key and f356A2Key are the P1-A parent keys; f356A1Key is also reused as
// the P1-B parent so the two graphs differ only in their declared edge kind.
func f356A1Key() metering.ComponentKey  { return r7Key("vendor:f62a_parent") }
func f356A2Key() metering.ComponentKey  { return r7Key("vendor:f62a_parent_zero") }
func f356P1BKey() metering.ComponentKey { return r7Key("vendor:f62b_subset") }
func f62AChildB() metering.ComponentKey { return r7Key("vendor:f62a_b") }
func f62AChildC() metering.ComponentKey { return r7Key("vendor:f62a_c") }

// f62Subset declares a single subset containment edge parent -> child.
func f62Subset(parent, child metering.ComponentKey) []metering.ComponentRelationship {
	return []metering.ComponentRelationship{
		{Kind: metering.RelationshipSubset, Parent: parent, Child: child},
	}
}

// f62Total prints the payable total without failing when the valuation was
// correctly rejected, so a RED assertion can print the pre-fix false-complete
// amount.
func f62Total(val economics.Valuation) string {
	if len(val.Totals) == 0 || val.Totals[0].Amount == nil {
		return "<none>"
	}
	return val.Totals[0].Amount.CanonicalString()
}

// f62Reject asserts a valuation certified no complete money and emitted a typed
// diagnostic.
func f62Reject(t *testing.T, seam string, val economics.Valuation, err error, want error) {
	t.Helper()
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("%s: must never certify complete money here; err=%v total=%s lines=%+v",
			seam, err, f62Total(val), val.Lines)
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s: err=%v, want %v; completeness=%q total=%s lines=%+v",
			seam, err, want, val.Completeness, f62Total(val), val.Lines)
	}
}

// TestReview62aZeroChargeParentDoesNotMaskMissingPartitionMember is P1-A. A
// resolving rule is not the same as a commercial basis: the parent must bill a
// positive amount for its own partition to be excused, because a zero-charged
// parent leaves the children as the only money in the scope.
func TestReview62aZeroChargeParentDoesNotMaskMissingPartitionMember(t *testing.T) {
	t.Parallel()
	a := f356A1Key()
	aZeroRate := f356A2Key()
	b, c := f62AChildB(), f62AChildC()
	graph := f356Partition(a, -1, b, c)

	// Primary RED vector: the parent's own quantity is zero, so its line is
	// rated but worth nothing, and the surviving money is entirely B's.
	t.Run("primary_zero_quantity_parent_missing_required_c", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f62a-a-rate", a, "1"),
			b1Rule(t, "f62a-b-rate", b, "1"),
			b1Rule(t, "f62a-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f62a-zero-parent", rules, graph)
		obs := f3Observation(t, "f62a-zero-parent",
			b1Measure(t, a, "0"),
			b1Measure(t, b, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f62Reject(t, seam.name, val, err, billing.ErrSchemaPartitionIncomplete)
			})
		}
	})

	// Equivalent vector: a positive parent quantity whose unit rate is
	// explicitly free produces the same zero effective amount by a different
	// route, so the classification must not key off the parent's quantity
	// either.
	t.Run("primary_explicit_free_parent_missing_required_c", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f62a-free-parent", aZeroRate, "0"),
			b1Rule(t, "f62a-b-rate2", b, "1"),
			b1Rule(t, "f62a-c-rate2", c, "1"),
		}
		resolved := f356Schema(t, "f62a-explicit-free", rules, f356Partition(aZeroRate, -1, b, c))
		obs := f3Observation(t, "f62a-explicit-free",
			b1Measure(t, aZeroRate, "100"),
			b1Measure(t, b, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f62Reject(t, seam.name, val, err, billing.ErrSchemaPartitionIncomplete)
			})
		}
	})

	// Control: supplying the required member C on a CONSERVED partition removes
	// the missing-member diagnosis entirely, even though the parent itself still
	// charges nothing. A's explicit-free rate of 0 means the children are the
	// only money; with 100 = 60 + 40 conserved and both children rateable the
	// cover is proven, so the settlement is a complete USD100 and no incomplete
	// diagnosis may survive. This is the boundary that keeps the repair from
	// failing closed on a proven cover.
	t.Run("control_c_supplied_on_conserved_partition_stays_complete", func(t *testing.T) {
		t.Parallel()
		zeroChargeParent := f356A2Key()
		rules := []economics.RatingRule{
			b1Rule(t, "f62a-free-parent4", zeroChargeParent, "0"),
			b1Rule(t, "f62a-b-rate3", b, "1"),
			b1Rule(t, "f62a-c-rate3", c, "1"),
		}
		resolved := f356Schema(t, "f62a-c-supplied", rules, f356Partition(zeroChargeParent, -1, b, c))
		obs := f3Observation(t, "f62a-c-supplied",
			b1Measure(t, zeroChargeParent, "100"),
			b1Measure(t, b, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: a fully present, conserved partition must not report a missing member; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, f62Total(val), val.Lines)
				}
				if err != nil {
					t.Fatalf("%s: conserved child-only partition must rate completely, got %v; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, f62Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (B 60 + C 40)", seam.name, total)
				}
			})
		}
	})

	// Control: a POSITIVE parent charge is the commercial basis for its own
	// partition, so an unpriced required child that the provider simply did not
	// report must stay an aggregate-only rating. This is the deliberate
	// boundary of the P1-A repair and is the stock OpenAI
	// input_token/text_token shape.
	t.Run("control_positive_charge_parent_aggregate_only_stays_complete", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f62a-agg-only", a, "1")}
		resolved := f356Schema(t, "f62a-aggregate-only", rules, graph)
		obs := f3Observation(t, "f62a-aggregate-only",
			b1Measure(t, a, "100"),
			b1Measure(t, b, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: an aggregate-only tariff with a positive parent charge must rate completely, got %v; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, f62Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (A only)", seam.name, total)
				}
			})
		}
	})
}

// TestReview62aSubsetQuantityConsistency is P1-B. A declared subset quantity
// may never exceed its parent's quantity in the same scope, direction and unit.
// The check is a quantity-consistency fact, so it must hold before effective
// charge positivity can drop the parent out of the monetary overlap graph.
func TestReview62aSubsetQuantityConsistency(t *testing.T) {
	t.Parallel()
	a := f356A1Key()
	s := f356P1BKey()
	graph := f62Subset(a, s)

	type vector struct {
		name           string
		parentAmount   string
		subsetAmount   string
		parentPrice    string
		subsetPrice    string
		parentUnpriced bool
		wantContradict bool
		wantTotal      string
	}
	// The parent's unit price is 0 (or absent) in every vector so the parent's
	// effective amount is never positive. That isolates the quantity invariant:
	// a priced parent plus a priced subset is already rejected by the separate,
	// pre-existing payable-overlap contract, which is asserted once below and
	// must keep owning that case.
	vectors := []vector{
		// The reported hole: 0 < 20 is inconsistent evidence and the parent's
		// own charge cannot mask it.
		{name: "parent_zero_subset_positive", parentAmount: "0", subsetAmount: "20", parentPrice: "0", subsetPrice: "1", wantContradict: true},
		{name: "parent_ten_subset_twenty", parentAmount: "10", subsetAmount: "20", parentPrice: "0", subsetPrice: "1", wantContradict: true},
		{name: "parent_ten_price_one_subset_twenty", parentAmount: "10", subsetAmount: "20", parentPrice: "1", subsetPrice: "0", wantContradict: true},
		// A subset equal to its parent is the whole-container boundary and is
		// valid evidence.
		{name: "equal_quantities_valid", parentAmount: "20", subsetAmount: "20", parentPrice: "0", subsetPrice: "1", wantTotal: "20/0"},
		{name: "subset_strictly_inside_valid", parentAmount: "100", subsetAmount: "20", parentPrice: "0", subsetPrice: "1", wantTotal: "20/0"},
		// A present, complete, zero subset inside its parent is valid evidence
		// and must never be reported.
		{name: "zero_subset_inside_parent_valid", parentAmount: "100", subsetAmount: "0", parentPrice: "0", subsetPrice: "1", wantTotal: "0/0"},
	}
	for _, testCase := range vectors {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rules := []economics.RatingRule{b1Rule(t, "f62b-subset-rate", s, testCase.subsetPrice)}
			if !testCase.parentUnpriced {
				rules = append(rules, b1Rule(t, "f62b-parent-rate", a, testCase.parentPrice))
			}
			resolved := f356Schema(t, "f62b-"+testCase.name, rules, graph)
			obs := f3Observation(t, "f62b-"+testCase.name,
				b1Measure(t, a, testCase.parentAmount),
				b1Measure(t, s, testCase.subsetAmount))
			for _, seam := range review5beSeams() {
				seam := seam
				t.Run(seam.name, func(t *testing.T) {
					seam := seam
					t.Parallel()
					val, err := seam.rate(t, resolved, obs)
					if testCase.wantContradict {
						f62Reject(t, seam.name, val, err, billing.ErrSchemaSubsetContradiction)
						return
					}
					if err != nil {
						t.Fatalf("%s: err=%v, want no error; completeness=%q total=%s lines=%+v",
							seam.name, err, val.Completeness, f62Total(val), val.Lines)
					}
					if val.Completeness != economics.CompletenessComplete {
						t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
					}
					if total := b1PayableTotal(t, val); total != testCase.wantTotal {
						t.Fatalf("%s: total=%s, want %s; lines=%+v", seam.name, total, testCase.wantTotal, val.Lines)
					}
				})
			}
		})
	}

	// Only the child is priced, so the parent has no rule of its own. The
	// quantities are consistent (20 <= 20) and the quantity check must stay
	// silent; the parent's own missing-rate diagnostic is what makes the
	// valuation partial, and that is a separate, pre-existing contract.
	t.Run("only_child_priced_is_not_a_quantity_contradiction", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f62b-child-only-rate", s, "1")}
		resolved := f356Schema(t, "f62b-only-child-priced", rules, graph)
		obs := f3Observation(t, "f62b-only-child-priced",
			b1Measure(t, a, "20"),
			b1Measure(t, s, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				_, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: consistent 20 <= 20 quantities must never report a subset contradiction; err=%v", seam.name, err)
				}
			})
		}
	})

	// A priced parent plus a priced subset stays owned by the pre-existing
	// payable-overlap contract. The new quantity check must neither replace nor
	// double-report it, and it must stay silent because 20 <= 20 holds.
	t.Run("both_priced_still_owned_by_payable_overlap", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f62b-both-parent", a, "1"),
			b1Rule(t, "f62b-both-subset", s, "1"),
		}
		resolved := f356Schema(t, "f62b-both-priced", rules, graph)
		obs := f3Observation(t, "f62b-both-priced",
			b1Measure(t, a, "20"),
			b1Measure(t, s, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: a priced parent plus a priced subset must stay the typed overlap conflict; err=%v completeness=%q total=%s",
						seam.name, err, val.Completeness, f62Total(val))
				}
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: consistent 20 <= 20 quantities must not add a subset contradiction; err=%v", seam.name, err)
				}
			})
		}
	})

	// Missing and unavailable operands must stay missing: the check must never
	// invent a zero for an absent or unquantified operand, so neither shape may
	// be reported as a subset quantity contradiction.
	t.Run("absent_parent_measure_is_not_a_contradiction", func(t *testing.T) {
		t.Parallel()
		absentParent := f356A2Key()
		rules := []economics.RatingRule{
			b1Rule(t, "f62b-absent-parent-rate", absentParent, "1"),
			b1Rule(t, "f62b-absent-subset-rate", s, "1"),
		}
		resolved := f356Schema(t, "f62b-absent-parent", rules, f62Subset(absentParent, s))
		obs := f3Observation(t, "f62b-absent-parent", b1Measure(t, s, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				_, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: an absent parent quantity must stay missing, not be invented as a zero; err=%v", seam.name, err)
				}
			})
		}
	})
	t.Run("unavailable_parent_measure_is_not_a_contradiction", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f62b-unavailable-parent-rate", a, "1"),
			b1Rule(t, "f62b-unavailable-subset-rate", s, "1"),
		}
		resolved := f356Schema(t, "f62b-unavailable-parent", rules, graph)
		obs := f3Observation(t, "f62b-unavailable-parent",
			b1UnavailableMeasure(a),
			b1Measure(t, s, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				_, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: an unavailable parent quantity must stay missing, not be invented as a zero; err=%v", seam.name, err)
				}
			})
		}
	})
}

// TestReview62aCrossScopeAndDirectionSubsetControls pins that the subset
// quantity check never reaches across reduction scopes, economic directions or
// unit derivations, and that a transform edge is not a containment at all.
func TestReview62aCrossScopeAndDirectionSubsetControls(t *testing.T) {
	t.Parallel()
	a := f356A1Key()
	s := f356P1BKey()

	// Cross-scope: the parent sits in a different B-leg scope from the subset.
	t.Run("cross_scope_stays_additive", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f62x-a-rate", a, "1"),
			b1Rule(t, "f62x-s-rate", s, "1"),
		}
		resolved := f356Schema(t, "f62x-cross-scope", rules, f62Subset(a, s))
		obsParent := b1Observation(t, "f62x-cross-scope-parent", "b-leg-parent", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "0"))
		obsSubset := b1Observation(t, "f62x-cross-scope-subset", "b-leg-subset", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, s, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obsParent, obsSubset}))
		if errors.Is(rateErr, billing.ErrSchemaSubsetContradiction) {
			t.Fatalf("per-scope subset arithmetic must not reach across scopes; err=%v lines=%+v", rateErr, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "20/0" {
			t.Fatalf("independent scopes total=%s, want 20 (S only)", total)
		}
	})

	// Cross-direction: the same component names in different directions are
	// different quantities, so an inverted pair must stay additive.
	t.Run("cross_direction_stays_additive", func(t *testing.T) {
		t.Parallel()
		parentKey := r7Key("vendor:f62x_dir_total")
		childKey := metering.ComponentKey{
			Direction: metering.DirectionOutput,
			Component: f356P1BKey().Component,
			Unit:      f356P1BKey().Unit,
			SchemaID:  f356P1BKey().SchemaID,
		}
		rules := []economics.RatingRule{
			b1Rule(t, "f62x-dir-parent", parentKey, "1"),
			b1Rule(t, "f62x-dir-child", childKey, "1"),
		}
		resolved := f356Schema(t, "f62x-cross-direction", rules, f62Subset(parentKey, childKey))
		obs := f3Observation(t, "f62x-cross-direction",
			b1Measure(t, parentKey, "0"),
			b1Measure(t, childKey, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: cross-direction quantities must not be compared; err=%v lines=%+v", seam.name, err, val.Lines)
				}
			})
		}
	})

	// A transform edge is a separately governed unit derivation, never a
	// containment, so it must never be arithmetic-checked.
	t.Run("transform_edge_is_not_containment", func(t *testing.T) {
		t.Parallel()
		schemas := []metering.ComponentSchema{{
			ID: b1SchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipTransform, Parent: a, Child: s},
			},
		}}
		rules := []economics.RatingRule{
			b1Rule(t, "f62x-tr-parent", a, "1"),
			b1Rule(t, "f62x-tr-child", s, "1"),
		}
		resolved := f3Resolved(t, "f62x-transform", rules, schemas)
		obs := f3Observation(t, "f62x-transform",
			b1Measure(t, a, "0"),
			b1Measure(t, s, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: a transform edge is not containment and must not be compared; err=%v lines=%+v", seam.name, err, val.Lines)
				}
			})
		}
	})
}

// TestReview62aOpenAIFamilySubsetKeysAreChecked pins the stock provider-family
// shape: the frozen OpenAI inclusion schema declares the ordinary aggregate
// total with an included cache-read input subset, and the adapter accepts that
// counter independently. An inverted cached-token count must not reach the
// generic V2 rater as complete money.
func TestReview62aOpenAIFamilySubsetKeysAreChecked(t *testing.T) {
	t.Parallel()
	aggregate := b1Key(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken)
	cached := b1Key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: aggregate, Child: cached},
		},
	}}
	rules := []economics.RatingRule{
		b1Rule(t, "f62o-aggregate-rate", aggregate, "1"),
		b1Rule(t, "f62o-cached-rate", cached, "1"),
	}
	resolved := f3Resolved(t, "f62o-cache-read", rules, schemas)
	obs := f3Observation(t, "f62o-cache-read",
		b1Measure(t, aggregate, "0"),
		b1Measure(t, cached, "40"))
	for _, seam := range review5beSeams() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			seam := seam
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			f62Reject(t, seam.name, val, err, billing.ErrSchemaSubsetContradiction)
		})
	}
}

// TestReview62aUnresolvedCoverWithPayableSubsetFailsClosed is the defensive
// matrix. A --partition--> {B, C} with C REQUIRED plus A --subset--> D, with A
// absent, B and D payable and C absent. The schema does not allocate D to B or
// to C, so disjointness between the two paid lines is unknown; "could not prove
// cover" must not authorize an additive B + D settlement.
func TestReview62aUnresolvedCoverWithPayableSubsetFailsClosed(t *testing.T) {
	t.Parallel()
	a := f356A2Key()
	b, c, d := f62AChildB(), f62AChildC(), f356DKey()
	relationships := f356Partition(a, -1, b, c)
	relationships = f356Subset(relationships, a, d)
	rules := []economics.RatingRule{
		b1Rule(t, "f62d-b-rate", b, "1"),
		b1Rule(t, "f62d-d-rate", d, "1"),
	}
	resolved := f356Schema(t, "f62d-unresolved-cover", rules, relationships)
	obs := f3Observation(t, "f62d-unresolved-cover",
		b1Measure(t, b, "60"),
		b1Measure(t, d, "20"))
	for _, seam := range review5beSeams() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			seam := seam
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			f62Reject(t, seam.name, val, err, billing.ErrSchemaPartitionIncomplete)
			if count := review5beCountComponentLines(val, d); count != 0 {
				t.Fatalf("%s: the unplaceable subset descendant D must be withheld, got %d line(s); total=%s lines=%+v",
					seam.name, count, f62Total(val), val.Lines)
			}
		})
	}

	// Control: the same graph with the required member C supplied makes the
	// partition complete and billable, so B and D are then provably disjoint
	// only if the cover is proven. Supplying a positive, priced C keeps the
	// declared partition incomplete arithmetically (A is absent), so the cover
	// stays unresolved and the fail-closed classification must persist. This
	// pins that the gate is the cover proof, not the mere presence of a measure.
	t.Run("control_cover_still_unresolved_keeps_fail_closed", func(t *testing.T) {
		t.Parallel()
		controlRules := append(append([]economics.RatingRule(nil), rules...),
			b1Rule(t, "f62d-c-rate", c, "1"))
		resolvedControl := f356Schema(t, "f62d-unresolved-control", controlRules, relationships)
		obsControl := f3Observation(t, "f62d-unresolved-control",
			b1Measure(t, b, "60"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				val, err := seam.rate(t, resolvedControl, obsControl)
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("%s: an absent parent leaves D unplaceable, so additive settlement must not be certified; err=%v total=%s lines=%+v",
						seam.name, err, f62Total(val), val.Lines)
				}
			})
		}
	})
}
