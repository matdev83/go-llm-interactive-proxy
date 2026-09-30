package billing_test

// 583 (PR #666 review finding P1-A hotfix): an explicitly present, complete,
// exactly-zero effective partition member is a genuine zero share. It needs no
// pricing rule to account for that share, but it must never be invented as a
// priced line. Before the repair the cover resolver demanded a proven child
// partition for such a member and rejected the whole cover, so a payable subset
// child of the same absent parent was never checked against the parent's
// complete partition and the additive lines settled as a false complete USD120.
//
// The graph is one scope/direction/unit/schema: A --partition--> {B, C},
// A --subset--> D, with A absent, B=0 (no rate), C=100 (linear USD1) and D=20
// (linear USD1). The repair derives the per-scope PRESENT + COMPLETE + EXACT
// ZERO canonical keys from the same reduced/mask-filtered consistency evidence
// the pricing path already uses, treats them as accounted zero-share cover
// members without a rate, and keeps them in the cover identity set for conflict
// suppression while leaving them out of the positive monetary contributor set.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// review583Schema declares A --partition--> {B, C} and A --subset--> D. B and C
// may be declared optional so the absent-required / absent-optional distinction
// is exercised by the same shape.
func review583Schema(parent, partB, partC, subsetD metering.ComponentKey, bOptional, cOptional bool) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB, Optional: bOptional},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC, Optional: cOptional},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
		},
	}}
}

// review583PartitionOnlySchema declares A --partition--> {B, C} without the
// subset child, for the same B=0,C=100 control and the A-present alignment.
func review583PartitionOnlySchema(parent, partB, partC metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
		},
	}}
}

// review583Total reports the first payable total without failing when the
// valuation was correctly rejected with no payable total, so a RED assertion can
// print the actual pre-fix USD120 and the post-fix <none>.
func review583Total(val economics.Valuation) string {
	if len(val.Totals) == 0 || val.Totals[0].Amount == nil {
		return "<none>"
	}
	return val.Totals[0].Amount.CanonicalString()
}

func TestReview583ZeroSharePartitionCover(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:583_parent")
	partB := r7Key("vendor:583_b")
	partC := r7Key("vendor:583_c")
	subsetD := r7Key("vendor:583_d")

	// Primary: A absent, B=0 unpriced, C=100 and D=20 priced. D is an included
	// subset of A whose complete partition is B + C, so billing C and D
	// additively double-charges and must fail closed.
	t.Run("primary_zero_b_priced_d_rejected", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-zero-share", rules, review583Schema(parent, partB, partC, subsetD, false, false))
		obs := f3Observation(t, "583-zero-share",
			b1Measure(t, partB, "0"),
			b1Measure(t, partC, "100"),
			b1Measure(t, subsetD, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: explicit-zero partition member must not hide the priced subset overlap; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// Control: the same B=0,C=100 without the priced subset stays complete at
	// USD100 with C emitted exactly once and no synthetic zero line.
	t.Run("control_zero_b_without_d_complete_100", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "583-c-rate", partC, "1")}
		resolved := f3Resolved(t, "583-zero-control", rules, review583PartitionOnlySchema(parent, partB, partC))
		obs := f3Observation(t, "583-zero-control",
			b1Measure(t, partB, "0"),
			b1Measure(t, partC, "100"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: explicit-zero conserved partition must rate completely, got %v; total=%s lines=%+v",
						seam.name, err, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (C only)", seam.name, total)
				}
				if count := review5beCountComponentLines(val, partC); count != 1 {
					t.Fatalf("%s: component C emitted %d times, want exactly once; lines=%+v", seam.name, count, val.Lines)
				}
				if count := review5beCountComponentLines(val, partB); count != 0 {
					t.Fatalf("%s: zero-share B must not be invented as a line, got %d; lines=%+v", seam.name, count, val.Lines)
				}
			})
		}
	})

	// Control: an explicit-free resolving rule on B makes it rateable (not only
	// zero-share), so the same priced subset D must still be rejected.
	t.Run("control_explicit_free_b_still_rejected", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-b-free", partB, "0"),
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-explicit-free", rules, review583Schema(parent, partB, partC, subsetD, false, false))
		obs := f3Observation(t, "583-explicit-free",
			b1Measure(t, partB, "0"),
			b1Measure(t, partC, "100"),
			b1Measure(t, subsetD, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: explicit-free zero B must not excuse the priced subset; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// Guard: a POSITIVE unpriced member is not an accounted zero share, so the
	// zero-share shortcut must not certify the cover. If it did, the priced
	// subset D would be rejected as an overlap conflict; assert that it is not.
	t.Run("guard_positive_unpriced_b_not_zero_share", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-positive-unpriced", rules, review583Schema(parent, partB, partC, subsetD, false, false))
		obs := f3Observation(t, "583-positive-unpriced",
			b1Measure(t, partB, "100"),
			b1Measure(t, partC, "100"),
			b1Measure(t, subsetD, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: positive unpriced B must not be treated as an accounted zero share; err=%v lines=%+v",
						seam.name, err, val.Lines)
				}
			})
		}
	})

	// Guard: an ABSENT required member is missing evidence, not a zero share, so
	// it must not manufacture a synthetic cover and falsely certify the overlap.
	t.Run("guard_absent_required_b_no_synthetic_cover", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-absent-required", rules, review583Schema(parent, partB, partC, subsetD, false, false))
		obs := f3Observation(t, "583-absent-required",
			b1Measure(t, partC, "100"),
			b1Measure(t, subsetD, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: absent required B must not synthesize a zero cover; err=%v lines=%+v",
						seam.name, err, val.Lines)
				}
			})
		}
	})

	// Control: an ABSENT optional member contributes the schema's declared zero
	// and does not block coverage, so the priced subset D is correctly rejected.
	t.Run("control_absent_optional_b_declares_zero", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-absent-optional", rules, review583Schema(parent, partB, partC, subsetD, true, false))
		obs := f3Observation(t, "583-absent-optional",
			b1Measure(t, partC, "100"),
			b1Measure(t, subsetD, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: absent optional zero B must still complete the cover and reject priced D; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// A-present pin (N2 compatibility): completeChildPartitionCoverage
	// deliberately keeps the missing-rate diagnostic for a conserved child-only
	// partition whose required zero child has no rule (N2), so this stays
	// partial and must never become a partition contradiction. The P1-A
	// zero-share repair is confined to the overlap cover resolver and does not
	// alter this diagnosis.
	t.Run("control_apresent_zero_b_keeps_pinned_partial_diagnostic", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "583-c-rate", partC, "1")}
		resolved := f3Resolved(t, "583-apresent-zero", rules, review583PartitionOnlySchema(parent, partB, partC))
		obs := f3Observation(t, "583-apresent-zero",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "0"),
			b1Measure(t, partC, "100"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("%s: conserved 100 = 0 + 100 must not be a contradiction; err=%v lines=%+v",
						seam.name, err, val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial (pinned child-only diagnostic); lines=%+v",
						seam.name, val.Completeness, val.Lines)
				}
				line := b2aComponentLine(t, val, parent)
				if line == nil || line.Status != economics.RatingLineRateMissing {
					t.Fatalf("%s: conserved child-only parent must keep its pinned missing-rate diagnostic; line=%+v lines=%+v",
						seam.name, line, val.Lines)
				}
			})
		}
	})

	// Scope isolation: the zero-share member and the priced partition child are
	// in one B-leg scope while the subset child is in another. The cover must not
	// reach across scopes, so the two scopes stay additive and nonconflicting.
	t.Run("control_other_scope_isolated", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "583-c-rate", partC, "1"),
			b1Rule(t, "583-d-rate", subsetD, "1"),
		}
		resolved := f3Resolved(t, "583-scope-isolation", rules, review583Schema(parent, partB, partC, subsetD, false, false))
		obsCover := b1Observation(t, "583-scope-cover", "b-leg-cover", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, partB, "0"), b1Measure(t, partC, "100"))
		obsSubset := b1Observation(t, "583-scope-subset", "b-leg-subset", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, subsetD, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obsCover, obsSubset}))
		if errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("per-scope zero-share cover must not reach across scopes; err=%v lines=%+v", rateErr, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "120/0" {
			t.Fatalf("independent scopes total=%s, want 120 (C 100 + D 20)", total)
		}
	})
}

// TestReview583AmbiguityLocality is P1-B: an unrelated complete-cover ambiguity
// (two distinct parents sharing one child) must not globally disable complete
// cover for every other parent. The taint is local -- only the parents that
// actually declare a shared child are denied coverage -- so an unaffected
// A/B-absent nested cover still fails closed as the typed overlap conflict, and
// an independent conserved child-only partition elsewhere still rates complete.
// Disjoint ambiguous fragments never poison a third valid graph, in any
// declaration order.
func TestReview583AmbiguityLocality(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:583loc_parent") // A
	partB := r7Key("vendor:583loc_b")       // B
	partC := r7Key("vendor:583loc_c")       // C
	partX := r7Key("vendor:583loc_x")       // X
	partY := r7Key("vendor:583loc_y")       // Y
	subsetD := r7Key("vendor:583loc_d")     // D
	z1 := r7Key("vendor:583loc_z1")
	z2 := r7Key("vendor:583loc_z2")
	sharedS := r7Key("vendor:583loc_s")
	zAmbiguity := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: z1, Child: sharedS},
		{Kind: metering.RelationshipPartition, Parent: z2, Child: sharedS},
	}

	// affectedCover declares A --partition--> {B, C}, B --partition--> {X, Y}
	// and A --subset--> D. When ambiguity is set, the unrelated unobserved
	// Z1/Z2 --partition--> S fragment that shares S is appended last.
	affectedCover := func(ambiguity bool) []metering.ComponentSchema {
		rels := []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipPartition, Parent: partB, Child: partX},
			{Kind: metering.RelationshipPartition, Parent: partB, Child: partY},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
		}
		if ambiguity {
			rels = append(rels, zAmbiguity...)
		}
		return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: rels}}
	}
	primaryRules := []economics.RatingRule{
		b1Rule(t, "583loc-x-rate", partX, "1"),
		b1Rule(t, "583loc-y-rate", partY, "1"),
		b1Rule(t, "583loc-c-rate", partC, "1"),
		b1Rule(t, "583loc-d-rate", subsetD, "1"),
	}
	primaryObs := []metering.Measure{
		b1Measure(t, partX, "30"),
		b1Measure(t, partY, "30"),
		b1Measure(t, partC, "40"),
		b1Measure(t, subsetD, "20"),
	}

	// 1. Primary RED: before the repair the unrelated S ambiguity globally nils
	// every complete cover, so the priced subset D is never checked against A's
	// absent complete partition and the additive lines settle as a false
	// complete USD120.
	t.Run("primary_unrelated_ambiguity_still_rejects_overlap", func(t *testing.T) {
		t.Parallel()
		resolved := f3Resolved(t, "583loc-ambiguous", primaryRules, affectedCover(true))
		obs := f3Observation(t, "583loc-ambiguous", primaryObs...)
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: unrelated shared-child ambiguity must not hide the priced subset overlap; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// 2. Control: the identical graph without the unrelated fragment is the
	// existing 5B-2 overlap rejection, unchanged by locality.
	t.Run("control_without_ambiguity_same_overlap", func(t *testing.T) {
		t.Parallel()
		resolved := f3Resolved(t, "583loc-clean", primaryRules, affectedCover(false))
		obs := f3Observation(t, "583loc-clean", primaryObs...)
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: clean graph must keep the 5B-2 overlap rejection; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// 3. Independent conserved child-only partition A = 100 with B=60, C=40
	// (B/C priced, A unpriced). The unrelated unobserved Z fragment must not
	// deny A its coverage: A is covered by its conserved partition and stays a
	// complete USD100 with B and C billed exactly once, exactly as without Z.
	t.Run("independent_cover_unaffected_by_unrelated_ambiguity", func(t *testing.T) {
		t.Parallel()
		schemas := []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: append([]metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
		}, zAmbiguity...)}}
		rules := []economics.RatingRule{
			b1Rule(t, "583loc-b-rate", partB, "1"),
			b1Rule(t, "583loc-c-rate", partC, "1"),
		}
		resolved := f3Resolved(t, "583loc-independent", rules, schemas)
		obs := f3Observation(t, "583loc-independent",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: independent conserved cover must rate completely despite unrelated ambiguity, got %v; total=%s lines=%+v",
						seam.name, err, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (B 60 + C 40)", seam.name, total)
				}
				for _, child := range []metering.ComponentKey{partB, partC} {
					if count := review5beCountComponentLines(val, child); count != 1 {
						t.Fatalf("%s: child %s emitted %d times, want exactly once; lines=%+v", seam.name, child.Component, count, val.Lines)
					}
				}
			})
		}
	})

	// 4. Guard: a parent Z1 that actually declares the shared child S and is
	// observed complete with S priced must not be treated as covered. S's true
	// owner is ambiguous, so Z1 stays the existing typed incomparable/partial
	// diagnosis; locality never makes one shared-child owner authoritative.
	t.Run("shared_child_parent_still_incomparable", func(t *testing.T) {
		t.Parallel()
		schemas := []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: zAmbiguity}}
		rules := []economics.RatingRule{b1Rule(t, "583loc-s-rate", sharedS, "1")}
		resolved := f3Resolved(t, "583loc-shared-child", rules, schemas)
		obs := f3Observation(t, "583loc-shared-child",
			b1Measure(t, z1, "100"),
			b1Measure(t, sharedS, "100"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
					t.Fatalf("%s: observed parent of a shared child must stay typed incomparable; err=%v completeness=%q lines=%+v",
						seam.name, err, val.Completeness, val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
			})
		}
	})

	// 5. Two disjoint ambiguous fragments (Z1/Z2->S and W1/W2->T) must not
	// poison a third valid child-only graph, and swapping the declaration order
	// of the fragments must not change the outcome.
	t.Run("disjoint_ambiguities_declaration_order_invariant", func(t *testing.T) {
		t.Parallel()
		w1 := r7Key("vendor:583loc_w1")
		w2 := r7Key("vendor:583loc_w2")
		sharedT := r7Key("vendor:583loc_t")
		zFragment := []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: z1, Child: sharedS},
			{Kind: metering.RelationshipPartition, Parent: z2, Child: sharedS},
		}
		wFragment := []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: w1, Child: sharedT},
			{Kind: metering.RelationshipPartition, Parent: w2, Child: sharedT},
		}
		validGraph := func(fragments ...[]metering.ComponentRelationship) []metering.ComponentSchema {
			rels := []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			}
			for _, fragment := range fragments {
				rels = append(rels, fragment...)
			}
			return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: rels}}
		}
		rules := []economics.RatingRule{
			b1Rule(t, "583loc-b-rate2", partB, "1"),
			b1Rule(t, "583loc-c-rate2", partC, "1"),
		}
		obs := f3Observation(t, "583loc-order",
			b1Measure(t, parent, "100"),
			b1Measure(t, partB, "60"),
			b1Measure(t, partC, "40"))
		for _, order := range []struct {
			name   string
			schema []metering.ComponentSchema
		}{
			{name: "z_then_w", schema: validGraph(zFragment, wFragment)},
			{name: "w_then_z", schema: validGraph(wFragment, zFragment)},
		} {
			order := order
			t.Run(order.name, func(t *testing.T) {
				t.Parallel()
				resolved := f3Resolved(t, "583loc-order-"+order.name, rules, order.schema)
				for _, seam := range review5beSeams() {
					seam := seam
					t.Run(seam.name, func(t *testing.T) {
						t.Parallel()
						val, err := seam.rate(t, resolved, obs)
						if err != nil {
							t.Fatalf("%s: disjoint ambiguities must not poison the valid graph, got %v; total=%s lines=%+v",
								seam.name, err, review583Total(val), val.Lines)
						}
						if val.Completeness != economics.CompletenessComplete {
							t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
						}
						if total := b1PayableTotal(t, val); total != "100/0" {
							t.Fatalf("%s: total=%s, want 100", seam.name, total)
						}
					})
				}
			})
		}
	})
}
