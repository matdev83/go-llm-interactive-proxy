package billing_test

// Adversarial settlement of the PR #659 review claim that a TAINTED declared
// child partition escapes classification when a PRESENT child has no resolving
// rule, so that adding an unrelated shared-child declaration turns a
// fail-closed partition into a complete customer charge.
//
// The vector is exactly the reviewer's:
//   schema (a) A --partition--> {B, S}            (both members REQUIRED)
//   schema (b) A --partition--> {B, S} AND D --partition--> {S}
//            -> S is shared, so A (and D) are ambiguous owners and A is tainted
//   rules    ONLY S is priced (USD5)               -> A and B have no rule
//   obs      A=0, B=0, S=30                        -> 0 != 0 + 30, NOT conserved
//
// The reviewer claimed (b) settles COMPLETE at 150/0 where (a) fails closed with
// ErrSchemaPartitionContradiction. This file drives the PUBLIC Rate seam
// (operator E and customer-policy R) and pins the real outcome of both.
//
// The crux the claim rests on -- ev.missingMember == false while a present child
// has no resolving rule -- is exactly the state (b) builds, so the classification
// (b) actually receives is the empirical answer to it.

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// advTaintMoney renders the whole economic verdict of one valuation so the -v
// transcript carries the real error, completeness, and every line's status,
// amount and quantity instead of only the pass/fail.
func advTaintMoney(val economics.Valuation, err error) string {
	var out strings.Builder
	if err == nil {
		out.WriteString("err=<nil>")
	} else {
		out.WriteString("err=" + err.Error())
	}
	out.WriteString(" completeness=" + string(val.Completeness))
	out.WriteString(" total=" + review583Total(val))
	out.WriteString(" lines=[")
	for i, line := range val.Lines {
		if i > 0 {
			out.WriteString(" ")
		}
		component := "<none>"
		if line.Component != nil {
			component = line.Component.Component
		}
		amount := "<none>"
		if line.Amount != nil {
			amount = line.Amount.CanonicalString()
		}
		quantity := "<none>"
		if line.Quantity != nil {
			quantity = line.Quantity.CanonicalString()
		}
		out.WriteString("{" + component +
			" status=" + string(line.Status) +
			" amount=" + amount +
			" qty=" + quantity + "}")
	}
	out.WriteString("]")
	return out.String()
}

func TestAdversarialTaintedPartitionUnpricedChildSettlement(t *testing.T) {
	t.Parallel()

	parentA := r7Key("vendor:adv_taint_a") // A
	partB := r7Key("vendor:adv_taint_b")   // B
	sharedS := r7Key("vendor:adv_taint_s") // S, shared in case (b)
	parentD := r7Key("vendor:adv_taint_d") // D, the unrelated shared owner

	// (a) one parent, two required partition members.
	untaintedSchema := []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: parentA, Child: partB},
		{Kind: metering.RelationshipPartition, Parent: parentA, Child: sharedS},
	}}}
	// (b) the same, plus the unrelated D -> {S} that makes S a shared member and
	// therefore taints A. Nothing about A, B, S, their quantities or their rules
	// changes between (a) and (b).
	taintedSchema := []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: parentA, Child: partB},
		{Kind: metering.RelationshipPartition, Parent: parentA, Child: sharedS},
		{Kind: metering.RelationshipPartition, Parent: parentD, Child: sharedS},
	}}}

	// The observed vector is identical in every case: A=0, B=0, S=30.
	measures := func(t *testing.T) []metering.Measure {
		t.Helper()
		return []metering.Measure{
			b1Measure(t, parentA, "0"),
			b1Measure(t, partB, "0"),
			b1Measure(t, sharedS, "30"),
		}
	}

	// (a) UntAINTED, only S priced, A=0 B=0 S=30. Conservation fails
	// (0 != 0 + 30) and the UNPRICED zero child B still contributes its exact
	// share to the sum, so this must be a typed partition contradiction that
	// fails the settlement closed as partial.
	t.Run("a_untainted_not_conserved_is_contradiction", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "advt-s-rate", sharedS, "5")}
		resolved := f3Resolved(t, "advt-a-untainted", rules, untaintedSchema)
		obs := f3Observation(t, "advt-a-untainted", measures(t)...)
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				t.Logf("case (a) %s: %s", seam.name, advTaintMoney(val, err))
				if !errors.Is(err, billing.ErrSchemaPartitionContradiction) {
					t.Fatalf("case (a) %s: want ErrSchemaPartitionContradiction, got %s", seam.name, advTaintMoney(val, err))
				}
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("case (a) %s: unconserved 0 != 0 + 30 must never settle complete: %s", seam.name, advTaintMoney(val, err))
				}
			})
		}
	})

	// (b) THE REVIEWER'S CASE: identical evidence, plus the unrelated D -> {S}
	// that taints A. B is still PRESENT and still has no resolving rule, so the
	// proof-half of coverage is false here. The settlement must still fail
	// closed: the tainted parent must be classified (typed), never skipped into
	// a complete customer charge.
	t.Run("b_tainted_shared_child_never_settles_complete", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{b1Rule(t, "advt-s-rate", sharedS, "5")}
		resolved := f3Resolved(t, "advt-b-tainted", rules, taintedSchema)
		obs := f3Observation(t, "advt-b-tainted", measures(t)...)
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				t.Logf("case (b) %s: %s", seam.name, advTaintMoney(val, err))
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("case (b) %s: LIVE FAIL-OPEN -- a tainted parent with an unpriced present child settled COMPLETE: %s", seam.name, advTaintMoney(val, err))
				}
				// The structural verdict for a shared member is the typed
				// incomparable diagnosis, decided from declared ownership alone
				// and independent of rule coverage.
				if !errors.Is(err, billing.ErrSchemaPartitionIncomparable) {
					t.Fatalf("case (b) %s: tainted parent must be classified as ErrSchemaPartitionIncomparable, got %s", seam.name, advTaintMoney(val, err))
				}
				if val.Completeness != economics.CompletenessPartial {
					t.Fatalf("case (b) %s: completeness=%q, want partial", seam.name, val.Completeness)
				}
			})
		}
	})

	// (c) The reviewer's control: same schema as (a) and same unconserved
	// quantities, but A and B are priced too, so the partition is fully
	// billable. This pins that the unconserved sum is rejected for ARITHMETIC
	// reasons and not merely because a child happened to be unpriced.
	t.Run("c_control_all_priced_not_conserved", func(t *testing.T) {
		t.Parallel()
		rules := []economics.RatingRule{
			b1Rule(t, "advt-a-rate", parentA, "5"),
			b1Rule(t, "advt-b-rate", partB, "5"),
			b1Rule(t, "advt-s-rate", sharedS, "5"),
		}
		resolved := f3Resolved(t, "advt-c-all-priced", rules, untaintedSchema)
		obs := f3Observation(t, "advt-c-all-priced", measures(t)...)
		for _, seam := range review5beSeams() {
			seam := seam
			t.Run(seam.name, func(t *testing.T) {
				t.Parallel()
				val, err := seam.rate(t, resolved, obs)
				t.Logf("case (c) %s: %s", seam.name, advTaintMoney(val, err))
				if val.Completeness == economics.CompletenessComplete {
					t.Fatalf("case (c) %s: unconserved 0 != 0 + 30 must never settle complete: %s", seam.name, advTaintMoney(val, err))
				}
			})
		}
	})
}
