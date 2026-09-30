package billing_test

// B2B (PR #659 adversarial repair R7): child-only tariff over a complete frozen
// partition of an intact aggregate observation. B1 rejects a tariff that prices
// both an aggregate parent and its included child. B2A prices only the aggregate
// parent and excuses its included children. B2B handles the complementary case:
// a frozen tariff prices only the children of an intact observation whose
// aggregate parent is present but unpriced.
//
// The unpriced aggregate summary is not an independent missing charge when a
// proven complete disjoint child partition covers it. The decision is driven by
// the explicit frozen partition/aggregate relationship, the equal direction/unit
// edge, the same reduction scope, the presence and completeness of every declared
// child, and exact arithmetic conservation of the comparable effective reduced
// quantities (parent == sum of present, complete, rateable children). It is never
// inferred from component names alone; a contradictory aggregate is exposed as
// partial/incomparable evidence rather than having its residual invented or
// silently suppressed.
//
// The suppression must fail closed for a mere subset edge, an incomplete
// partition (absent/null/unavailable child), an arithmetically contradicted
// partition, an ambiguous overlapping child shared by two complete parents, a
// cross-direction/cross-scope edge, and schema-free legacy tariffs. A tariff
// that prices both sides still fails closed through B1, and an aggregate-only
// B2A tariff still rates unchanged.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	b2bParentIn  = "vendor:ordinary_input_total"
	b2bParentOut = "vendor:ordinary_output_total"
	b2bTextIn    = "vendor:text_input"
	b2bAudioIn   = "vendor:audio_input"
	b2bTextOut   = "vendor:text_output"
	b2bAudioOut  = "vendor:audio_output"

	b2bExtraIn = "vendor:image_input"
)

func b2bParentInKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, b2bParentIn, metering.UnitToken)
}

func b2bParentOutKey() metering.ComponentKey {
	return b1Key(metering.DirectionOutput, b2bParentOut, metering.UnitToken)
}

func b2bTextInKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, b2bTextIn, metering.UnitToken)
}

func b2bAudioInKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, b2bAudioIn, metering.UnitToken)
}

func b2bTextOutKey() metering.ComponentKey {
	return b1Key(metering.DirectionOutput, b2bTextOut, metering.UnitToken)
}

func b2bAudioOutKey() metering.ComponentKey {
	return b1Key(metering.DirectionOutput, b2bAudioOut, metering.UnitToken)
}

// b2bIntactMeasures is the intact native evidence shape: the ordinary aggregate
// totals plus their disjoint directional text/audio children. The aggregate
// quantities equal the sum of their children, exactly as an intact provider
// mapping reports them.
func b2bIntactMeasures(t *testing.T) []metering.Measure {
	t.Helper()
	return []metering.Measure{
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1Measure(t, b2bAudioInKey(), "3"),
		b1Measure(t, b2bParentOutKey(), "9"),
		b1Measure(t, b2bTextOutKey(), "5"),
		b1Measure(t, b2bAudioOutKey(), "4"),
	}
}

// b2bChildOnlyRules prices only the four directional native children, with
// deliberately distinct rates per direction and modality so no aggregate sum can
// coincide with the literal expected total by accident:
// 8*2 + 3*5 + 5*3 + 4*7 = 16 + 15 + 15 + 28 = 74.
func b2bChildOnlyRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "text-in-rate", b2bTextInKey(), "2"),
		b1Rule(t, "audio-in-rate", b2bAudioInKey(), "5"),
		b1Rule(t, "text-out-rate", b2bTextOutKey(), "3"),
		b1Rule(t, "audio-out-rate", b2bAudioOutKey(), "7"),
	}
}

func b2bAggregateOnlyRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "parent-in-rate", b2bParentInKey(), "2"),
		b1Rule(t, "parent-out-rate", b2bParentOutKey(), "3"),
	}
}

// b2bPartitionSchemas declares a complete disjoint partition: each aggregate
// parent's declared children are exactly the native children observed. The
// relationship kind is parameterized so subset (partial) can be contrasted with
// partition/aggregate (complete).
func b2bPartitionSchemas(kind metering.RelationshipKind) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: kind, Parent: b2bParentInKey(), Child: b2bTextInKey()},
			{Kind: kind, Parent: b2bParentInKey(), Child: b2bAudioInKey()},
			{Kind: kind, Parent: b2bParentOutKey(), Child: b2bTextOutKey()},
			{Kind: kind, Parent: b2bParentOutKey(), Child: b2bAudioOutKey()},
		},
	}}
}

func b2bResolved(t *testing.T, refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	tariff := b1Tariff(t, refID, rules, schemas)
	resolved := b1Resolve(t, tariff)
	if resolved.Content.ContentHash != tariff.Content.ContentHash {
		t.Fatalf("catalog changed the frozen schema hash")
	}
	return resolved
}

func b2bIntactObservation(t *testing.T, id string) metering.Observation {
	t.Helper()
	return b1Observation(t, id, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, b2bIntactMeasures(t)...)
}

// b2bAssertChildOnly pinpoints the literal hand-derived child-only result 74/0
// over exactly the four non-zero child lines. It never regenerates the expected
// amount from the implementation under test.
func b2bAssertChildOnly(t *testing.T, val economics.Valuation) {
	t.Helper()
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("child-only completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "74/0" {
		t.Fatalf("child-only total=%s, want 74", total)
	}
	nonzero := make(map[string]struct{})
	for _, line := range val.Lines {
		if line.Component == nil {
			t.Fatalf("unexpected component-less line %+v", line)
		}
		if line.Amount != nil && line.Amount.CanonicalString() != "0/0" {
			nonzero[line.Component.Component] = struct{}{}
		}
	}
	for _, child := range []string{b2bTextIn, b2bAudioIn, b2bTextOut, b2bAudioOut} {
		if _, ok := nonzero[child]; !ok {
			t.Fatalf("child %q missing from payable set %v", child, nonzero)
		}
	}
	for _, parent := range []string{b2bParentIn, b2bParentOut} {
		if _, ok := nonzero[parent]; ok {
			t.Fatalf("suppressed aggregate parent %q must not be payable: %v", parent, nonzero)
		}
	}
	if len(nonzero) != 4 {
		t.Fatalf("payable child count=%d, want exactly 4: %v", len(nonzero), nonzero)
	}
}

// b2bAssertNoParentLine proves the unpriced aggregate is not emitted as a
// rate-missing or partial line once its complete child partition covers it.
func b2bAssertNoParentLine(t *testing.T, val economics.Valuation) {
	t.Helper()
	for _, line := range val.Lines {
		if line.Component == nil {
			continue
		}
		if line.Component.Component == b2bParentIn || line.Component.Component == b2bParentOut {
			t.Fatalf("suppressed aggregate parent line must not be emitted: %+v", line)
		}
	}
}

func TestB2BChildOnlyCompletePartitionRatesIntactEvidence(t *testing.T) {
	t.Parallel()
	obs := b2bIntactObservation(t, "b2b-intact")
	resolved := b2bResolved(t, "b2b-child-only-complete", b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipPartition))

	// Operator E-plane.
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	operatorVal, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("child-only operator rating must be complete, got %v; lines=%+v", err, operatorVal.Lines)
	}
	b2bAssertChildOnly(t, operatorVal)
	b2bAssertNoParentLine(t, operatorVal)
	b2aAssertIntactEvidenceRetained(t, obs, operatorVal)

	// Customer-policy seam.
	policyVal, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("child-only customer-policy rating must be complete, got %v; lines=%+v", err, policyVal.Lines)
	}
	b2bAssertChildOnly(t, policyVal)
	b2bAssertNoParentLine(t, policyVal)
	b2aAssertIntactEvidenceRetained(t, obs, policyVal)
}

func TestB2BChildOnlyCompletePartitionRatesThroughRetailSelection(t *testing.T) {
	t.Parallel()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatalf("NewBillingCallID: %v", err)
	}
	obs := b2aRetailObservation(t, callID, "b2b-intact-retail", b2bIntactMeasures(t))
	call, leg, policy := b2aRetailCall(t, callID, obs)
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatalf("SelectRetailBLegEvidence: %v", err)
	}
	resolved := b2bResolved(t, b2aRetailTariffID, b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipPartition))

	result, err := billing.RateSelectedRetailBLegs(context.Background(), billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: resolved, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("child-only retail rating must be complete, got %v; inference=%+v", err, result.InferenceValuation)
	}
	b2bAssertChildOnly(t, result.InferenceValuation)
	b2bAssertNoParentLine(t, result.InferenceValuation)
	b2aAssertIntactEvidenceRetained(t, obs, result.InferenceValuation)
}

// TestB2BChildOnlyAggregateRelationshipKindRatesIntactEvidence proves an
// explicit aggregate parent edge (not just partition) is likewise a complete
// coverage declaration for child-only pricing.
func TestB2BChildOnlyAggregateRelationshipKindRatesIntactEvidence(t *testing.T) {
	t.Parallel()
	obs := b2bIntactObservation(t, "b2b-intact-aggregate-kind")
	resolved := b2bResolved(t, "b2b-child-only-aggregate-kind", b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipAggregate))

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("aggregate-kind child-only rating must be complete, got %v; lines=%+v", err, val.Lines)
	}
	b2bAssertChildOnly(t, val)
	b2aAssertIntactEvidenceRetained(t, obs, val)
}

// TestB2BChildOnlyContradictoryAggregateStaysPartial proves the complete
// partition proof is bounded by exact arithmetic conservation (F3). The
// declared complete partition is a claim, not a truth: when the intact aggregate
// parent quantities (37 and 41) disagree with the child sums (8+3 and 5+4), the
// evidence is inconsistent and the rater must fail closed partial rather than
// invent the aggregate's residual or suppress it as if the children had priced
// its whole share. The priced children stay payable at their literal child total
// 74/0, and each contested parent keeps its missing-rate diagnostic.
func TestB2BChildOnlyContradictoryAggregateStaysPartial(t *testing.T) {
	t.Parallel()
	resolved := b2bResolved(t, "b2b-contradictory-aggregate", b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b1Observation(t, "b2b-contradictory-aggregate", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "37"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1Measure(t, b2bAudioInKey(), "3"),
		b1Measure(t, b2bParentOutKey(), "41"),
		b1Measure(t, b2bTextOutKey(), "5"),
		b1Measure(t, b2bAudioOutKey(), "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("contradicted complete partition must stay partial, got complete total %s; lines=%+v", b1PayableTotal(t, val), val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "74/0" {
		t.Fatalf("contradicted child-only total=%s, want literal child total 74/0", total)
	}
	assertB2BChildrenPayable(t, val)

	for _, parentKey := range []metering.ComponentKey{b2bParentInKey(), b2bParentOutKey()} {
		line := b2aComponentLine(t, val, parentKey)
		if line == nil {
			t.Fatalf("contested aggregate parent %s diagnostic missing; lines=%+v", parentKey.Component, val.Lines)
		}
		if line.Status != economics.RatingLineRateMissing {
			t.Fatalf("aggregate parent %s status=%q, want missing rate", parentKey.Component, line.Status)
		}
	}
}

// TestB2BChildOnlyMereSubsetStaysPartial pins the fail-closed boundary: a
// subset edge only proves partial containment, so an unpriced aggregate parent
// with priced children keeps its typed partial instead of being excused.
func TestB2BChildOnlyMereSubsetStaysPartial(t *testing.T) {
	t.Parallel()
	obs := b2bIntactObservation(t, "b2b-subset-partial")
	resolved := b2bResolved(t, "b2b-child-only-subset", b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipSubset))

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("a mere subset must not excuse the unpriced aggregate parent; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("subset completeness=%q, want partial", val.Completeness)
	}
	assertB2BChildrenPayable(t, val)
}

// TestB2BChildOnlyMissingPartitionChildStaysPartial proves a declared partition
// child absent from the evidence leaves the partition incomplete, so the
// unpriced parent keeps its typed partial.
func TestB2BChildOnlyMissingPartitionChildStaysPartial(t *testing.T) {
	t.Parallel()
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: b2bParentInKey(), Child: b2bTextInKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentInKey(), Child: b2bAudioInKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentInKey(), Child: b1Key(metering.DirectionInput, b2bExtraIn, metering.UnitToken)},
			{Kind: metering.RelationshipPartition, Parent: b2bParentOutKey(), Child: b2bTextOutKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentOutKey(), Child: b2bAudioOutKey()},
		},
	}}
	resolved := b2bResolved(t, "b2b-child-only-missing", b2bChildOnlyRules(t), schemas)
	obs := b1Observation(t, "b2b-missing-child", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1Measure(t, b2bAudioInKey(), "3"),
		b1Measure(t, b2bParentOutKey(), "9"),
		b1Measure(t, b2bTextOutKey(), "5"),
		b1Measure(t, b2bAudioOutKey(), "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("missing partition child must keep the partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("missing-child completeness=%q, want partial", val.Completeness)
	}
	assertB2BChildrenPayable(t, val)
}

// TestB2BChildOnlyNullPartitionChildStaysPartial proves an unavailable (null)
// partition child makes the partition incomplete and keeps the parent partial.
func TestB2BChildOnlyNullPartitionChildStaysPartial(t *testing.T) {
	t.Parallel()
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: b2bParentInKey(), Child: b2bTextInKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentInKey(), Child: b2bAudioInKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentOutKey(), Child: b2bTextOutKey()},
			{Kind: metering.RelationshipPartition, Parent: b2bParentOutKey(), Child: b2bAudioOutKey()},
		},
	}}
	resolved := b2bResolved(t, "b2b-child-only-null", b2bChildOnlyRules(t), schemas)
	obs := b1Observation(t, "b2b-null-child", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1UnavailableMeasure(b2bAudioInKey()),
		b1Measure(t, b2bParentOutKey(), "9"),
		b1Measure(t, b2bTextOutKey(), "5"),
		b1Measure(t, b2bAudioOutKey(), "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("null partition child must keep the partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("null-child completeness=%q, want partial", val.Completeness)
	}
}

// TestB2BChildOnlyExplicitZeroChildStaysComplete proves a present, complete
// zero-valued partition child counts toward the complete partition: the parent
// stays excused and the zero child is an explicit-free priced line.
func TestB2BChildOnlyExplicitZeroChildStaysComplete(t *testing.T) {
	t.Parallel()
	resolved := b2bResolved(t, "b2b-child-only-zero", b2bChildOnlyRules(t), b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b1Observation(t, "b2b-zero-child", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "8"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1Measure(t, b2bAudioInKey(), "0"),
		b1Measure(t, b2bParentOutKey(), "9"),
		b1Measure(t, b2bTextOutKey(), "5"),
		b1Measure(t, b2bAudioOutKey(), "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("explicit-zero partition child must stay complete, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("explicit-zero completeness=%q, want complete", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "59/0" {
		t.Fatalf("explicit-zero total=%s, want 59 (8*2 + 0*5 + 5*3 + 4*7)", total)
	}
	b2bAssertNoParentLine(t, val)
}

// TestB2BChildOnlyAmbiguousOverlappingChildrenFailClosed proves a child shared
// by two complete parents is an ambiguous overlapping partition, so neither
// parent is excused and the valuation stays partial without double counting.
func TestB2BChildOnlyAmbiguousOverlappingChildrenFailClosed(t *testing.T) {
	t.Parallel()
	parentIn := b2bParentInKey()
	shared := b2bTextInKey()
	firstOther := b1Key(metering.DirectionInput, "vendor:other_total", metering.UnitToken)
	secondOther := b1Key(metering.DirectionInput, "vendor:third_total", metering.UnitToken)
	sharedChild := b1Key(metering.DirectionInput, "vendor:shared_child", metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parentIn, Child: shared},
			{Kind: metering.RelationshipPartition, Parent: firstOther, Child: sharedChild},
			{Kind: metering.RelationshipPartition, Parent: secondOther, Child: sharedChild},
		},
	}}
	rules := []economics.RatingRule{
		b1Rule(t, "shared-rate", shared, "2"),
		b1Rule(t, "child-rate", sharedChild, "3"),
	}
	resolved := b2bResolved(t, "b2b-ambiguous-overlap", rules, schemas)
	obs := b1Observation(t, "b2b-ambiguous-overlap", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parentIn, "4"),
		b1Measure(t, shared, "4"),
		b1Measure(t, firstOther, "4"),
		b1Measure(t, secondOther, "4"),
		b1Measure(t, sharedChild, "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("ambiguous overlapping children must fail closed; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("ambiguous completeness=%q, want partial", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "20/0" {
		t.Fatalf("ambiguous total=%s, want 20 (4*2 + 4*3, shared child billed once)", total)
	}
}

// TestB2BChildOnlyCrossDirectionNotComplete proves a declared edge that crosses
// direction is not a same-flow partition of the parent, so the parent keeps its
// partial.
func TestB2BChildOnlyCrossDirectionNotComplete(t *testing.T) {
	t.Parallel()
	outputChild := b2bAudioOutKey()
	tariff := b1Tariff(t, "b2b-cross-direction", []economics.RatingRule{
		b1Rule(t, "output-child-rate", outputChild, "3"),
	}, b1Schema(metering.RelationshipPartition, b2bParentInKey(), outputChild))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b2b-cross-direction", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, outputChild, "4"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("cross-direction edge must not excuse the parent; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("cross-direction completeness=%q, want partial", val.Completeness)
	}
}

// TestB2BChildOnlyCrossScopeNotComplete proves the partition is scoped: a child
// priced in a different source/B-leg scope cannot excuse its parent's missing
// rate in this scope.
func TestB2BChildOnlyCrossScopeNotComplete(t *testing.T) {
	t.Parallel()
	parent := b2bParentInKey()
	child := b2bTextInKey()
	tariff := b1Tariff(t, "b2b-cross-scope", []economics.RatingRule{
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipPartition, parent, child))
	resolved := b1Resolve(t, tariff)
	parentObs := b1Observation(t, "b2b-cross-scope-parent", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "11"))
	childObs := b1Observation(t, "b2b-cross-scope-child", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, child, "8"))

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{parentObs, childObs}), resolved)
	if err == nil {
		t.Fatalf("cross-scope child must not excuse the parent; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("cross-scope completeness=%q, want partial", val.Completeness)
	}
}

// TestB2BBothPricedStillFailsClosed is the B1 regression: a tariff that prices
// the aggregate parent and its included child still returns the typed overlap
// conflict with no payable quantity line.
func TestB2BBothPricedStillFailsClosed(t *testing.T) {
	t.Parallel()
	rules := append(b2bAggregateOnlyRules(t), b2bChildOnlyRules(t)...)
	resolved := b2bResolved(t, "b2b-both-priced", rules, b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b2bIntactObservation(t, "b2b-both-priced")

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("priced parent+child error=%v, want ErrSchemaOverlapConflict; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("both-priced completeness=%q, want conflict", val.Completeness)
	}
	for _, line := range val.Lines {
		if line.Component != nil && line.Amount != nil && line.Amount.CanonicalString() != "0/0" {
			t.Fatalf("overlapping quantity line must not be payable: %+v", line)
		}
	}
}

// TestB2BAggregateOnlyRegressionUnchanged proves the B2A behavior is not
// weakened: a tariff that prices only the aggregate parents with unpriced
// included children still rates completely at 49/0.
func TestB2BAggregateOnlyRegressionUnchanged(t *testing.T) {
	t.Parallel()
	resolved := b2bResolved(t, "b2b-aggregate-only-regression", b2bAggregateOnlyRules(t), b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b2bIntactObservation(t, "b2b-aggregate-only-regression")

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("aggregate-only B2A regression must be complete, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("aggregate-only completeness=%q, want complete", val.Completeness)
	}
	if total := b1PayableTotal(t, val); total != "49/0" {
		t.Fatalf("aggregate-only total=%s, want 49 (11*2 + 9*3)", total)
	}
}

// TestB2BChildOnlyNilSchemaLegacyUnchanged pins legacy behavior: without a
// frozen schema an unpriced aggregate parent is still a typed partial even when
// its children are priced. Complete coverage is never inferred.
func TestB2BChildOnlyNilSchemaLegacyUnchanged(t *testing.T) {
	t.Parallel()
	resolved := b2bResolved(t, "b2b-child-only-nil-schema", b2bChildOnlyRules(t), nil)
	obs := b2bIntactObservation(t, "b2b-child-only-nil-schema")

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("legacy nil-schema child-only tariff must stay partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("legacy completeness=%q, want partial", val.Completeness)
	}
	assertB2BChildrenPayable(t, val)
}

// b2bWholeContextTierRule prices key with a whole-context all-units tier: the
// low rate applies while the selected context is at or below 15 units and the
// high rate applies above it. The context is deliberately selected from the
// frozen economic basis, so counting a duplicate aggregate summary in the
// context would push the tier over the threshold and overcharge.
func b2bWholeContextTierRule(t *testing.T, id string, key metering.ComponentKey, low, high string) economics.RatingRule {
	t.Helper()
	limit := b1Decimal(t, "15")
	lowRate := b1Decimal(t, low)
	highRate := b1Decimal(t, high)
	return economics.RatingRule{
		ID: id, Kind: economics.RatingRuleAllUnits, Component: &key, Currency: "USD",
		SelectionScope: economics.SelectionWholeContext, TierMode: economics.TierAllUnits,
		Tiers: []economics.RatingTier{{UpTo: &limit, UnitPrice: &lowRate}, {UnitPrice: &highRate}},
	}
}

// TestB2BChildOnlyCoveredParentExcludedFromWholeContextTier is the finding
// R7-B2B-1 RED vector. The unpriced aggregate parent is excused from line
// emission by the complete child partition, but before the fix its quantity
// still entered the whole-context tier total. With parent 11 + children 8 + 3
// the context was 22 (> 15), selecting the high tier and overcharging
// 8*10 + 3*7 = 101. The child economic basis is 8 + 3 = 11 (<= 15):
// 8*2 + 3*5 = 31. The covered summary must be removed from the economic context,
// not only from the emitted lines, while its observation reference stays
// retained for audit.
func TestB2BChildOnlyCoveredParentExcludedFromWholeContextTier(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{
		b2bWholeContextTierRule(t, "text-in-tier", b2bTextInKey(), "2", "10"),
		b2bWholeContextTierRule(t, "audio-in-tier", b2bAudioInKey(), "5", "7"),
	}
	resolved := b2bResolved(t, "b2b-child-only-tier", rules, b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b1Observation(t, "b2b-child-only-tier", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, b2bTextInKey(), "8"),
		b1Measure(t, b2bAudioInKey(), "3"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("child-only whole-context rating must be complete, got %v; lines=%+v", err, val.Lines)
	}
	b2bAssertNoParentLine(t, val)
	b2aAssertIntactEvidenceRetained(t, obs, val)
	if total := b1PayableTotal(t, val); total != "31/0" {
		t.Fatalf("child-only whole-context total=%s, want 31 (8*2 + 3*5 from child basis 11); lines=%+v", total, val.Lines)
	}
	if amount := b2aComponentAmount(t, val, b2bTextInKey()); amount == nil || amount.CanonicalString() != "16/0" {
		t.Fatalf("text-in amount=%v, want 16 (low tier from child context)", amount)
	}
	if amount := b2aComponentAmount(t, val, b2bAudioInKey()); amount == nil || amount.CanonicalString() != "15/0" {
		t.Fatalf("audio-in amount=%v, want 15 (low tier from child context)", amount)
	}
}

// TestB2BChildOnlyTierContextControlAggregateOnlyUsesParent is the R7-B2B-1
// control. When the aggregate parent is the priced biller (B2A aggregate-only)
// it must keep contributing its own quantity to its whole-context tier. The
// children are excluded as included-but-unpriced, so the context is the parent
// 18 (> 15), selecting the high rate 10: 18*10 = 180. Removing a priced parent
// from its own context would wrongly select the low tier.
func TestB2BChildOnlyTierContextControlAggregateOnlyUsesParent(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{b2bWholeContextTierRule(t, "parent-in-tier", b2bParentInKey(), "2", "10")}
	resolved := b2bResolved(t, "b2b-tier-control", rules, b2bPartitionSchemas(metering.RelationshipPartition))
	obs := b1Observation(t, "b2b-tier-control", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "18"),
		b1Measure(t, b2bTextInKey(), "10"),
		b1Measure(t, b2bAudioInKey(), "8"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("aggregate-only whole-context parent must rate completely, got %v; lines=%+v", err, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "180/0" {
		t.Fatalf("aggregate-only whole-context total=%s, want 180 (18*10 from parent basis); lines=%+v", total, val.Lines)
	}
	if amount := b2aComponentAmount(t, val, b2bParentInKey()); amount == nil || amount.CanonicalString() != "180/0" {
		t.Fatalf("aggregate parent amount=%v, want 180", amount)
	}
}

// TestB2BChildOnlyInformationalChildCannotProveParentCoverage is the finding
// R7-B2B-2 RED vector. An unpriced informational summary child
// (input_token_total) is present and complete, so the structural presence check
// alone excused the unpriced aggregate parent even though the rating loop skips
// the informational child for lack of a rule. An independently priced output
// then made the valuation falsely complete with no input charge. A child proves
// parent coverage only when it is itself selected, rating-eligible and has a
// resolving rule; otherwise the parent's missing-rate diagnostic is retained.
func TestB2BChildOnlyInformationalChildCannotProveParentCoverage(t *testing.T) {
	t.Parallel()
	infoChild := b1Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	outKey := b1Key(metering.DirectionOutput, "vendor:output", metering.UnitToken)
	schemas := b1Schema(metering.RelationshipPartition, b2bParentInKey(), infoChild)
	rules := []economics.RatingRule{b1Rule(t, "output-rate", outKey, "3")}
	resolved := b2bResolved(t, "b2b-informational-child", rules, schemas)
	obs := b1Observation(t, "b2b-informational-child", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, b2bParentInKey(), "11"),
		b1Measure(t, infoChild, "11"),
		b1Measure(t, outKey, "7"),
	)

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("an unpriced informational child must not prove parent coverage; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("informational-child completeness=%q, want partial", val.Completeness)
	}
	if amount := b2aComponentAmount(t, val, outKey); amount == nil || amount.CanonicalString() != "21/0" {
		t.Fatalf("independent output amount=%v, want 21 retained", amount)
	}
	parentLine := b2aComponentLine(t, val, b2bParentInKey())
	if parentLine == nil {
		t.Fatalf("uncovered aggregate parent diagnostic missing; lines=%+v", val.Lines)
	}
	if parentLine.Status != economics.RatingLineRateMissing {
		t.Fatalf("aggregate parent status=%q, want missing rate; line=%+v", parentLine.Status, parentLine)
	}
}

func assertB2BChildrenPayable(t *testing.T, val economics.Valuation) {
	t.Helper()
	for _, child := range []string{b2bTextIn, b2bAudioIn, b2bTextOut, b2bAudioOut} {
		amount := b2aComponentAmount(t, val, b2bKeyForComponent(child))
		if amount == nil || amount.CanonicalString() == "0/0" {
			t.Fatalf("child %q must stay payable on a partial child-only valuation, got %v; lines=%+v", child, amount, val.Lines)
		}
	}
}

func b2bKeyForComponent(component string) metering.ComponentKey {
	switch component {
	case b2bTextIn:
		return b2bTextInKey()
	case b2bAudioIn:
		return b2bAudioInKey()
	case b2bTextOut:
		return b2bTextOutKey()
	default:
		return b2bAudioOutKey()
	}
}
