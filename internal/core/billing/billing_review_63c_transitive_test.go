package billing_test

// 63c (PR #666 adversarial re-review at head 866edb6e, TEST-ONLY baseline).
//
// Both findings in this round share one root cause: DIRECT-only logic inside a
// state machine whose monetary overlap rules already treat inclusion
// transitively. Containment and cover coverage are both transitive properties of
// the frozen inclusion graph, so a check that only inspects a node's immediate
// neighbours is not sound.
//
// P1-1: commercial relevance of an UNPROVABLE complete cover was decided by
// looking only at the parent's DIRECT declared partition children for positive
// payable money. But the money on the partition side can be represented several
// levels down: A --partition--> {B, C}, B --partition--> {X, Y}, A --subset--> D,
// with A, B and C absent and X and D present and priced. B is absent, so the
// direct check finds no payable partition money, the fail-closed branch is
// skipped, and X = 60 plus D = 20 settles as a false complete USD80 even though
// A's cover is unprovable and the schema never proved X disjoint from D.
//
// P1-2: subset quantity consistency was direct-only while subset containment is
// transitive. A --subset--> B, B --subset--> C with A = 10, B absent and C = 20
// never compared A against C, because the direct A->B edge has no comparable B
// quantity and the direct B->C edge has no comparable B quantity either. C is
// nevertheless contained in A, so 20 > 10 is inconsistent evidence and the shape
// settled as a false complete USD20.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// f63aKey, f63bKey, f63cKey, f63dKey, f63xKey and f63yKey are the nodes of the
// P1-1 graph; f63aKey/f63bKey/f63cKey are also the P1-2 subset chain.
func f63aKey() metering.ComponentKey { return r7Key("vendor:f63_a") }
func f63bKey() metering.ComponentKey { return r7Key("vendor:f63_b") }
func f63cKey() metering.ComponentKey { return r7Key("vendor:f63_c") }
func f63dKey() metering.ComponentKey { return r7Key("vendor:f63_d") }
func f63xKey() metering.ComponentKey { return r7Key("vendor:f63_x") }
func f63yKey() metering.ComponentKey { return r7Key("vendor:f63_y") }

// f63Total prints the payable total without failing when the valuation was
// correctly rejected, so a RED assertion can print the pre-fix false amount.
func f63Total(val economics.Valuation) string {
	if len(val.Totals) == 0 || val.Totals[0].Amount == nil {
		return "<none>"
	}
	return val.Totals[0].Amount.CanonicalString()
}

// f63Reject asserts a valuation certified no complete money and emitted a typed
// diagnostic.
func f63Reject(t *testing.T, seam string, val economics.Valuation, err error, want error) {
	t.Helper()
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("%s: must never certify complete money here; err=%v total=%s lines=%+v",
			seam, err, f63Total(val), val.Lines)
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s: err=%v, want %v; completeness=%q total=%s lines=%+v",
			seam, err, want, val.Completeness, f63Total(val), val.Lines)
	}
}

// TestReview63cNestedPayableDescendantKeepsUnprovableCoverDiagnosed is P1-1.
func TestReview63cNestedPayableDescendantKeepsUnprovableCoverDiagnosed(t *testing.T) {
	t.Parallel()
	a, b, c, d := f63aKey(), f63bKey(), f63cKey(), f63dKey()
	x, y := f63xKey(), f63yKey()
	// A --partition--> {B, C}; B --partition--> {X, Y}; A --subset--> D.
	graph := append(f356Partition(a, -1, b, c), f356Partition(b, -1, x, y)...)
	graph = f356Subset(graph, a, d)

	t.Run("primary_nested_payable_x_plus_payable_subset_d", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63c-x-rate", x, "1"),
			b1Rule(t, "f63c-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f63c-nested-descendant", rules, graph)
		obs := f3Observation(t, "f63c-nested-descendant",
			b1Measure(t, x, "60"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				f63Reject(t, seam.name, val, err, billing.ErrSchemaPartitionIncomplete)
				if count := review5beCountComponentLines(val, d); count != 0 {
					t.Fatalf("%s: the unplaceable subset descendant D must be withheld, got %d line(s); total=%s lines=%+v",
						seam.name, count, f63Total(val), val.Lines)
				}
			})
		}
	})

	// Control: three nesting levels. Money represented two complete-partition
	// hops below A must be recognized just the same.
	t.Run("control_three_nesting_levels", func(t *testing.T) {
		t.Parallel()
		z := r7Key("vendor:f63_z")
		deep := append(append(f356Partition(a, -1, b, c), f356Partition(b, -1, x, y)...), f356Partition(y, -1, z)...)
		deep = f356Subset(deep, a, d)
		rules := []economics.RatingRule{
			b1Rule(t, "f63c3-z-rate", z, "1"),
			b1Rule(t, "f63c3-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f63c-three-levels", rules, deep)
		obs := f3Observation(t, "f63c-three-levels",
			b1Measure(t, z, "60"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				f63RejectOne(t, seam, resolved, obs, billing.ErrSchemaPartitionIncomplete)
			})
		}
	})

	// Control: a PROVEN nested cover (B accounted for by X and the supplied Y)
	// plus the supplied required member C makes the partition complete, so the
	// ordinary overlap rule owns the shape and D is a genuine extra payable
	// descendant rather than an unplaceable one.
	t.Run("control_proven_nested_cover_uses_normal_overlap", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63c-c-rate", c, "1"),
			b1Rule(t, "f63c-x-rate", x, "1"),
			b1Rule(t, "f63c-y-rate", y, "1"),
			b1Rule(t, "f63c-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f63c-proven-cover", rules, graph)
		obs := f3Observation(t, "f63c-proven-cover",
			b1Measure(t, x, "30"),
			b1Measure(t, y, "30"),
			b1Measure(t, c, "40"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: a proven complete cover must hand the shape to the typed overlap contract; err=%v completeness=%q total=%s",
						seam.name, err, val.Completeness, f63Total(val))
				}
				b1AssertNoPayableLines(t, val)
			})
		}
	})

	// Control: with no payable partition-side money at all there is nothing in
	// this scope for D to double-charge, so the subset stays payable. This is
	// the same per-scope isolation the cross-scope suite pins.
	t.Run("control_no_payable_partition_descendant_keeps_subset_payable", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "f63c-d-rate", d, "1")}
		resolved := f356Schema(t, "f63c-subset-only", rules, graph)
		obs := f3Observation(t, "f63c-subset-only", b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: with no payable partition-side money the subset must stay payable; err=%v lines=%+v", seam.name, err, val.Lines)
				}
				if total := b1PayableTotal(t, val); total != "20/0" {
					t.Fatalf("%s: total=%s, want 20 (D only)", seam.name, total)
				}
			})
		}
	})

	// Control: cross-scope isolation. The nested partition money X sits in one
	// B-leg scope and the payable subset D in another, so the scope holding D has
	// no payable partition descendant and the two scopes stay additive.
	t.Run("control_cross_scope_isolation", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63c-x-rate", x, "1"),
			b1Rule(t, "f63c-d-rate", d, "1"),
		}
		resolved := f356Schema(t, "f63c-cross-scope", rules, graph)
		obsPartition := b1Observation(t, "f63c-cross-scope-partition", "b-leg-partition", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, x, "60"))
		obsSubset := b1Observation(t, "f63c-cross-scope-subset", "b-leg-subset", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, d, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obsPartition, obsSubset}))
		if errors.Is(rateErr, billing.ErrSchemaPartitionIncomplete) {
			t.Fatalf("per-scope isolation must hold: the subset scope has no payable partition descendant; err=%v lines=%+v", rateErr, val.Lines)
		}
		if total := b1PayableTotal(t, val); total != "80/0" {
			t.Fatalf("independent scopes total=%s, want 80 (X 60 + D 20)", total)
		}
	})

	// Control: declaration order must not change the classification.
	t.Run("control_declaration_order_invariant", func(t *testing.T) {
		t.Parallel()
		for _, order := range []struct {
			name  string
			graph []metering.ComponentRelationship
		}{
			{
				name: "partitions_first",
				graph: f356Subset(
					append(f356Partition(a, -1, b, c), f356Partition(b, -1, x, y)...), a, d),
			},
			{
				name: "subset_first",
				graph: f356Subset(
					append(f356Partition(b, -1, x, y), f356Partition(a, -1, b, c)...), a, d),
			},
		} {
			order := order
			t.Run(order.name, func(t *testing.T) {
				t.Parallel()
				rules := []economics.RatingRule{
					b1Rule(t, "f63c-x-rate", x, "1"),
					b1Rule(t, "f63c-d-rate", d, "1"),
				}
				resolved := f356Schema(t, "f63c-order-"+order.name, rules, order.graph)
				obs := f3Observation(t, "f63c-order-"+order.name,
					b1Measure(t, x, "60"),
					b1Measure(t, d, "20"))
				for _, seam := range review5beSeams() {
					seam := seam
					t.Run(seam.name, func(t *testing.T) {
						seam := seam
						t.Parallel()
						f63RejectOne(t, seam, resolved, obs, billing.ErrSchemaPartitionIncomplete)
					})
				}
			})
		}
	})
}

// TestReview63cTransitiveSubsetQuantityConsistency is P1-2. Quantity
// containment must agree with the transitive inclusion semantics the monetary
// overlap rules already use: for comparable same-scope ancestor and descendant
// quantities over a subset chain, enforce descendant <= ancestor.
func TestReview63cTransitiveSubsetQuantityConsistency(t *testing.T) {
	t.Parallel()
	a, b, c := f63aKey(), f63bKey(), f63cKey()
	// A --subset--> B --subset--> C, with B absent so neither direct edge is
	// comparable on its own.
	chain := append(f62Subset(a, b), f62Subset(b, c)...)

	type vector struct {
		name           string
		ancestor       string
		descendant     string
		ancestorPrice  string
		wantContradict bool
		wantTotal      string
	}
	vectors := []vector{
		{name: "ten_to_twenty_over_two_levels", ancestor: "10", descendant: "20", ancestorPrice: "0", wantContradict: true},
		{name: "twenty_to_twenty_over_two_levels", ancestor: "20", descendant: "20", ancestorPrice: "0", wantTotal: "20/0"},
		{name: "hundred_to_twenty_over_two_levels", ancestor: "100", descendant: "20", ancestorPrice: "0", wantTotal: "20/0"},
		// A POSITIVE priced ancestor plus a positive priced descendant is owned by
		// the pre-existing payable-overlap contract, and the consistent
		// quantities must not add a subset contradiction on top of it.
		{name: "twenty_to_twenty_both_priced", ancestor: "20", descendant: "20", ancestorPrice: "1", wantTotal: ""},
	}
	for _, testCase := range vectors {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rules := []economics.RatingRule{
				b1Rule(t, "f63t-ancestor", a, testCase.ancestorPrice),
				b1Rule(t, "f63t-descendant", c, "1"),
			}
			resolved := f356Schema(t, "f63t-"+testCase.name, rules, chain)
			obs := f3Observation(t, "f63t-"+testCase.name,
				b1Measure(t, a, testCase.ancestor),
				b1Measure(t, c, testCase.descendant))
			for _, seam := range review5beSeams() {
				seam := seam
				t.Run(seam.name, func(t *testing.T) {
					seam := seam
					t.Parallel()
					val, err := seam.rate(t, resolved, obs)
					if testCase.wantContradict {
						f63Reject(t, seam.name, val, err, billing.ErrSchemaSubsetContradiction)
						return
					}
					if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
						t.Fatalf("%s: consistent ancestor/descendant quantities must never report a subset contradiction; err=%v", seam.name, err)
					}
					if testCase.wantTotal == "" {
						// Owned by the payable-overlap contract instead.
						if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
							t.Fatalf("%s: a priced ancestor plus a priced descendant must stay the typed overlap conflict; err=%v completeness=%q",
								seam.name, err, val.Completeness)
						}
						return
					}
					if err != nil {
						t.Fatalf("%s: err=%v, want no error; completeness=%q total=%s lines=%+v",
							seam.name, err, val.Completeness, f63Total(val), val.Lines)
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

	// Control: four subset levels. Containment is transitive to any depth.
	t.Run("control_four_subset_levels", func(t *testing.T) {
		t.Parallel()
		d := f63dKey()
		deep := append(append(f62Subset(a, b), f62Subset(b, c)...), f62Subset(c, d)...)
		rules := []economics.RatingRule{
			b1Rule(t, "f63t4-ancestor", a, "0"),
			b1Rule(t, "f63t4-descendant", d, "1"),
		}
		resolved := f356Schema(t, "f63t-four-levels", rules, deep)
		obs := f3Observation(t, "f63t-four-levels",
			b1Measure(t, a, "10"),
			b1Measure(t, d, "30"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				f63RejectOne(t, seam, resolved, obs, billing.ErrSchemaSubsetContradiction)
			})
		}
	})

	// Control: a zero ancestor still validates quantities. The check is a
	// quantity fact and must not key off the ancestor's effective charge.
	t.Run("control_zero_ancestor_still_validates", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63tz-ancestor", a, "0"),
			b1Rule(t, "f63tz-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f63t-zero-ancestor", rules, chain)
		obs := f3Observation(t, "f63t-zero-ancestor",
			b1Measure(t, a, "0"),
			b1Measure(t, c, "5"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				f63RejectOne(t, seam, resolved, obs, billing.ErrSchemaSubsetContradiction)
			})
		}
	})

	// Control: missing or incomplete endpoints stay missing. An absent ancestor
	// has no comparable quantity, so nothing may be invented for it.
	t.Run("control_absent_ancestor_stays_missing", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63tm-ancestor", a, "0"),
			b1Rule(t, "f63tm-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f63t-absent-ancestor", rules, chain)
		obs := f3Observation(t, "f63t-absent-ancestor", b1Measure(t, c, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				if _, err := seam.rate(t, resolved, obs); errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: an absent ancestor quantity must stay missing, not be invented as a zero; err=%v", seam.name, err)
				}
			})
		}
	})

	// Control: cross-scope and cross-direction subset chains stay isolated. The
	// ancestor is in another scope, and a same-named component in another
	// direction is a different quantity.
	t.Run("control_cross_scope_and_direction_isolated", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f63tc-ancestor", a, "0"),
			b1Rule(t, "f63tc-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f63t-cross-scope", rules, chain)
		obsAncestor := b1Observation(t, "f63t-cross-scope-ancestor", "b-leg-ancestor", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"))
		obsDescendant := b1Observation(t, "f63t-cross-scope-descendant", "b-leg-descendant", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, c, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obsAncestor, obsDescendant}))
		if errors.Is(rateErr, billing.ErrSchemaSubsetContradiction) {
			t.Fatalf("per-scope subset arithmetic must not reach across scopes; err=%v lines=%+v", rateErr, val.Lines)
		}
	})
}

// f63RejectOne drives one rating seam and applies the shared fail-closed
// assertion, so a control can be stated in one line.
func f63RejectOne(t *testing.T, seam review5beSeam, resolved economics.TariffSnapshot, obs metering.Observation, want error) {
	t.Helper()
	val, err := seam.rate(t, resolved, obs)
	f63Reject(t, seam.name, val, err, want)
}
