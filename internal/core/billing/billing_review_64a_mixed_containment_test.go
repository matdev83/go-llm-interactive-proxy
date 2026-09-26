package billing_test

// 64a (PR #666 adversarial re-review at head 8d2ac7c8, TEST-ONLY baseline).
//
// One root cause: the state machine had TWO definitions of containment.
//
//  1. Monetary overlap treats aggregate + partition + subset all as transitive
//     inclusion edges.
//  2. Quantity consistency followed only pure subset -> subset chains.
//
// Those disagree, so a quantity check built on the narrower definition is blind
// to any chain that passes through a complete-coverage edge. The invariant is
// that ANY non-transform inclusion edge means the child is contained in the
// parent; aggregate and partition edges additionally assert conservation. So
// quantity containment must use the merged view, while cover-specific logic
// keeps the two classes apart.
//
// Minimal valid counterexample: A --subset--> B, B --partition--> C, with
// A = 10 present/complete/explicit-free, B absent, and C = 20 present/complete and
// priced at $1/unit. C is contained in B and B is contained in A, so the
// comparable endpoints imply C <= A, yet 20 > 10 is inconsistent evidence. The
// direct A->B edge cannot be compared because B has no quantity, and the
// pure-subset walk never reaches C because B->C is a partition edge, so the
// shape settled as a false complete USD20 with A charged nothing.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// f64aKey, f64bKey and f64cKey are the chain endpoints; f64dKey extends the chain
// to three mixed levels.
func f64aKey() metering.ComponentKey { return r7Key("vendor:f64a_a") }
func f64bKey() metering.ComponentKey { return r7Key("vendor:f64a_b") }
func f64cKey() metering.ComponentKey { return r7Key("vendor:f64a_c") }
func f64dKey() metering.ComponentKey { return r7Key("vendor:f64a_d") }

// f64RejectOne drives one rating seam and applies the shared fail-closed
// assertion, so a case states its whole expectation in one line.
func f64RejectOne(t *testing.T, seam review5beSeam, resolved economics.TariffSnapshot, obs metering.Observation) {
	t.Helper()
	val, err := seam.rate(t, resolved, obs)
	f64Reject(t, seam.name, val, err)
}

// f64Total prints the payable total without failing when the valuation was
// correctly rejected, so a RED assertion can print the pre-fix false amount.
func f64Total(val economics.Valuation) string {
	if len(val.Totals) == 0 || val.Totals[0].Amount == nil {
		return "<none>"
	}
	return val.Totals[0].Amount.CanonicalString()
}

// f64Reject asserts a valuation certified no complete money and emitted the
// containment sentinel. It takes the seam result directly so a case states its
// whole assertion in one line.
func f64Reject(t *testing.T, seam string, val economics.Valuation, err error) {
	t.Helper()
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("%s: must never certify complete money from a mixed containment chain; err=%v total=%s lines=%+v",
			seam, err, f64Total(val), val.Lines)
	}
	if !errors.Is(err, billing.ErrSchemaSubsetContradiction) {
		t.Fatalf("%s: err=%v, want ErrSchemaSubsetContradiction; completeness=%q total=%s lines=%+v",
			seam, err, val.Completeness, f64Total(val), val.Lines)
	}
}

// f64ChainDecl declares the two-level mixed chain A -> B -> C, where the
// A -> B edge kind is supplied so the partition and aggregate variants share one
// shape.
func f64ChainDecl(firstKind metering.RelationshipKind) []metering.ComponentRelationship {
	return []metering.ComponentRelationship{
		{Kind: firstKind, Parent: f64aKey(), Child: f64bKey()},
		{Kind: metering.RelationshipPartition, Parent: f64bKey(), Child: f64cKey()},
	}
}

// TestReview64aMixedContainmentChainQuantityConsistency is the reported
// counterexample plus its required controls.
func TestReview64aMixedContainmentChainQuantityConsistency(t *testing.T) {
	t.Parallel()
	a, c := f64aKey(), f64cKey()

	type vector struct {
		name           string
		firstKind      metering.RelationshipKind
		ancestor       string
		descendant     string
		ancestorPrice  string
		wantContradict bool
		wantPartition  bool
		wantTotal      string
	}
	vectors := []vector{
		// 1. The reviewed counterexample: the ancestor declares only a PARTIAL
		// containment edge, so the complete-coverage proof has nothing to say about
		// it and the merged containment walk is the only thing that can bound C.
		{
			name: "subset_then_partition_ten_to_twenty", firstKind: metering.RelationshipSubset,
			ancestor: "10", descendant: "20", ancestorPrice: "0", wantContradict: true,
		},
		// 2. The same chain with RelationshipAggregate instead of subset. The
		// ancestor now declares COMPLETE coverage, and its member B was never
		// observed, so the complete-partition proof classifies it first. That
		// diagnosis is built from the declared structure and stays authoritative;
		// the containment walk must stay silent instead of adding a second error
		// for the same root cause.
		{
			name: "aggregate_then_partition_ten_to_twenty", firstKind: metering.RelationshipAggregate,
			ancestor: "10", descendant: "20", ancestorPrice: "0", wantPartition: true,
		},
		// 3. Equal quantities are the whole-container boundary and are valid.
		{
			name: "equal_quantities_valid", firstKind: metering.RelationshipSubset,
			ancestor: "20", descendant: "20", ancestorPrice: "0", wantTotal: "20/0",
		},
		// 4. A strict descendant is valid evidence.
		{
			name: "subset_strictly_inside_valid", firstKind: metering.RelationshipSubset,
			ancestor: "100", descendant: "20", ancestorPrice: "0", wantTotal: "20/0",
		},
	}
	for _, testCase := range vectors {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rules := []economics.RatingRule{
				b1Rule(t, "f64a-ancestor", a, testCase.ancestorPrice),
				b1Rule(t, "f64a-descendant", c, "1"),
			}
			resolved := f356Schema(t, "f64a-"+testCase.name, rules, f64ChainDecl(testCase.firstKind))
			obs := f3Observation(t, "f64a-"+testCase.name,
				b1Measure(t, a, testCase.ancestor),
				b1Measure(t, c, testCase.descendant))
			for _, seam := range review5beSeams() {
				seam := seam
				t.Run(seam.name, func(t *testing.T) {
					seam := seam
					t.Parallel()
					val, err := seam.rate(t, resolved, obs)
					if testCase.wantContradict {
						f64Reject(t, seam.name, val, err)
						return
					}
					if testCase.wantPartition {
						if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
							t.Fatalf("%s: err=%v, want ErrSchemaPartitionIncomplete", seam.name, err)
						}
						if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
							t.Fatalf("%s: the partition diagnosis owns this shape, so no duplicate containment diagnosis may be added; err=%v",
								seam.name, err)
						}
						return
					}
					if err != nil {
						t.Fatalf("%s: err=%v, want no error; completeness=%q total=%s lines=%+v",
							seam.name, err, val.Completeness, f64Total(val), val.Lines)
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

	// 5. Three mixed levels: A --subset--> B, B --partition--> C, C --subset--> D.
	// Containment is transitive across the class change, so D is bounded by A.
	t.Run("three_mixed_levels", func(t *testing.T) {
		t.Parallel()
		d := f64dKey()
		relationships := append(f64ChainDecl(metering.RelationshipSubset), metering.ComponentRelationship{
			Kind: metering.RelationshipSubset, Parent: f64cKey(), Child: d,
		})
		rules := []economics.RatingRule{
			b1Rule(t, "f64a5-ancestor", a, "0"),
			b1Rule(t, "f64a5-descendant", d, "1"),
		}
		resolved := f356Schema(t, "f64a-three-levels", rules, relationships)
		obs := f3Observation(t, "f64a-three-levels",
			b1Measure(t, a, "10"),
			b1Measure(t, d, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				f64RejectOne(t, seam, resolved, obs)
			})
		}
	})

	// 8. A transform edge breaks containment traversal. Transform is a separately
	// governed unit derivation and is not containment, so the chain must stop
	// there and the shape must stay additive.
	t.Run("transform_edge_breaks_containment", func(t *testing.T) {
		t.Parallel()
		relationships := []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: a, Child: f64bKey()},
			{Kind: metering.RelationshipTransform, Parent: f64bKey(), Child: c},
		}
		rules := []economics.RatingRule{
			b1Rule(t, "f64a8-ancestor", a, "0"),
			b1Rule(t, "f64a8-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f64a-transform", rules, relationships)
		obs := f3Observation(t, "f64a-transform",
			b1Measure(t, a, "10"),
			b1Measure(t, c, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				if _, err := seam.rate(t, resolved, obs); errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: a transform edge is a unit derivation, not containment, and must not be traversed; err=%v", seam.name, err)
				}
			})
		}
	})

	// 7. Cross-scope isolation: the ancestor sits in another B-leg scope, so the
	// chain never becomes comparable and nothing may be invented for it.
	t.Run("cross_scope_isolated", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f64a7-ancestor", a, "0"),
			b1Rule(t, "f64a7-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f64a-cross-scope", rules, f64ChainDecl(metering.RelationshipSubset))
		obsAncestor := b1Observation(t, "f64a-cross-scope-ancestor", "b-leg-ancestor", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, a, "10"))
		obsDescendant := b1Observation(t, "f64a-cross-scope-descendant", "b-leg-descendant", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, c, "20"))
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obsAncestor, obsDescendant}))
		if errors.Is(rateErr, billing.ErrSchemaSubsetContradiction) {
			t.Fatalf("per-scope containment arithmetic must not reach across scopes; err=%v lines=%+v", rateErr, val.Lines)
		}
	})

	// 7. Cross-direction isolation: the same component name in another direction
	// is a different quantity, so the chain must not apply.
	t.Run("cross_direction_isolated", func(t *testing.T) {
		t.Parallel()
		mirrored := metering.ComponentKey{
			Direction: metering.DirectionOutput,
			Component: f64cKey().Component,
			Unit:      f64cKey().Unit,
			SchemaID:  f64cKey().SchemaID,
		}
		relationships := []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: a, Child: f64bKey()},
			{Kind: metering.RelationshipPartition, Parent: f64bKey(), Child: mirrored},
		}
		rules := []economics.RatingRule{
			b1Rule(t, "f64a7d-ancestor", a, "0"),
			b1Rule(t, "f64a7d-descendant", mirrored, "1"),
		}
		resolved := f356Schema(t, "f64a-cross-direction", rules, relationships)
		obs := f3Observation(t, "f64a-cross-direction",
			b1Measure(t, a, "10"),
			b1Measure(t, mirrored, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				if _, err := seam.rate(t, resolved, obs); errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: cross-direction quantities must not be compared; err=%v", seam.name, err)
				}
			})
		}
	})

	// 9. Declaration-order invariant: the same declared graph in either
	// declaration order must classify identically.
	t.Run("declaration_order_invariant", func(t *testing.T) {
		t.Parallel()
		forward := f64ChainDecl(metering.RelationshipSubset)
		reverse := []metering.ComponentRelationship{forward[1], forward[0]}
		for _, order := range []struct {
			name          string
			relationships []metering.ComponentRelationship
		}{{name: "forward", relationships: forward}, {name: "reverse", relationships: reverse}} {
			order := order
			t.Run(order.name, func(t *testing.T) {
				t.Parallel()
				rules := []economics.RatingRule{
					b1Rule(t, "f64a9-ancestor", a, "0"),
					b1Rule(t, "f64a9-descendant", c, "1"),
				}
				resolved := f356Schema(t, "f64a-order-"+order.name, rules, order.relationships)
				obs := f3Observation(t, "f64a-order-"+order.name,
					b1Measure(t, a, "10"),
					b1Measure(t, c, "20"))
				for _, seam := range review5beSeams() {
					seam := seam
					t.Run(seam.name, func(t *testing.T) {
						seam := seam
						t.Parallel()
						f64RejectOne(t, seam, resolved, obs)
					})
				}
			})
		}
	})
}

// TestReview64aDirectPartitionDiagnosisIsNotDuplicated is requirement 6: the
// reverse mixed chain A --partition--> B, B --subset--> C with an OBSERVED A and
// a MISSING B must keep the partition proof's own, more specific diagnosis and
// must not also report a containment contradiction for the same root cause.
//
// Two variants are pinned because the dedup must key off the partition proof's
// effective classification, which itself depends on whether the parent bills
// money:
//
//   - A charges nothing, so the missing REQUIRED member is the authoritative
//     diagnosis and the containment report is suppressed as a duplicate.
//   - A charges a positive amount, so the partition classification is not
//     commercially load-bearing and the genuine C > A contradiction must still
//     surface.
func TestReview64aDirectPartitionDiagnosisIsNotDuplicated(t *testing.T) {
	t.Parallel()
	a, b, c := f64aKey(), f64bKey(), f64cKey()
	graph := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: a, Child: b},
		{Kind: metering.RelationshipSubset, Parent: b, Child: c},
	}

	t.Run("zero_charge_ancestor_keeps_only_the_partition_incomplete", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f64a6-ancestor", a, "0"),
			b1Rule(t, "f64a6-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f64a-dedup-free", rules, graph)
		obs := f3Observation(t, "f64a-dedup-free",
			b1Measure(t, a, "10"),
			b1Measure(t, c, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
					t.Fatalf("%s: the missing REQUIRED member must keep the partition-incomplete diagnosis; err=%v completeness=%q total=%s",
						seam.name, err, val.Completeness, f64Total(val))
				}
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: the partition proof already owns this root cause, so no conflicting containment diagnosis may be added; err=%v",
						seam.name, err)
				}
			})
		}
	})

	// A POSITIVE charged ancestor makes both sides payable, and the pre-existing
	// TRANSITIVE payable overlap already rejects the shape as a typed overlap
	// conflict. That existing behaviour is the authoritative classification, so
	// the containment check must not add a second, differently-worded diagnosis of
	// the same money. This is the duplicate-suppression half of the invariant.
	t.Run("charging_ancestor_keeps_only_the_existing_overlap", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "f64a6b-ancestor", a, "1"),
			b1Rule(t, "f64a6b-descendant", c, "1"),
		}
		resolved := f356Schema(t, "f64a-dedup-charging", rules, graph)
		obs := f3Observation(t, "f64a-dedup-charging",
			b1Measure(t, a, "10"),
			b1Measure(t, c, "20"))
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				seam := seam
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
					t.Fatalf("%s: the existing transitive payable overlap must keep owning this shape; err=%v completeness=%q",
						seam.name, err, val.Completeness)
				}
				if errors.Is(err, billing.ErrSchemaSubsetContradiction) {
					t.Fatalf("%s: the overlap diagnosis is authoritative, so no duplicate containment diagnosis may be added; err=%v",
						seam.name, err)
				}
			})
		}
	})
}
