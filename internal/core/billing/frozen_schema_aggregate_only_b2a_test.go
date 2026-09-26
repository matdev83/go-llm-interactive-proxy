package billing_test

// B2A (PR #659 adversarial repair R7): aggregate-only pricing of an intact
// aggregate-plus-included-children observation. Where B1 rejects a tariff that
// prices both a parent aggregate and an included child, B2A handles the
// complementary case: a frozen tariff prices only the aggregate parent and has
// no priced child rules. The included child evidence must stay retained on its
// original observation, must not independently require a rate, and must not
// make the aggregate-only valuation partial or add a second charge.
//
// The decision is driven only by the explicit frozen relationship (inclusion
// kind, equal direction, equal unit, same scope), the parent having a
// resolvable rule, and the child having no resolvable rule of its own. It is
// never inferred from component names or coincidental numbers. An independent
// component with no relationship and no rule keeps the existing typed partial
// behavior, and a tariff that prices both sides still fails closed through B1.
//
// These vectors run the real catalog-resolved frozen snapshot through the
// production customer-policy and retail-selection seams.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	b2aParentIn  = "vendor:ordinary_input_total"
	b2aParentOut = "vendor:ordinary_output_total"
	b2aTextIn    = "vendor:text_input"
	b2aAudioIn   = "vendor:audio_input"
	b2aTextOut   = "vendor:text_output"
	b2aAudioOut  = "vendor:audio_output"

	b2aRetailTariffID = "b2a-retail-aggregate-only"
	b2aRetailPolicyID = "b2a-retail-policy"
)

func b2aParentInKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, b2aParentIn, metering.UnitToken)
}

func b2aParentOutKey() metering.ComponentKey {
	return b1Key(metering.DirectionOutput, b2aParentOut, metering.UnitToken)
}

func b2aChildKeys() [4]metering.ComponentKey {
	return [4]metering.ComponentKey{
		b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken),
		b1Key(metering.DirectionInput, b2aAudioIn, metering.UnitToken),
		b1Key(metering.DirectionOutput, b2aTextOut, metering.UnitToken),
		b1Key(metering.DirectionOutput, b2aAudioOut, metering.UnitToken),
	}
}

// b2aIntactMeasures is the intact native evidence shape: the ordinary aggregate
// totals plus their directional native text/audio children. The aggregate
// quantities equal the sum of their children, exactly as an intact provider
// mapping reports them.
func b2aIntactMeasures(t *testing.T) []metering.Measure {
	t.Helper()
	children := b2aChildKeys()
	return []metering.Measure{
		b1Measure(t, b2aParentInKey(), "11"),
		b1Measure(t, children[0], "8"),
		b1Measure(t, children[1], "3"),
		b1Measure(t, b2aParentOutKey(), "9"),
		b1Measure(t, children[2], "5"),
		b1Measure(t, children[3], "4"),
	}
}

func b2aObservationWith(t *testing.T, id string, measures []metering.Measure) metering.Observation {
	t.Helper()
	return b1Observation(t, id, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
}

// b2aInclusionSchemas declares, for every native child, that its aggregate
// parent already includes it. Exactly one explicit relationship kind is used;
// direction and unit are equal across the edge.
func b2aInclusionSchemas(kind metering.RelationshipKind) []metering.ComponentSchema {
	children := b2aChildKeys()
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: kind, Parent: b2aParentInKey(), Child: children[0]},
			{Kind: kind, Parent: b2aParentInKey(), Child: children[1]},
			{Kind: kind, Parent: b2aParentOutKey(), Child: children[2]},
			{Kind: kind, Parent: b2aParentOutKey(), Child: children[3]},
		},
	}}
}

// b2aAggregateOnlyRules prices only the ordinary aggregate parents. Input and
// output rates are deliberately distinct so a wrong aggregate sum cannot
// coincide with the literal expected amount. No child rule exists at all, which
// is exactly the "no priced child rules" branch B2A owns.
func b2aAggregateOnlyRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "parent-in-rate", b2aParentInKey(), "2"),
		b1Rule(t, "parent-out-rate", b2aParentOutKey(), "3"),
	}
}

func b2aResolvedAggregateOnlyTariff(t *testing.T, refID string) economics.TariffSnapshot {
	t.Helper()
	tariff := b1Tariff(t, refID, b2aAggregateOnlyRules(t), b2aInclusionSchemas(metering.RelationshipSubset))
	resolved := b1Resolve(t, tariff)
	if resolved.Content.ContentHash != tariff.Content.ContentHash {
		t.Fatalf("catalog changed the frozen schema hash")
	}
	return resolved
}

// b2aAssertAggregateOnly pins the literal hand-derived aggregate-only result
// 11*2 + 9*3 = 49 over exactly the two aggregate parent lines. It never
// regenerates the expected amount from the implementation under test.
func b2aAssertAggregateOnly(t *testing.T, val economics.Valuation) {
	t.Helper()
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("aggregate-only completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "49/0" {
		t.Fatalf("aggregate-only total=%s, want 49", total)
	}
	payable := make(map[string]struct{})
	for _, line := range val.Lines {
		if line.Amount == nil || line.Component == nil || line.Amount.CanonicalString() == "0/0" {
			continue
		}
		payable[line.Component.Component] = struct{}{}
	}
	if len(payable) != 2 {
		t.Fatalf("payable components=%v, want exactly the two aggregate parents", payable)
	}
	for _, parent := range []string{b2aParentIn, b2aParentOut} {
		if _, ok := payable[parent]; !ok {
			t.Fatalf("aggregate parent %q missing from payable set %v", parent, payable)
		}
	}
	for _, child := range []string{b2aTextIn, b2aAudioIn, b2aTextOut, b2aAudioOut} {
		if _, ok := payable[child]; ok {
			t.Fatalf("included child %q must not be independently payable: %v", child, payable)
		}
	}
}

// b2aAssertIntactEvidenceRetained proves the intact observation (aggregate
// totals AND every child measure) is the literal referenced evidence: its
// original replay-stable reference, which hashes the full measure set, is
// retained in the valuation input. A filtered or rewritten child payload would
// produce a different reference and fail this assertion.
func b2aAssertIntactEvidenceRetained(t *testing.T, observation metering.Observation, val economics.Valuation) {
	t.Helper()
	want, err := observation.Ref(observation.Subject.StoreID)
	if err != nil {
		t.Fatalf("observation ref: %v", err)
	}
	for _, ref := range val.InputObservations {
		if ref.Equal(want) {
			return
		}
	}
	t.Fatalf("intact observation reference (all %d measures) not retained for audit: %+v", len(observation.Measures), val.InputObservations)
}

func TestB2AAggregateOnlyTariffRatesIntactEvidenceThroughCatalogAndCustomerPolicy(t *testing.T) {
	t.Parallel()
	obs := b2aObservationWith(t, "b2a-intact-customer", b2aIntactMeasures(t))
	resolved := b2aResolvedAggregateOnlyTariff(t, "b2a-customer-aggregate-only")

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("aggregate-only customer-policy rating must be complete, got %v; lines=%+v", err, val.Lines)
	}
	b2aAssertAggregateOnly(t, val)
	b2aAssertIntactEvidenceRetained(t, obs, val)

	// The operator E-plane seam shares the same reduction and must agree.
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	operatorVal, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("aggregate-only operator rating must be complete, got %v; lines=%+v", err, operatorVal.Lines)
	}
	b2aAssertAggregateOnly(t, operatorVal)
	b2aAssertIntactEvidenceRetained(t, obs, operatorVal)
}

func TestB2AAggregateOnlyTariffRatesIntactEvidenceThroughCatalogAndRetailSelection(t *testing.T) {
	t.Parallel()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatalf("NewBillingCallID: %v", err)
	}
	obs := b2aRetailObservation(t, callID, "b2a-intact-retail", b2aIntactMeasures(t))
	call, leg, policy := b2aRetailCall(t, callID, obs)
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatalf("SelectRetailBLegEvidence: %v", err)
	}
	resolved := b2aResolvedAggregateOnlyTariff(t, b2aRetailTariffID)

	result, err := billing.RateSelectedRetailBLegs(context.Background(), billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: resolved, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("aggregate-only retail rating must be complete, got %v; inference=%+v", err, result.InferenceValuation)
	}
	b2aAssertAggregateOnly(t, result.InferenceValuation)
	b2aAssertIntactEvidenceRetained(t, obs, result.InferenceValuation)
}

// TestB2AAggregateOnlyAbsentZeroNullChildStaysComplete covers the boundary child
// evidence forms. An absent child, an explicit-zero child, and an unavailable
// (null) child are each already contained in the priced aggregate parent, so
// none may induce a false partial or require an independent rate.
func TestB2AAggregateOnlyAbsentZeroNullChildStaysComplete(t *testing.T) {
	t.Parallel()
	resolved := b2aResolvedAggregateOnlyTariff(t, "b2a-child-forms")
	children := b2aChildKeys()
	cases := []struct {
		name     string
		measures []metering.Measure
	}{
		{
			name: "absent",
			measures: []metering.Measure{
				b1Measure(t, b2aParentInKey(), "11"),
				b1Measure(t, children[0], "8"),
				b1Measure(t, b2aParentOutKey(), "9"),
				b1Measure(t, children[2], "5"),
			},
		},
		{
			name: "explicit_zero",
			measures: []metering.Measure{
				b1Measure(t, b2aParentInKey(), "11"),
				b1Measure(t, children[0], "8"),
				b1Measure(t, children[1], "0"),
				b1Measure(t, b2aParentOutKey(), "9"),
				b1Measure(t, children[2], "5"),
				b1Measure(t, children[3], "4"),
			},
		},
		{
			name: "null",
			measures: []metering.Measure{
				b1Measure(t, b2aParentInKey(), "11"),
				b1Measure(t, children[0], "8"),
				b1UnavailableMeasure(children[1]),
				b1Measure(t, b2aParentOutKey(), "9"),
				b1Measure(t, children[2], "5"),
				b1Measure(t, children[3], "4"),
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := b2aObservationWith(t, "b2a-forms-"+tc.name, tc.measures)
			val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
			if err != nil {
				t.Fatalf("%s child must stay complete, got %v; lines=%+v", tc.name, err, val.Lines)
			}
			b2aAssertAggregateOnly(t, val)
			b2aAssertIntactEvidenceRetained(t, obs, val)
		})
	}
}

// TestB2AAggregateOnlyNullChildOperatorPathStaysComplete is the exact operator
// E-plane counterexample for finding R7-B2A-1. The customer-policy / selected
// retail paths carry a retail component mask (keptEntries != nil) that already
// bypasses the generic reduction-completeness fallback, but the operator path
// passes no mask. With an included NULL child, aggregate.ApplyObservations sets
// the global reduced.Complete=false; the excluded child is skipped by every
// per-measure attribution loop, so the fallback then emitted a spurious
// ErrQuantityIncomplete even though the intact aggregate-only reduction is
// complete. An excluded child alone must not poison completeness, while real
// reducer failures stay visible (covered by the independent/scope/legacy
// vectors below).
func TestB2AAggregateOnlyNullChildOperatorPathStaysComplete(t *testing.T) {
	t.Parallel()
	resolved := b2aResolvedAggregateOnlyTariff(t, "b2a-operator-null-child")
	children := b2aChildKeys()
	obs := b2aObservationWith(t, "b2a-operator-null-child", []metering.Measure{
		b1Measure(t, b2aParentInKey(), "11"),
		b1Measure(t, children[0], "8"),
		b1UnavailableMeasure(children[1]),
		b1Measure(t, b2aParentOutKey(), "9"),
		b1Measure(t, children[2], "5"),
		b1Measure(t, children[3], "4"),
	})

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("operator aggregate-only null child must stay complete, got %v; lines=%+v", err, val.Lines)
	}
	b2aAssertAggregateOnly(t, val)
	b2aAssertIntactEvidenceRetained(t, obs, val)
}

// TestB2AAggregateOnlyInformationalParentDoesNotExcuseChild is the
// informational-summary counterexample for finding R7-B2A-2. A frozen
// input_token_total -> native child edge plus a rule that prices only the
// informational total must not silently excuse the native child.
// Informational totals are inclusive evidence summaries, not disjoint
// aggregate billers: treating one as coverage would drop the child's own
// billable evidence while an independently priced output makes the valuation
// falsely complete.
func TestB2AAggregateOnlyInformationalParentDoesNotExcuseChild(t *testing.T) {
	t.Parallel()
	infoParent := b1Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	nativeChild := b1Key(metering.DirectionInput, "vendor:native_input", metering.UnitToken)
	outKey := b1Key(metering.DirectionOutput, "vendor:output", metering.UnitToken)
	schemas := b1Schema(metering.RelationshipSubset, infoParent, nativeChild)
	tariff := b1Tariff(t, "b2a-informational-parent", []economics.RatingRule{
		b1Rule(t, "info-total-rate", infoParent, "2"),
		b1Rule(t, "output-rate", outKey, "3"),
	}, schemas)
	resolved := b1Resolve(t, tariff)

	obs := b2aObservationWith(t, "b2a-informational-parent", []metering.Measure{
		b1Measure(t, infoParent, "11"),
		b1Measure(t, nativeChild, "11"),
		b1Measure(t, outKey, "7"),
	})

	assertInformationalChildNotExcused := func(t *testing.T, val economics.Valuation, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("informational total must not excuse the native child; lines=%+v", val.Lines)
		}
		if val.Completeness != economics.CompletenessPartial {
			t.Fatalf("informational-parent completeness=%q, want partial", val.Completeness)
		}
		if amount := b2aComponentAmount(t, val, outKey); amount == nil || amount.CanonicalString() != "21/0" {
			t.Fatalf("independent output amount=%v, want 21 retained", amount)
		}
		line := b2aComponentLine(t, val, nativeChild)
		if line == nil {
			t.Fatalf("native child line missing; lines=%+v", val.Lines)
		}
		if line.Status != economics.RatingLineRateMissing {
			t.Fatalf("native child status=%q, want missing rate; line=%+v", line.Status, line)
		}
	}

	customerVal, customerErr := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	assertInformationalChildNotExcused(t, customerVal, customerErr)

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	operatorVal, operatorErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	assertInformationalChildNotExcused(t, operatorVal, operatorErr)
}

// TestB2AAggregateOnlyPolicyUnselectedParentDoesNotExcuseChild is the
// policy-selection counterexample for finding R7-B2A-2. A local parent measure
// that loses the frozen retail source competition is still present in the raw
// evidence, but it is not in the effective selection mask and is never rated.
// Using its raw presence to excuse a selected child in the same scope silently
// drops the child's charge; the provider-owned parent in a different scope plus
// an independent output then make the valuation falsely complete.
func TestB2AAggregateOnlyPolicyUnselectedParentDoesNotExcuseChild(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	outKey := b1Key(metering.DirectionOutput, "vendor:output", metering.UnitToken)
	schemas := b1Schema(metering.RelationshipSubset, parent, child)
	tariff := b1Tariff(t, "b2a-unselected-parent", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "2"),
		b1Rule(t, "output-rate", outKey, "3"),
	}, schemas)
	resolved := b1Resolve(t, tariff)

	local := b2aObservationWith(t, "b2a-unselected-local", []metering.Measure{
		b1Measure(t, parent, "11"),
		b1Measure(t, child, "11"),
		b1Measure(t, outKey, "7"),
	})
	provider := b1Observation(t, "b2a-unselected-provider", "b-leg-1", metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "11"))

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{local, provider}), resolved)
	if err == nil {
		t.Fatalf("a policy-unselected parent must not excuse the selected child; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("unselected-parent completeness=%q, want partial", val.Completeness)
	}
	line := b2aComponentLine(t, val, child)
	if line == nil {
		t.Fatalf("selected child line missing; lines=%+v", val.Lines)
	}
	if line.Status != economics.RatingLineRateMissing {
		t.Fatalf("selected child status=%q, want missing rate; line=%+v", line.Status, line)
	}
	// The provider-owned parent is the effective aggregate participant and stays
	// payable; only the silently-dropped selected child is repaired.
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "22/0" {
		t.Fatalf("selected provider parent amount=%v, want 22", amount)
	}
}

// TestB2AAggregateOnlyExplicitZeroRateChildStaysComplete proves a child that
// carries an explicit zero-rate rule is a priced explicit-free line, not an
// excused missing rate and not an overlap: the aggregate-only total stays 49.
func TestB2AAggregateOnlyExplicitZeroRateChildStaysComplete(t *testing.T) {
	t.Parallel()
	children := b2aChildKeys()
	rules := append(b2aAggregateOnlyRules(t),
		b1Rule(t, "child-free-rate", children[1], "0"),
	)
	tariff := b1Tariff(t, "b2a-child-zero-rate", rules, b2aInclusionSchemas(metering.RelationshipSubset))
	resolved := b1Resolve(t, tariff)

	obs := b2aObservationWith(t, "b2a-child-zero-rate", b2aIntactMeasures(t))
	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err != nil {
		t.Fatalf("explicit zero-rate child must rate free, got %v; lines=%+v", err, val.Lines)
	}
	b2aAssertAggregateOnly(t, val)
	if amount := b2aComponentAmount(t, val, children[1]); amount != nil && amount.CanonicalString() != "0/0" {
		t.Fatalf("explicit zero-rate child amount=%v, want 0", amount)
	}
}

// TestB2AAggregateOnlyIndependentChildMissingRuleStaysPartial proves the
// inclusion decision is explicit: an unrelated component with no frozen
// relationship and no rule keeps the existing typed partial diagnostic rather
// than being silently excused.
func TestB2AAggregateOnlyIndependentChildMissingRuleStaysPartial(t *testing.T) {
	t.Parallel()
	parent := b2aParentInKey()
	independent := b1Key(metering.DirectionInput, "vendor:unrelated_part", metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind: metering.RelationshipSubset, Parent: parent, Child: b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken),
		}},
	}}
	tariff := b1Tariff(t, "b2a-independent-child", []economics.RatingRule{b1Rule(t, "parent-in-rate", parent, "2")}, schemas)
	resolved := b1Resolve(t, tariff)

	obs := b2aObservationWith(t, "b2a-independent-child", []metering.Measure{
		b1Measure(t, parent, "5"),
		b1Measure(t, b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken), "2"),
		b1Measure(t, independent, "4"),
	})
	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("an unrelated unpriced component must keep the typed partial diagnostic; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("independent child completeness=%q, want partial", val.Completeness)
	}
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "10/0" {
		t.Fatalf("aggregate parent amount=%v, want 10 retained alongside the partial", amount)
	}
	if amount := b2aComponentAmount(t, val, independent); amount != nil {
		t.Fatalf("unrelated unpriced component must not be fabricated, got %v", amount)
	}
}

// TestB2AAggregateOnlyChildInUncoveredScopeNotExcused proves the inclusion
// decision is scoped: the same included child in a B-leg/work scope that has no
// priced aggregate parent is not excused and keeps the existing partial.
func TestB2AAggregateOnlyChildInUncoveredScopeNotExcused(t *testing.T) {
	t.Parallel()
	parent := b2aParentInKey()
	child := b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}},
	}}
	tariff := b1Tariff(t, "b2a-scope-isolation", []economics.RatingRule{b1Rule(t, "parent-in-rate", parent, "2")}, schemas)
	resolved := b1Resolve(t, tariff)

	covered := b1Observation(t, "b2a-scope-covered", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "11"), b1Measure(t, child, "8"))
	uncovered := b1Observation(t, "b2a-scope-uncovered", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, child, "4"))

	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{covered, uncovered}), resolved)
	if err == nil {
		t.Fatalf("included child outside its parent scope must keep the partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("scope-isolation completeness=%q, want partial", val.Completeness)
	}
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "22/0" {
		t.Fatalf("covered-scope parent amount=%v, want 22", amount)
	}
}

// TestB2AAggregateOnlyLegacyNilSchemaUnchanged pins legacy behavior: without a
// frozen schema an unpriced sibling component is still a typed partial even
// with a priced aggregate parent. Inclusion is never inferred.
func TestB2AAggregateOnlyLegacyNilSchemaUnchanged(t *testing.T) {
	t.Parallel()
	parent := b2aParentInKey()
	child := b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken)
	tariff := b1Tariff(t, "b2a-legacy-nil-schema", []economics.RatingRule{b1Rule(t, "parent-in-rate", parent, "2")}, nil)
	resolved := b1Resolve(t, tariff)

	obs := b2aObservationWith(t, "b2a-legacy-nil-schema", []metering.Measure{
		b1Measure(t, parent, "11"), b1Measure(t, child, "8"),
	})
	val, err := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, []metering.Observation{obs}), resolved)
	if err == nil {
		t.Fatalf("legacy nil-schema tariff must keep the unpriced-child partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("legacy completeness=%q, want partial", val.Completeness)
	}
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "22/0" {
		t.Fatalf("legacy parent amount=%v, want 22", amount)
	}
}

// TestB2AAggregateOnlyBothPricedStillFailsClosed proves B2A does not weaken the
// B1 guarantee: a tariff that prices the aggregate parent and its included child
// still returns the typed overlap conflict with no payable quantity line.
func TestB2AAggregateOnlyBothPricedStillFailsClosed(t *testing.T) {
	t.Parallel()
	parent := b2aParentInKey()
	child := b1Key(metering.DirectionInput, b2aTextIn, metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}},
	}}
	tariff := b1Tariff(t, "b2a-both-priced", []economics.RatingRule{
		b1Rule(t, "parent-in-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, schemas)
	resolved := b1Resolve(t, tariff)

	obs := b2aObservationWith(t, "b2a-both-priced", []metering.Measure{
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"),
	})
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

func b2aComponentAmount(t *testing.T, val economics.Valuation, key metering.ComponentKey) *metering.Decimal {
	t.Helper()
	line := b2aComponentLine(t, val, key)
	if line == nil {
		return nil
	}
	return line.Amount
}

func b2aComponentLine(t *testing.T, val economics.Valuation, key metering.ComponentKey) *economics.LineItem {
	t.Helper()
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Direction == key.Direction && line.Component.Component == key.Component &&
			line.Component.Unit == key.Unit && line.Component.SchemaID == key.SchemaID {
			return line
		}
	}
	return nil
}

// b2aRetailObservation is the retail-path intact evidence. It carries the
// generated BillingCallID lineage so it is inside the frozen call selection.
func b2aRetailObservation(t *testing.T, callID billing.BillingCallID, id string, measures []metering.Measure) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1,
		StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-b1", ALegID: "a-b1", BillingCallID: callID.String(), BLegID: "b-leg-1"},
		Correlation: metering.CorrelationV2{StoreID: "store-b1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-b1", BLegID: "b-leg-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: b1SchemaID,
		Measures: measures,
	}
}

func b2aRetailCall(t *testing.T, callID billing.BillingCallID, observation metering.Observation) (billing.CallUsageRecord, billing.CallLegUsageRecord, billing.ChargePolicy) {
	t.Helper()
	policy := billing.ChargePolicy{
		Ref:                billing.VersionRef{ID: b2aRetailPolicyID, Version: "v1"},
		PricingRef:         billing.VersionRef{ID: b2aRetailTariffID, Version: "v1"},
		Scope:              billing.ChargeSurfacedTurn,
		IncludeInputTokens: true, IncludeOutputTokens: true,
		Retail: &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        callID, AccountID: "acct-1", ALegID: "a-b1",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: policy.PricingRef, ChargePolicyRef: policy.Ref,
		ExpectedBLegIDs: []string{"b-leg-1"},
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-b1", BLegID: "b-leg-1", AttemptSeq: 1,
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		EvidenceVersion: billing.EvidenceFormatVersionV2, EvidenceProjection: billing.EvidenceProjectionV1,
		Observations: []metering.Observation{observation},
	}
	return call, leg, policy
}

// b2aSupersessionStream is the shared source stream for the historical-null and
// effective-replacement pair. Supersession scope is stream-local, so both
// revisions must share it while keeping their own replay identity.
const b2aSupersessionStream = "b2a-supersession-stream"

// b2aSupersessionKeys are the three components of the supersession vector: an
// independently priced component X, a priced aggregate parent P, and P's
// included-but-unpriced child C. X and P are deliberately unrelated and priced
// at distinct rates so the literal effective total cannot coincide with a
// historical quantity or an aggregate sum by accident.
func b2aSupersessionKeys() (independent, parent, child metering.ComponentKey) {
	independent = b1Key(metering.DirectionInput, "vendor:independent_addon", metering.UnitToken)
	parent = b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child = b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	return
}

// b2aSupersessionTariff declares the aggregate-only inclusion edge P->C and
// prices only X (rate 5) and P (rate 2); C has no rule of its own.
func b2aSupersessionTariff(t *testing.T, independent, parent, child metering.ComponentKey) economics.TariffSnapshot {
	t.Helper()
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind: metering.RelationshipSubset, Parent: parent, Child: child,
		}},
	}}
	return b1Resolve(t, b1Tariff(t, "b2a-supersession", []economics.RatingRule{
		b1Rule(t, "independent-rate", independent, "5"),
		b1Rule(t, "parent-rate", parent, "2"),
	}, schemas))
}

// b2aSupersessionAggregate is the observation carrying a complete priced parent
// P and an included NULL child C. C is excluded by the frozen P->C edge, so it
// must not independently require a rate or poison the valuation.
func b2aSupersessionAggregate(t *testing.T, parent, child metering.ComponentKey) metering.Observation {
	t.Helper()
	return b2aObservationWith(t, "b2a-supersession-aggregate", []metering.Measure{
		b1Measure(t, parent, "11"),
		b1UnavailableMeasure(child),
	})
}

// b2aEffectiveReplacementPair builds a valid supersession graph: a historical
// delta observation whose independently priced component X is null, and an
// immutable replacement revision in the same source that supplies X complete.
// The replacement keeps the historical observation's replay-stable reference,
// including its payload hash, so the link resolves as immutable valid evidence.
func b2aEffectiveReplacementPair(t *testing.T, key metering.ComponentKey, quantity string) (metering.Observation, metering.Observation) {
	t.Helper()
	historical := b2aObservationWith(t, "b2a-supersession-historical", []metering.Measure{b1UnavailableMeasure(key)})
	historical.Revision = 1
	historical.Sequence = 1
	historical.StreamID = b2aSupersessionStream
	historicalRef, err := historical.Ref(historical.Subject.StoreID)
	if err != nil {
		t.Fatalf("historical ref: %v", err)
	}
	replacement := b2aObservationWith(t, "b2a-supersession-replacement", []metering.Measure{b1Measure(t, key, quantity)})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.Supersedes = []metering.ObservationRef{historicalRef}
	replacement.Revision = 2
	replacement.Sequence = 2
	replacement.StreamID = b2aSupersessionStream
	return historical, replacement
}

// b2aAssertAuditRefRetained proves the observation's original replay-stable
// reference is still part of the valuation's immutable input set. The fix must
// attribute incompleteness from effective state without dropping evidence.
func b2aAssertAuditRefRetained(t *testing.T, observation metering.Observation, val economics.Valuation) {
	t.Helper()
	want, err := observation.Ref(observation.Subject.StoreID)
	if err != nil {
		t.Fatalf("observation ref: %v", err)
	}
	for _, ref := range val.InputObservations {
		if ref.Equal(want) {
			return
		}
	}
	t.Fatalf("audit reference %+v missing from %+v", want, val.InputObservations)
}

// TestB2AAggregateOnlySupersededNullHistoryCannotPoisonEffectiveValuation is the
// R7-B2A supersession counterexample. A superseded historical observation still
// carries a null value for the independently priced component X, but an
// immutable valid replacement in the same source makes X complete. A separate
// observation carries the complete priced aggregate P with an included NULL
// child C that the frozen P->C edge excludes. Effective X and P are complete;
// only the excluded C makes the global reducer Complete=false. The
// reduction-completeness attribution must follow effective history: the
// superseded null X is not a live gap and must not emit a false
// ErrQuantityIncomplete/partial. The literal effective total is X(4*5) +
// P(11*2) = 42, and both the historical and replacement audit references stay
// retained.
func TestB2AAggregateOnlySupersededNullHistoryCannotPoisonEffectiveValuation(t *testing.T) {
	t.Parallel()
	independent, parent, child := b2aSupersessionKeys()
	historical, replacement := b2aEffectiveReplacementPair(t, independent, "4")
	aggregate := b2aSupersessionAggregate(t, parent, child)
	resolved := b2aSupersessionTariff(t, independent, parent, child)
	observations := []metering.Observation{historical, replacement, aggregate}

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, observations))
	if err != nil {
		t.Fatalf("superseded null history must not poison the effective valuation, got %v; lines=%+v", err, val.Lines)
	}
	if val.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "42/0" {
		t.Fatalf("effective total=%s, want 42 (X 4*5 + P 11*2)", total)
	}
	if amount := b2aComponentAmount(t, val, independent); amount == nil || amount.CanonicalString() != "20/0" {
		t.Fatalf("effective independent amount=%v, want 20", amount)
	}
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "22/0" {
		t.Fatalf("effective aggregate amount=%v, want 22", amount)
	}
	b2aAssertAuditRefRetained(t, historical, val)
	b2aAssertAuditRefRetained(t, replacement, val)

	// The selected-retail / customer-policy seam shares the same reduction and
	// must agree, proving the effective-history fix is not operator-only.
	retailVal, retailErr := billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, observations), resolved)
	if retailErr != nil {
		t.Fatalf("retail supersession rating must stay complete, got %v; lines=%+v", retailErr, retailVal.Lines)
	}
	if retailVal.Completeness != economics.CompletenessComplete {
		t.Fatalf("retail completeness=%q, want complete; lines=%+v", retailVal.Completeness, retailVal.Lines)
	}
	if total := b1PayableTotal(t, retailVal); total != "42/0" {
		t.Fatalf("retail effective total=%s, want 42", total)
	}
	b2aAssertAuditRefRetained(t, historical, retailVal)
	b2aAssertAuditRefRetained(t, replacement, retailVal)
}

// TestB2AAggregateOnlyEffectiveIndependentNullStillPartials is the control that
// the supersession fix must not weaken fail-closed behavior: with no resolving
// replacement the independently priced component X is an effective null, so the
// valuation stays partial even though the excluded child C is also present. The
// complete priced aggregate P remains payable alongside the diagnostic.
func TestB2AAggregateOnlyEffectiveIndependentNullStillPartials(t *testing.T) {
	t.Parallel()
	independent, parent, child := b2aSupersessionKeys()
	effectiveNull := b2aObservationWith(t, "b2a-supersession-historical", []metering.Measure{b1UnavailableMeasure(independent)})
	aggregate := b2aSupersessionAggregate(t, parent, child)
	resolved := b2aSupersessionTariff(t, independent, parent, child)

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{effectiveNull, aggregate}))
	if err == nil {
		t.Fatalf("an unresolved effective independent null must stay partial; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
	if amount := b2aComponentAmount(t, val, parent); amount == nil || amount.CanonicalString() != "22/0" {
		t.Fatalf("complete aggregate sibling amount=%v, want 22 retained", amount)
	}
}

// TestB2AAggregateOnlyUnresolvedSupersessionStillFailClosed is the control that
// a replacement naming an absent predecessor leaves a pending supersession;
// the rater must keep the typed failure rather than letting the effective-value
// attribution excuse an unresolved replay link.
func TestB2AAggregateOnlyUnresolvedSupersessionStillFailClosed(t *testing.T) {
	t.Parallel()
	independent, parent, child := b2aSupersessionKeys()
	pending := b2aObservationWith(t, "b2a-supersession-pending", []metering.Measure{b1Measure(t, independent, "4")})
	pending.Semantics = metering.SemanticsReplacement
	pending.Supersedes = []metering.ObservationRef{{
		StoreID: "store-b1", ObservationID: "b2a-absent-predecessor", Revision: 1, PayloadHash: strings.Repeat("a", 64),
	}}
	pending.Revision = 2
	pending.Sequence = 2
	pending.StreamID = b2aSupersessionStream
	aggregate := b2aSupersessionAggregate(t, parent, child)
	resolved := b2aSupersessionTariff(t, independent, parent, child)

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{pending, aggregate}))
	if err == nil {
		t.Fatalf("an unresolved supersession link must still fail closed; lines=%+v", val.Lines)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("completeness=%q, want partial", val.Completeness)
	}
}
