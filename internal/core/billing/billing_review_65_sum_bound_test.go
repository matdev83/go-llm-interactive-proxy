package billing_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Review 65 P1: a quantity bound is a constraint on the COMPLETE represented
// quantity, not on each edge in isolation.
//
// A --subset-> B, B --partition-> {C, D}, with A=30 explicit free, B absent,
// C=20 paid and D=20 paid. C and D are each individually inside A, so a
// per-edge bound accepts both. But B's complete coverage is a sum, so the
// represented B is 40, and 40 > 30 contradicts the containment A >= B.
//
// The second vector adds a REQUIRED member E that was never observed. B is not
// exactly representable any more, so there is no exact B to compare. The KNOWN
// LOWER BOUND is still C + D = 40 > 30, which is already contradictory evidence:
// the true value is at least 40, so it cannot be <= 30. Treating this as merely
// unknown would be the error.

func f65Key(role string) metering.ComponentKey { return r7Key("vendor:f65_" + role) }

// f65ExpectContradiction is the shared assertion for both vectors: the valuation
// must not be certified complete, and the impossible geometry must surface as a
// typed quantity contradiction rather than as an unrelated missing-operand
// complaint.
func f65ExpectContradiction(t *testing.T, seam review5beSeam, val economics.Valuation, err error) {
	t.Helper()
	if val.Completeness == economics.CompletenessComplete {
		t.Fatalf("%s: a complete-cover sum exceeding its subset ancestor must never certify complete; err=%v total=%s lines=%+v",
			seam.name, err, b1PayableTotal(t, val), val.Lines)
	}
	if err == nil {
		t.Fatalf("%s: completeness=%q with no diagnostic is an untyped contradiction", seam.name, val.Completeness)
	}
}

// TestReview65CompleteSumExceedingAncestorIsRejected is the known baseline
// blocker at 26c8521a: the individual-edge bound passes while the represented
// sum does not.
func TestReview65CompleteSumExceedingAncestorIsRejected(t *testing.T) {
	t.Parallel()
	a, b, c, d := f65Key("a"), f65Key("b"), f65Key("c"), f65Key("d")
	relationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipSubset, Parent: a, Child: b},
		{Kind: metering.RelationshipPartition, Parent: b, Child: c},
		{Kind: metering.RelationshipPartition, Parent: b, Child: d},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "f65-ancestor", a, "0"),
		b1Rule(t, "f65-c", c, "1"),
		b1Rule(t, "f65-d", d, "1"),
	}
	resolved := f356Schema(t, "f65-sum-exceeds-ancestor", rules, relationships)
	obs := f3Observation(t, "f65-sum-exceeds-ancestor",
		b1Measure(t, a, "30"),
		b1Measure(t, c, "20"),
		b1Measure(t, d, "20"))
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			seam := seam
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			f65ExpectContradiction(t, seam, val, err)
		})
	}
}

// TestReview65KnownLowerBoundExceedingAncestorIsRejected is the stronger form:
// one further REQUIRED member is missing, so no exact B exists, but the known
// lower bound already exceeds the ancestor and the evidence is contradictory.
func TestReview65KnownLowerBoundExceedingAncestorIsRejected(t *testing.T) {
	t.Parallel()
	a, b, c, d, e := f65Key("la"), f65Key("lb"), f65Key("lc"), f65Key("ld"), f65Key("le")
	relationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipSubset, Parent: a, Child: b},
		{Kind: metering.RelationshipPartition, Parent: b, Child: c},
		{Kind: metering.RelationshipPartition, Parent: b, Child: d},
		{Kind: metering.RelationshipPartition, Parent: b, Child: e},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "f65-lb-ancestor", a, "0"),
		b1Rule(t, "f65-lb-c", c, "1"),
		b1Rule(t, "f65-lb-d", d, "1"),
		b1Rule(t, "f65-lb-e", e, "1"),
	}
	resolved := f356Schema(t, "f65-lower-bound", rules, relationships)
	obs := f3Observation(t, "f65-lower-bound",
		b1Measure(t, a, "30"),
		b1Measure(t, c, "20"),
		b1Measure(t, d, "20"))
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			seam := seam
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if val.Completeness == economics.CompletenessComplete {
				t.Fatalf("%s: a known lower bound above the ancestor must never certify complete; err=%v total=%s",
					seam.name, err, b1PayableTotal(t, val))
			}
			if err == nil {
				t.Fatalf("%s: no diagnostic for contradictory evidence", seam.name)
			}
		})
	}
}
