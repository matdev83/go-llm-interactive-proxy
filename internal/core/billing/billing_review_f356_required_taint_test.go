package billing_test

// f356 (PR #666 adversarial re-review at head f356b681, TEST-ONLY baseline for
// the two remaining P1 correctness gaps and the P2 availability gap in the
// frozen-schema rating state machine of component_rater.go).
//
// P1-1 -- missing required partition member must never certify complete money.
// Graph: TOTAL --partition--> {B, C}, TOTAL is the INFORMATIONAL
// input_token_total summary (no pricing rule), B=60 priced, C ABSENT and
// REQUIRED. completeChildPartitionCoverage correctly refuses the cover
// (covers=false, comparable=false) but records neither contradiction nor
// incomparable, the rating loop then skips TOTAL because it is informational
// and unpriced, and C has no measure at all, so nothing is partial: the
// valuation settles as a false complete USD60. The invariant is that missing
// fields stay MISSING; a declared complete partition whose required member is
// not observed is incomplete evidence, and that is an independent
// classification exactly like contradiction/incomparable.
//
// P1-2 -- a tainted absent parent must not silently disable the overlap check.
// Graph: A --partition--> {B, C}, Z --partition--> B (so B is shared and A/Z
// are tainted complete parents), A --subset--> D, with A and Z ABSENT and
// B=60, C=40, D=20 all priced. collectCompleteCoverMembers refuses A at entry
// because it is tainted, the caller only continues, no ambiguity/conflict is
// recorded anywhere, and B + C + D settle as a false complete USD120. The
// system cannot prove D is disjoint from the already-paid A partition, so
// "could not prove cover" must fail closed rather than be read as permission to
// additively sum the descendants. The repair therefore needs a tri-state cover
// result (covered / uncovered-or-missing / tainted-incomparable) and must keep
// the taint LOCAL so an unrelated ambiguous fragment still cannot poison an
// independent valid cover.
//
// P2 -- a recursively proven paid cover is not propagated to the parent's
// rating completeness. Graph: A --partition--> {B, C}, B --partition--> {X, Y},
// A=100 present and unpriced, B=60 present and unpriced, X=30, Y=30, C=40 all
// priced. Everything conserves exactly (100 = B 60 + C 40 = X 30 + Y 30 +
// C 40) and the recursive overlap resolver already understands the nested paid
// cover, but completeChildPartitionCoverage was not recursive: A was never
// marked covered, so A kept a missing-rate line and the valuation stayed
// partial.

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// f356TotalKey is the informational summary component the P1-1 graph uses. It
// is the exact production total the rating loop skips for lack of a rule.
func f356TotalKey() metering.ComponentKey {
	return r7Key("input_token_total")
}

// f356B1Key, f356B2Key, f356CKey, f356XKey, f356YKey, f356DKey and f356ZKey are
// the remaining named nodes of the P1-1/P1-2/P2 graphs.
func f356B1Key() metering.ComponentKey { return r7Key("vendor:f356_b1") }
func f356B2Key() metering.ComponentKey { return r7Key("vendor:f356_b2") }
func f356CKey() metering.ComponentKey  { return r7Key("vendor:f356_c") }
func f356XKey() metering.ComponentKey  { return r7Key("vendor:f356_x") }
func f356YKey() metering.ComponentKey  { return r7Key("vendor:f356_y") }
func f356DKey() metering.ComponentKey  { return r7Key("vendor:f356_d") }
func f356ZKey() metering.ComponentKey  { return r7Key("vendor:f356_z") }

// f356Partition declares parent --partition--> children as complete coverage.
// A child is declared optional only when it is the single optionalChild
// argument, so an absent REQUIRED member is exercised by the default call.
func f356Partition(parent metering.ComponentKey, optionalChild int, children ...metering.ComponentKey) []metering.ComponentRelationship {
	relationships := make([]metering.ComponentRelationship, 0, len(children))
	for i, child := range children {
		relationships = append(relationships, metering.ComponentRelationship{
			Kind: metering.RelationshipPartition, Parent: parent, Child: child, Optional: i == optionalChild,
		})
	}
	return relationships
}

// f356Subset appends a declared subset containment edge parent --subset--> child
// to an existing relationship set.
func f356Subset(relationships []metering.ComponentRelationship, parent, child metering.ComponentKey) []metering.ComponentRelationship {
	return append(relationships, metering.ComponentRelationship{
		Kind: metering.RelationshipSubset, Parent: parent, Child: child,
	})
}

// f356Schema wraps one relationship set into the single frozen schema every
// f356 graph uses, so the graphs differ only in their declared edges.
func f356Schema(t *testing.T, refID string, rules []economics.RatingRule, relationships []metering.ComponentRelationship) economics.TariffSnapshot {
	t.Helper()
	return f3Resolved(t, refID, rules, []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: relationships}})
}

// f356AssertRejectedPartition fails closed on a valuation that certified
// complete money from incomplete or ambiguous evidence, printing the pre-fix
// USD total so a RED run is self-documenting.
func f356AssertRejectedPartition(t *testing.T, seam string, val economics.Valuation, err error) {
	t.Helper()
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("%s: must never certify complete money from an incomplete or ambiguous partition; err=%v total=%s lines=%+v",
			seam, err, review583Total(val), val.Lines)
	}
	if err == nil {
		t.Fatalf("%s: an unprovable partition must emit an explicit typed diagnostic; completeness=%q total=%s lines=%+v",
			seam, val.Completeness, review583Total(val), val.Lines)
	}
}

// TestReviewF356MissingRequiredPartitionMemberIsNotComplete is P1-1. The
// observed, complete, informational TOTAL declares the complete partition
// {B, C} with REQUIRED C. C is absent, so the complete partition is not known
// and the children-only money must not look complete.
func TestReviewF356MissingRequiredPartitionMemberIsNotComplete(t *testing.T) {
	t.Parallel()
	total := f356TotalKey()
	b1, c := f356B1Key(), f356CKey()
	graph := f356Partition(total, -1, b1, c)

	t.Run("primary_informational_parent_missing_required_c", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f356-p1-b-rate", b1, "1")}
		resolved := f356Schema(t, "f356-p1-missing-required", rules, graph)
		obs := f3Observation(t, "f356-p1-missing-required",
			b1Measure(t, total, "100"),
			b1Measure(t, b1, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f356AssertRejectedPartition(t, seam.name, val, err)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomplete (absent REQUIRED member is missing evidence, not a contradiction and not an ambiguity); completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
			})
		}
	})

	// Control: the same graph with C supplied conserves exactly (100 = 60 + 40)
	// and the child-only tariff supports it, so the valuation must be complete
	// USD100. The P1-1 classification must be scoped to genuinely missing
	// evidence, not to the informational-parent shape itself.
	t.Run("control_informational_parent_conserved_complete_100", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p1-b-rate", b1, "1"),
			b1Rule(t, "f356-p1-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p1-conserved", rules, graph)
		obs := f3Observation(t, "f356-p1-conserved",
			b1Measure(t, total, "100"),
			b1Measure(t, b1, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: conserved informational partition must rate completely, got %v; total=%s lines=%+v",
						seam.name, err, review583Total(val), val.Lines)
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

	// Control: an absent OPTIONAL member keeps the schema's optional-zero
	// semantics, so the same shape stays valid and complete at USD60.
	t.Run("control_absent_optional_c_declares_zero", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f356-p1-b-rate", b1, "1")}
		resolved := f356Schema(t, "f356-p1-absent-optional", rules, f356Partition(total, 1, b1, c))
		obs := f3Observation(t, "f356-p1-absent-optional",
			b1Measure(t, total, "60"),
			b1Measure(t, b1, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: absent OPTIONAL member declares the schema zero, got %v; total=%s lines=%+v",
						seam.name, err, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "60/0" {
					t.Fatalf("%s: total=%s, want 60 (B only, optional C declares zero)", seam.name, total)
				}
			})
		}
	})

	// Control: a present but UNAVAILABLE required member is also not comparable
	// evidence, so it must never certify complete money either.
	t.Run("control_unavailable_required_c", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f356-p1-b-rate", b1, "1")}
		resolved := f356Schema(t, "f356-p1-unavailable", rules, graph)
		obs := f3Observation(t, "f356-p1-unavailable",
			b1Measure(t, total, "100"),
			b1Measure(t, b1, "60"),
			b1UnavailableMeasure(c))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f356AssertRejectedPartition(t, seam.name, val, err)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomplete for an unavailable required member; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
			})
		}
	})

	// Control: a PRICED parent bills its own observed quantity, so a missing
	// unpriced required child certifies nothing about that money and the
	// valuation stays complete. This is the deliberate boundary of the P1-1
	// classification: only children-only money can be a false complete.
	t.Run("control_priced_parent_with_missing_unpriced_child_stays_complete", func(t *testing.T) {
		t.Parallel()
		// A generic (non-informational) priced parent with an unpriced required
		// child C that the provider did not report: the parent's own line is the
		// authoritative charge.
		pricedTotal := f356ZKey()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p1p-parent-rate", pricedTotal, "1"),
			b1Rule(t, "f356-p1p-b-rate", b1, "1"),
		}
		resolved := f356Schema(t, "f356-p1-priced-parent", rules, f356Partition(pricedTotal, -1, b1, c))
		obs := f3Observation(t, "f356-p1-priced-parent",
			b1Measure(t, pricedTotal, "60"),
			b1Measure(t, b1, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: a priced parent bills its own quantity, so a missing unpriced child must not fail it closed; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
			})
		}
	})

	// Control: a later replacement that supplies C must produce a fresh
	// complete valuation without mutating the rejected evidence, proving the
	// missing member stays missing rather than being back-filled.
	t.Run("control_replacement_supplying_c_rates_complete", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p1-b-rate", b1, "1"),
			b1Rule(t, "f356-p1-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p1-replacement", rules, graph)
		partial := f3Observation(t, "f356-p1-replacement-1",
			b1Measure(t, total, "100"),
			b1Measure(t, b1, "60"))
		replacement := f3Observation(t, "f356-p1-replacement-2",
			b1Measure(t, total, "100"),
			b1Measure(t, b1, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				rejected, err := seam.rate(t, resolved, partial)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: incomplete partition err=%v, want ErrSchemaPartitionIncomplete; completeness=%q lines=%+v",
						seam.name, err, rejected.Completeness, rejected.Lines)
				}
				complete, err := seam.rate(t, resolved, replacement)
				if err != nil {
					t.Fatalf("%s: replacement supplying C must rate completely, got %v; lines=%+v", seam.name, err, complete.Lines)
				}
				if complete.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; lines=%+v", seam.name, complete.Completeness, complete.Lines)
				}
				if total := b1PayableTotal(t, complete); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (B 60 + C 40)", seam.name, total)
				}
				if rejected.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: the rejected valuation must stay partial, got %q", seam.name, rejected.Completeness)
				}
			})
		}
	})
}

// TestReviewF356TaintedAbsentParentFailsClosed is P1-2. B is shared between A
// and Z, so A's complete cover is ambiguous; A is absent and the collector
// refused it at entry, which used to leave B + C + D as a false complete
// additive USD120.
func TestReviewF356TaintedAbsentParentFailsClosed(t *testing.T) {
	t.Parallel()
	a, z := f356B2Key(), f356ZKey()
	sharedB := f356B1Key()
	c, d := f356CKey(), f356DKey()
	// A --partition--> {B, C}; Z --partition--> B; A --subset--> D.
	withSubset := f356Subset(
		append(f356Partition(a, -1, sharedB, c), f356Partition(z, -1, sharedB)...),
		a, d)
	withoutSubset := append(f356Partition(a, -1, sharedB, c), f356Partition(z, -1, sharedB)...)

	t.Run("primary_tainted_absent_parent_plus_priced_subset", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2-b-rate", sharedB, "1"),
			b1Rule(t, "f356-p2-c-rate", c, "1"),
			b1Rule(t, "f356-p2-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f356-p2-tainted-absent", rules, withSubset)
		obs := f3Observation(t, "f356-p2-tainted-absent",
			b1Measure(t, sharedB, "60"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f356AssertRejectedPartition(t, seam.name, val, err)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
					t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomparable (an unprovable shared-child cover is ambiguous, not arithmetically contradictory); completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial; total=%s lines=%+v",
						seam.name, val.Completeness, review583Total(val), val.Lines)
				}
				if count := review5beCountComponentLines(val, d); count != 0 {
					t.Fatalf("%s: the ambiguous subset descendant D must be withheld, got %d line(s); total=%s lines=%+v",
						seam.name, count, review583Total(val), val.Lines)
				}
			})
		}
	})

	// Control: a shared, present, complete, exactly-zero child plus a priced
	// subset must still not produce a silent complete additive result.
	t.Run("control_shared_zero_b_plus_priced_d", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2-c-rate", c, "1"),
			b1Rule(t, "f356-p2-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f356-p2-shared-zero", rules, withSubset)
		obs := f3Observation(t, "f356-p2-shared-zero",
			b1Measure(t, sharedB, "0"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f356AssertRejectedPartition(t, seam.name, val, err)
			})
		}
	})

	// Control: the same graph WITHOUT the priced subset D must not invent an
	// overlap. B and C stay independent payable money in a scope whose A is
	// unobserved, so no conflict may be raised.
	t.Run("control_tainted_parent_without_priced_subset", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2-b-rate", sharedB, "1"),
			b1Rule(t, "f356-p2-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p2-tainted-no-subset", rules, withoutSubset)
		obs := f3Observation(t, "f356-p2-tainted-no-subset",
			b1Measure(t, sharedB, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: no priced subset exists, so no overlap may be invented; err=%v lines=%+v", seam.name, err, val.Lines)
				}
			})
		}
	})

	// Control: an OBSERVED A with a shared B and a fully present, comparable,
	// conserved partition keeps the existing typed incomparable classification
	// (N2/5B-1 secondary pin) rather than a different sentinel.
	t.Run("control_observed_tainted_parent_stays_incomparable", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2-b-rate", sharedB, "1"),
			b1Rule(t, "f356-p2-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p2-observed-tainted", rules, withSubset)
		obs := f3Observation(t, "f356-p2-observed-tainted",
			b1Measure(t, a, "100"),
			b1Measure(t, sharedB, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
					t.Fatalf("%s: observed tainted parent must stay typed incomparable; err=%v completeness=%q lines=%+v",
						seam.name, err, val.Completeness, val.Lines)
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("%s: completeness=%q, want partial; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
			})
		}
	})

	// Control: an OBSERVED tainted parent whose partition is ALSO missing a
	// required member is incomplete evidence, not an arithmetic contradiction
	// and not merely ambiguous; both classifications are reported so no
	// diagnostic is lost.
	t.Run("control_observed_tainted_parent_missing_member", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f356-p2-b-rate", sharedB, "1")}
		// C is a declared REQUIRED member of A and is absent from the evidence.
		resolved := f356Schema(t, "f356-p2-observed-tainted-missing", rules, withoutSubset)
		obs := f3Observation(t, "f356-p2-observed-tainted-missing",
			b1Measure(t, a, "60"),
			b1Measure(t, sharedB, "60"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomplete for the absent required member; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("%s: an absent required member is not an arithmetic contradiction; err=%v", seam.name, err)
				}
			})
		}
	})

	// Control: an unrelated ambiguous fragment must not disable the
	// independent valid overlap rejection (the f356b681 locality fix).
	t.Run("control_unrelated_ambiguity_still_rejects", func(t *testing.T) {
		t.Parallel()
		valid := f356Subset(f356Partition(a, -1, sharedB, c), a, d)
		ambiguous := append(
			f356Partition(f356XKey(), -1, f356YKey()),
			f356Partition(d, -1, c)...)
		ambiguous = append(ambiguous, f356Partition(z, -1, c)...)
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2-b-rate", sharedB, "1"),
			b1Rule(t, "f356-p2-c-rate", c, "1"),
			b1Rule(t, "f356-p2-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f356-p2-unrelated-ambiguity", rules, append(valid, ambiguous...))
		obs := f3Observation(t, "f356-p2-unrelated-ambiguity",
			b1Measure(t, sharedB, "60"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: unrelated ambiguity must not hide the priced subset overlap; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessConflict {
					t.Fatalf("%s: completeness=%q, want conflict; lines=%+v", seam.name, val.Completeness, val.Lines)
				}
			})
		}
	})

	// Control: a TAINTED INTERMEDIATE inside an untainted ancestor must
	// propagate the ambiguity to the ancestor instead of silently disabling its
	// overlap analysis. P --partition--> {T, C}, T --partition--> {B, X},
	// Z --partition--> B (T and Z are tainted by the shared B), P --subset--> D.
	// P and T are absent, and B, X, C and D are priced.
	t.Run("control_tainted_intermediate_propagates_to_ancestor", func(t *testing.T) {
		t.Parallel()
		p := f356YKey()
		intermediate := f356XKey()
		inner := r7Key("vendor:f356_p2n_inner")
		relationships := f356Partition(p, -1, intermediate, c)
		relationships = append(relationships, f356Partition(intermediate, -1, sharedB, inner)...)
		relationships = append(relationships, f356Partition(z, -1, sharedB)...)
		relationships = f356Subset(relationships, p, d)
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p2n-b-rate", sharedB, "1"),
			b1Rule(t, "f356-p2n-inner-rate", inner, "1"),
			b1Rule(t, "f356-p2n-c-rate", c, "1"),
			b1Rule(t, "f356-p2n-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f356-p2-nested-taint", rules, relationships)
		obs := f3Observation(t, "f356-p2-nested-taint",
			b1Measure(t, sharedB, "60"),
			b1Measure(t, inner, "10"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f356AssertRejectedPartition(t, seam.name, val, err)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
					t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomparable; the tainted intermediate must propagate to the untainted ancestor P; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
			})
		}
	})

	// Declaration-order invariant: swapping the order in which the ambiguous
	// fragment is declared must not change the classification.
	t.Run("control_declaration_order_invariant", func(t *testing.T) {
		t.Parallel()
		for _, order := range []struct {
			name          string
			relationships []metering.ComponentRelationship
		}{
			{name: "shared_first", relationships: append(f356Partition(z, -1, sharedB), f356Partition(a, -1, sharedB, c)...)},
			{name: "a_first", relationships: append(f356Partition(a, -1, sharedB, c), f356Partition(z, -1, sharedB)...)},
		} {
			order := order
			t.Run(order.name, func(t *testing.T) {
				t.Parallel()
				rules := []economics.RatingRule{
					b1Rule(t, "f356-p2-b-rate", sharedB, "1"),
					b1Rule(t, "f356-p2-c-rate", c, "1"),
					b1Rule(t, "f356-p2-d-rate", d, "1"),
				}
				relationships := f356Subset(order.relationships, a, d)
				resolved := f356Schema(t, "f356-p2-order-"+order.name, rules, relationships)
				obs := f3Observation(t, "f356-p2-order-"+order.name,
					b1Measure(t, sharedB, "60"),
					b1Measure(t, c, "40"),
					b1Measure(t, d, "20"))
				for _, seam := range review5beSeams() {
					seam := seam
					t.Run(seam.name, func(t *testing.T) {
						t.Parallel()
						val, err := seam.rate(t, resolved, obs)
						f356AssertRejectedPartition(t, seam.name, val, err)
						if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
							t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomparable regardless of declaration order; completeness=%q total=%s lines=%+v",
								seam.name, err, val.Completeness, review583Total(val), val.Lines)
						}
					})
				}
			})
		}
	})
}

// TestReviewF356RecursiveCoverPropagatesToCompleteness is P2. The nested
// child-only tariff A -> {B, C}, B -> {X, Y} conserves exactly and the
// recursive overlap resolver already understands that X + Y pays for B, so the
// proven cover must also satisfy A's complete partition for rating
// completeness. Otherwise a valid nested schema that the schema validator
// accepts fails only after execution.
func TestReviewF356RecursiveCoverPropagatesToCompleteness(t *testing.T) {
	t.Parallel()
	a := f356B2Key()
	b, c := f356B1Key(), f356CKey()
	x, y := f356XKey(), f356YKey()
	graph := append(f356Partition(a, -1, b, c), f356Partition(b, -1, x, y)...)

	t.Run("primary_nested_child_only_conserved_complete_100", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p3-x-rate", x, "1"),
			b1Rule(t, "f356-p3-y-rate", y, "1"),
			b1Rule(t, "f356-p3-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p3-nested-cover", rules, graph)
		obs := f3Observation(t, "f356-p3-nested-cover",
			b1Measure(t, a, "100"),
			b1Measure(t, b, "60"),
			b1Measure(t, x, "30"),
			b1Measure(t, y, "30"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if err != nil {
					t.Fatalf("%s: nested conserved child-only partition must rate completely, got %v; completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness != economics.CompletenessComplete {
					t.Fatalf("%s: completeness=%q, want complete; total=%s lines=%+v",
						seam.name, val.Completeness, review583Total(val), val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "100/0" {
					t.Fatalf("%s: total=%s, want 100 (X 30 + Y 30 + C 40)", seam.name, total)
				}
				for _, child := range []metering.ComponentKey{x, y, c} {
					if count := review5beCountComponentLines(val, child); count != 1 {
						t.Fatalf("%s: component %s emitted %d times, want exactly once; lines=%+v",
							seam.name, child.Component, count, val.Lines)
					}
				}
				for _, covered := range []metering.ComponentKey{a, b} {
					if count := review5beCountComponentLines(val, covered); count != 0 {
						t.Fatalf("%s: recursively covered parent %s must not emit a missing-rate line, got %d; lines=%+v",
							seam.name, covered.Component, count, val.Lines)
					}
				}
			})
		}
	})

	// Control: a NESTED partition that does NOT conserve must stay partial, so
	// the recursion never certifies cover from a contradicted intermediate.
	t.Run("control_nested_partition_contradiction_stays_partial", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p3-x-rate", x, "1"),
			b1Rule(t, "f356-p3-y-rate", y, "1"),
			b1Rule(t, "f356-p3-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p3-nested-contradiction", rules, graph)
		obs := f3Observation(t, "f356-p3-nested-contradiction",
			b1Measure(t, a, "100"),
			b1Measure(t, b, "60"),
			b1Measure(t, x, "30"),
			b1Measure(t, y, "60"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("%s: nested 60 != 30 + 60 must stay a typed contradiction; err=%v completeness=%q total=%s lines=%+v",
						seam.name, err, val.Completeness, review583Total(val), val.Lines)
				}
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("%s: contradicted nested partition must never settle as complete; lines=%+v", seam.name, val.Lines)
				}
			})
		}
	})

	// Control: a nested intermediate whose own REQUIRED member is absent must
	// not certify the ancestor, so the recursion still fails closed on missing
	// evidence.
	t.Run("control_nested_missing_required_stays_partial", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f356-p3-x-rate", x, "1"),
			b1Rule(t, "f356-p3-c-rate", c, "1"),
		}
		resolved := f356Schema(t, "f356-p3-nested-missing", rules, graph)
		obs := f3Observation(t, "f356-p3-nested-missing",
			b1Measure(t, a, "70"),
			b1Measure(t, b, "30"),
			b1Measure(t, x, "30"),
			b1Measure(t, c, "40"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("%s: a nested partition missing a REQUIRED member must not certify the ancestor; err=%v total=%s lines=%+v",
						seam.name, err, review583Total(val), val.Lines)
				}
			})
		}
	})
}
