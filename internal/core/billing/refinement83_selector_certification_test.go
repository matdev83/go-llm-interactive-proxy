package billing

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestRefinement83 selector certification, subpass A: all-operator-payable
// B-leg COGS versus default winner-only and explicit retry-inclusive retail
// selection over direction-qualified multimodal quantities.
//
// One BillingCallID carries a failed retry, a parallel/race loser, a
// canceled-but-payable attempt, a surfaced winner, a customer-payer (BYOK)
// failed attempt, and a never-started shell. Operator COGS must include
// every operator-payable attributable B-leg regardless of surfaced outcome;
// default retail must select only the winner; the explicit retry-inclusive
// policy must select every attributable attempt. Cost pass-through and
// non-request allocation are deferred to subpass B.

const ref83Schema = "refinement83.cert.v1"

func ref83Key(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: ref83Schema}
}

var (
	ref83ImageIn  = ref83Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	ref83ImageOut = ref83Key(metering.DirectionOutput, metering.ComponentImage, metering.UnitImage)
	ref83AudioIn  = ref83Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	ref83AudioOut = ref83Key(metering.DirectionOutput, metering.ComponentAudio, metering.UnitSecond)
	ref83DocIn    = ref83Key(metering.DirectionInput, metering.ComponentDocument, metering.UnitPage)
)

type ref83Measure struct {
	key      metering.ComponentKey
	quantity string
}

func ref83Observation(t *testing.T, callID BillingCallID, bLegID, id string, payer metering.PaymentParty, chargeAmount string, measures ...ref83Measure) metering.Observation {
	t.Helper()
	items := make([]metering.Measure, 0, len(measures))
	for _, item := range measures {
		items = append(items, metering.Measure{Key: item.key, Value: phase10RetailDecimal(item.quantity), Quality: metering.QualityObserved})
	}
	amount := phase10RetailDecimal(chargeAmount)
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-83", ALegID: "a-83", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-83", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-83", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement83.cert.v1",
		Measures: items,
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "cogs-" + id, Kind: metering.ChargeKindAggregate, Payer: payer,
			Amount: amount, Currency: "USD",
		}},
	}
}

func ref83Leg(t *testing.T, callID BillingCallID, id string, seq int, outcome LegOutcome, surfaced SurfacedState, observation *metering.Observation) CallLegUsageRecord {
	t.Helper()
	leg := testCallLegUsageRecord(callID, id)
	leg.ALegID = "a-83"
	leg.AttemptSeq = seq
	leg.Outcome = outcome
	leg.Surfaced = surfaced
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	if observation != nil {
		leg.Observations = []metering.Observation{*observation}
	}
	return leg
}

func ref83ProxyObservation(t *testing.T, callID BillingCallID) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_001, 0).UTC()
	return metering.Observation{
		Version: 2, ID: "proxy-egress-83", SourceEventKey: "proxy-egress-83-source", Revision: 1, StreamID: "proxy-egress-83-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveCustomer, Boundary: metering.BoundaryFrontendEgress, Lifecycle: metering.LifecycleLogicalRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "store-83", BillingCallID: callID.String()},
		Correlation: metering.CorrelationV2{StoreID: "store-83", CallID: callID.String(), BillingCallID: callID.String()},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement83.proxy.v1",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionNone, Component: "proxy_egress_bytes", Unit: metering.UnitByte, SchemaID: "proxy.service.v1"},
			Value:   phase10RetailDecimal("1000"),
			Quality: metering.QualityObserved,
		}},
	}
}

// ref83Fixture builds one BillingCallID with a failed retry, a race loser, a
// canceled-but-payable attempt, a surfaced winner, a customer-payer failed
// attempt, and a never-started shell.
func ref83Fixture(t *testing.T, policy ChargePolicy) (CallUsageRecord, []CallLegUsageRecord) {
	t.Helper()
	callID := mustBillingCallID(t)
	operator := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	customer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer}
	obs := func(bLegID, id, charge string, payer metering.PaymentParty, measures ...ref83Measure) *metering.Observation {
		observation := ref83Observation(t, callID, bLegID, id, payer, charge, measures...)
		return &observation
	}
	legs := []CallLegUsageRecord{
		ref83Leg(t, callID, "b-retry", 1, LegOutcomeFailed, SurfacedNo,
			obs("b-retry", "obs-b-retry", "1.25", operator, ref83Measure{key: ref83ImageIn, quantity: "3"})),
		ref83Leg(t, callID, "b-loser", 2, LegOutcomeLoser, SurfacedNo,
			obs("b-loser", "obs-b-loser", "0.75", operator, ref83Measure{key: ref83ImageOut, quantity: "2"})),
		ref83Leg(t, callID, "b-canceled", 3, LegOutcomeCanceled, SurfacedNo,
			obs("b-canceled", "obs-b-canceled", "2.00", operator,
				ref83Measure{key: ref83AudioIn, quantity: "30"},
				ref83Measure{key: ref83AudioOut, quantity: "20"})),
		ref83Leg(t, callID, "b-winner", 4, LegOutcomeWinner, SurfacedYes,
			obs("b-winner", "obs-b-winner", "3.50", operator,
				ref83Measure{key: ref83ImageIn, quantity: "1"},
				ref83Measure{key: ref83ImageOut, quantity: "1"},
				ref83Measure{key: ref83AudioIn, quantity: "10"},
				ref83Measure{key: ref83AudioOut, quantity: "5"},
				ref83Measure{key: ref83DocIn, quantity: "2"})),
		ref83Leg(t, callID, "b-byok", 5, LegOutcomeFailed, SurfacedNo,
			obs("b-byok", "obs-b-byok", "5.00", customer, ref83Measure{key: ref83ImageIn, quantity: "1"})),
		ref83Leg(t, callID, "b-shell", 6, LegOutcomeNeverStarted, SurfacedNo, nil),
	}
	call := testCallUsageRecord(callID)
	call.ALegID = "a-83"
	call.AccountID = "acct-83"
	call.SubmissionID = "submission-83"
	call.ExpectedBLegIDs = []string{"b-retry", "b-loser", "b-canceled", "b-winner", "b-byok", "b-shell"}
	call.CustomerPricingRef = policy.PricingRef
	call.ChargePolicyRef = policy.Ref
	return call, legs
}

func ref83Tariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	return phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("image-input", ref83ImageIn, "0.10"),
		phase10RetailLinearRule("image-output", ref83ImageOut, "0.40"),
		phase10RetailLinearRule("audio-input", ref83AudioIn, "0.02"),
		phase10RetailLinearRule("audio-output", ref83AudioOut, "0.05"),
		phase10RetailLinearRule("document-input", ref83DocIn, "0.01"),
		phase10RetailFixedRule("call-fee", economics.FixedFeeScopeCall, "1"),
	})
}

func ref83ProxyTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	return phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "proxy-tariff-83", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{
		phase10RetailLinearRule("egress", metering.ComponentKey{Direction: metering.DirectionNone, Component: "proxy_egress_bytes", Unit: metering.UnitByte, SchemaID: "proxy.service.v1"}, "0.001"),
	})
}

func ref83LegKey(call CallUsageRecord, bLegID string) string {
	return call.CallID.String() + ":" + bLegID
}

//nolint:revive // test helper keeps t first per Go testing convention
func ref83Rate(t *testing.T, ctx context.Context, call CallUsageRecord, legs []CallLegUsageRecord, selection RetailSelectionResult, policy ChargePolicy, withProxy bool) RetailRatingResult {
	t.Helper()
	in := RetailRatingInput{
		Call: call, Legs: legs, Selection: selection, Policy: policy,
		Tariff: ref83Tariff(t), Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	}
	if withProxy {
		in.ProxyService = &RetailProxyServiceInput{
			Tariff: ref83ProxyTariff(t), Observations: []metering.Observation{ref83ProxyObservation(t, call.CallID)},
		}
	}
	result, err := RateSelectedRetailBLegs(ctx, in)
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	return result
}

func ref83WinnerPolicy() ChargePolicy {
	return retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
}

func ref83InclusivePolicy() ChargePolicy {
	return retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
}

func TestRefinement83OperatorCOGSIncludesEveryPayableBLeg(t *testing.T) {
	t.Parallel()
	call, legs := ref83Fixture(t, ref83WinnerPolicy())
	got, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal != (Money{Nano: 7_500_000_000, Currency: "USD"}) {
		t.Fatalf("operator COGS subtotal = %+v, want 7.50 USD (1.25+0.75+2.00+3.50)", got.KnownSubtotal)
	}
	if got.Completeness != CostCompletenessKnown || !got.Payable {
		t.Fatalf("operator COGS = %+v, want known payable attribution", got)
	}
	// IncludedLegKeys are sorted by the production authority; the set, not
	// the order of attempts, is the certified property.
	wantIncluded := []string{
		ref83LegKey(call, "b-canceled"), ref83LegKey(call, "b-loser"),
		ref83LegKey(call, "b-retry"), ref83LegKey(call, "b-winner"),
	}
	if len(got.IncludedLegKeys) != len(wantIncluded) {
		t.Fatalf("included legs = %v, want %v", got.IncludedLegKeys, wantIncluded)
	}
	for i, want := range wantIncluded {
		if got.IncludedLegKeys[i] != want {
			t.Fatalf("included legs = %v, want %v", got.IncludedLegKeys, wantIncluded)
		}
	}
	wantExcluded := []string{ref83LegKey(call, "b-byok"), ref83LegKey(call, "b-shell")}
	if len(got.ExcludedLegKeys) != len(wantExcluded) {
		t.Fatalf("excluded legs = %v, want customer-payer BYOK plus never-started shell %v", got.ExcludedLegKeys, wantExcluded)
	}
	for i, want := range wantExcluded {
		if got.ExcludedLegKeys[i] != want {
			t.Fatalf("excluded legs = %v, want %v", got.ExcludedLegKeys, wantExcluded)
		}
	}
	if len(got.UnknownLegKeys) != 0 || len(got.PendingCoverage) != 0 {
		t.Fatalf("unknown/pending = %v/%v, want every payable leg explained", got.UnknownLegKeys, got.PendingCoverage)
	}
	// Every included amount stays explainable from its immutable observation:
	// each leg observation fingerprints and resolves a replay-stable ref.
	for _, leg := range legs[:4] {
		observation := leg.Observations[0]
		if observation.Fingerprint() == "" {
			t.Fatalf("leg %q observation has no fingerprint", leg.BLegID)
		}
		ref, err := observation.Ref("store-83")
		if err != nil {
			t.Fatalf("leg %q observation ref: %v", leg.BLegID, err)
		}
		if ref.ObservationID != observation.ID || ref.Revision != observation.Revision {
			t.Fatalf("leg %q ref %+v does not identify observation %q", leg.BLegID, ref, observation.ID)
		}
	}
}

func TestRefinement83DefaultRetailSelectsWinnerOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := ref83WinnerPolicy()
	call, legs := ref83Fixture(t, policy)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("selected B-legs = %+v, want only the surfaced winner", selection.SelectedBLegs)
	}
	if selection.PolicyRef != policy.Ref || selection.Mode != RetailSelectionSurfacedWinner ||
		selection.Reason != RetailSelectionSurfacedWinner || selection.Basis != RetailBasisIndependent {
		t.Fatalf("selection = %+v, want frozen winner-only policy identity", selection)
	}
	if selection.Completeness != RetailSelectionComplete || selection.Capability != RetailCapabilityV2BLegObservations {
		t.Fatalf("selection = %+v, want complete V2 evidence", selection)
	}
	winnerRef, err := legs[3].Observations[0].Ref("store-83")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 1 || selection.ObservationRefs[0] != winnerRef {
		t.Fatalf("selection refs = %+v, want exactly the winner observation ref %+v", selection.ObservationRefs, winnerRef)
	}
	result := ref83Rate(t, ctx, call, legs, selection, policy, true)
	// Winner quantities: 1x0.10 + 1x0.40 + 10x0.02 + 5x0.05 + 2x0.01 = 0.97,
	// plus the once-per-call 1.00 fee and the 1.00 proxy line.
	if result.CustomerCharge != (Money{Nano: 2_970_000_000, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 2.97 USD", result.CustomerCharge)
	}
	// Five quantity lines plus the once-per-call fee plus the separately
	// named proxy-service line, all visible in the combined valuation.
	if len(result.Valuation.Lines) != 7 {
		t.Fatalf("retail lines = %d, want five quantity plus one call-fee plus one proxy line: %+v", len(result.Valuation.Lines), result.Valuation.Lines)
	}
	kinds := make(map[string]int)
	for _, line := range result.Valuation.Lines {
		kinds[line.ChargeKind]++
	}
	if kinds[RetailChargeKindInferenceUsage] != 5 || kinds[RetailChargeKindCommercialFee] != 1 || kinds[RetailChargeKindProxyService] != 1 {
		t.Fatalf("retail line kinds = %v, want 5 inference + 1 commercial + 1 proxy", kinds)
	}
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind != RetailChargeKindInferenceUsage {
			continue
		}
		for _, ref := range line.SourceObservationRefs {
			if ref.ObservationID != "obs-b-winner" {
				t.Fatalf("unselected B-leg leaked into retail line: %+v", line)
			}
		}
	}
	if result.ProxyServiceValuation == nil || len(result.ProxyServiceValuation.Lines) != 1 {
		t.Fatalf("proxy valuation = %+v, want one separate service line", result.ProxyServiceValuation)
	}
	proxyLine := result.ProxyServiceValuation.Lines[0]
	if proxyLine.ChargeKind != RetailChargeKindProxyService || proxyLine.Component == nil || proxyLine.Component.Component != "proxy_egress_bytes" {
		t.Fatalf("proxy line = %+v, want separately named proxy service line, never provider inference usage", proxyLine)
	}
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind == RetailChargeKindInferenceUsage && line.Component != nil && line.Component.Component == "proxy_egress_bytes" {
			t.Fatalf("proxy meter substituted for inference usage: %+v", line)
		}
	}
	if result.Valuation.Totals[0].RoundedAmount.NanoUnits != result.CustomerCharge.Nano || !result.Valuation.Totals[0].RoundedAmount.Present {
		t.Fatalf("summary = %+v, charge = %+v; rounded summary must equal detail total", result.Valuation.Totals, result.CustomerCharge)
	}
	replay := ref83Rate(t, ctx, call, legs, selection, policy, true)
	if replay.Fingerprint != result.Fingerprint || replay.Valuation.ID != result.Valuation.ID || replay.CustomerCharge != result.CustomerCharge {
		t.Fatalf("replay changed result: first=%+v replay=%+v", result, replay)
	}
}

func TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := ref83InclusivePolicy()
	call, legs := ref83Fixture(t, policy)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"b-retry", "b-loser", "b-canceled", "b-winner", "b-byok"}
	if len(selection.SelectedBLegs) != len(wantIDs) {
		t.Fatalf("selected B-legs = %+v, want every attributable attempt", selection.SelectedBLegs)
	}
	for i, want := range wantIDs {
		if selection.SelectedBLegs[i].BLegID != want {
			t.Fatalf("selected B-legs = %+v, want attempt order %v", selection.SelectedBLegs, wantIDs)
		}
	}
	if selection.PolicyRef != policy.Ref || selection.Mode != RetailSelectionAllAttributable || selection.Basis != RetailBasisIndependent {
		t.Fatalf("selection = %+v, want frozen retry-inclusive policy identity", selection)
	}
	if selection.Reason != RetailSelectionAllAttributable {
		t.Fatalf("selection reason = %q, want the frozen retry-inclusive mode", selection.Reason)
	}
	if selection.Completeness != RetailSelectionComplete || selection.Capability != RetailCapabilityV2BLegObservations {
		t.Fatalf("selection = %+v, want complete V2 evidence", selection)
	}
	wantRefs := make([]metering.ObservationRef, 0, len(wantIDs))
	for _, leg := range legs[:5] {
		ref, err := leg.Observations[0].Ref("store-83")
		if err != nil {
			t.Fatal(err)
		}
		wantRefs = append(wantRefs, ref)
	}
	if len(selection.ObservationRefs) != len(wantRefs) {
		t.Fatalf("selection refs = %+v, want one ref per attributable attempt", selection.ObservationRefs)
	}
	for i := range wantRefs {
		if selection.ObservationRefs[i] != wantRefs[i] {
			t.Fatalf("selection ref %d = %+v, want %+v", i, selection.ObservationRefs[i], wantRefs[i])
		}
	}
	result := ref83Rate(t, ctx, call, legs, selection, policy, true)
	// Winner 0.97 plus retry 0.30, loser 0.80, canceled 1.60, BYOK 0.10 = 3.77
	// quantities, plus the once-per-call 1.00 fee and the 1.00 proxy line.
	// The call fee is not multiplied by the five selected B-legs.
	if result.CustomerCharge != (Money{Nano: 5_770_000_000, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 5.77 USD", result.CustomerCharge)
	}
	if got := phase10RetailLineCount(result.Valuation.Lines, RetailChargeKindCommercialFee, "call-fee"); got != 1 {
		t.Fatalf("call fee line count = %d, want exactly one", got)
	}
	seen := make(map[string]struct{})
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind != RetailChargeKindInferenceUsage {
			continue
		}
		for _, ref := range line.SourceObservationRefs {
			seen[ref.ObservationID] = struct{}{}
		}
	}
	for _, want := range []string{"obs-b-retry", "obs-b-loser", "obs-b-canceled", "obs-b-winner", "obs-b-byok"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("attributable observation %q missing from retail lines: %v", want, seen)
		}
	}
	reselected, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatalf("reselect: %v", err)
	}
	ref83RequireSameSelection(t, selection, reselected)
	rerated := ref83Rate(t, ctx, call, legs, reselected, policy, true)
	if rerated.Fingerprint != result.Fingerprint || rerated.Valuation.ID != result.Valuation.ID || rerated.CustomerCharge != result.CustomerCharge {
		t.Fatalf("replay changed retry-inclusive rating: first=%+v replay=%+v", result, rerated)
	}
}

func TestRefinement83RateCallSettlesWinnerOnlyThroughSelection(t *testing.T) {
	t.Parallel()
	policy := ref83WinnerPolicy()
	call, legs := ref83Fixture(t, policy)
	tariff := ref83Tariff(t)
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	// Same winner quantities plus the call fee as the direct rating path, but
	// without the proxy-service input used in the dedicated retail test.
	if result.CustomerCharge != (Money{Nano: 1_970_000_000, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 1.97 USD through the settlement seam", result.CustomerCharge)
	}
	if result.Fingerprint == "" {
		t.Fatal("settlement result carries no fingerprint")
	}
	again, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("RateCall replay: %v", err)
	}
	if again.Fingerprint != result.Fingerprint || again.CustomerCharge != result.CustomerCharge {
		t.Fatalf("replay changed settlement: first=%+v replay=%+v", result, again)
	}
}

func TestRefinement83AuthoritiesStaySeparateAndCloneStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := ref83WinnerPolicy()
	call, legs := ref83Fixture(t, policy)
	cogs, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	rate := func(legs []CallLegUsageRecord) RetailRatingResult {
		t.Helper()
		return ref83Rate(t, ctx, call, legs, selection, policy, false)
	}
	retail := rate(legs)

	// Mutating an unselected retry quantity changes neither operator COGS
	// (which reads charges, not measures) nor default winner-only retail
	// (which never selects the retry leg).
	quantityChanged := append([]CallLegUsageRecord(nil), legs...)
	quantityChanged[0] = quantityChanged[0].Clone()
	quantityChanged[0].Observations[0].Measures[0].Value = phase10RetailDecimal("999")
	if again, err := AttributeOperatorCOGS(quantityChanged, nil, "USD"); err != nil {
		t.Fatalf("COGS after quantity mutation: %v", err)
	} else if again.KnownSubtotal != cogs.KnownSubtotal {
		t.Fatalf("quantity mutation changed COGS: was %+v now %+v", cogs.KnownSubtotal, again.KnownSubtotal)
	}
	if mutated := rate(quantityChanged); mutated.CustomerCharge != retail.CustomerCharge {
		t.Fatalf("unselected retry quantity changed default retail: was %+v now %+v", retail.CustomerCharge, mutated.CustomerCharge)
	}

	// Mutating an unselected retry provider charge changes COGS but never
	// independent default retail.
	chargeChanged := append([]CallLegUsageRecord(nil), legs...)
	chargeChanged[0] = chargeChanged[0].Clone()
	chargeChanged[0].Observations[0].Charges[0].Amount = phase10RetailDecimal("9.99")
	if again, err := AttributeOperatorCOGS(chargeChanged, nil, "USD"); err != nil {
		t.Fatalf("COGS after charge mutation: %v", err)
	} else if again.KnownSubtotal == cogs.KnownSubtotal {
		t.Fatalf("charge mutation did not change COGS: %v", again.KnownSubtotal)
	}
	if mutated := rate(chargeChanged); mutated.CustomerCharge != retail.CustomerCharge {
		t.Fatalf("unselected retry charge changed default retail: was %+v now %+v", retail.CustomerCharge, mutated.CustomerCharge)
	}

	// Clone stability: detached copies reselect and re-rate identically.
	clonedCall := call
	clonedLegs := append([]CallLegUsageRecord(nil), legs...)
	for i := range clonedLegs {
		clonedLegs[i] = clonedLegs[i].Clone()
	}
	reselected, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: clonedCall, Legs: clonedLegs, Policy: policy})
	if err != nil {
		t.Fatalf("reselect on clones: %v", err)
	}
	ref83RequireSameSelection(t, selection, reselected)
	cloned := selection.Clone()
	rerated := ref83Rate(t, ctx, clonedCall, clonedLegs, cloned, policy, false)
	if rerated.Fingerprint != retail.Fingerprint || rerated.Valuation.ID != retail.Valuation.ID || rerated.CustomerCharge != retail.CustomerCharge {
		t.Fatalf("clone changed rating: first=%+v cloned=%+v", retail, rerated)
	}
}

func ref83RequireSameSelection(t *testing.T, want, got RetailSelectionResult) {
	t.Helper()
	if got.CallID != want.CallID || got.PolicyRef != want.PolicyRef || got.Mode != want.Mode ||
		got.Reason != want.Reason || got.Basis != want.Basis || got.Completeness != want.Completeness ||
		got.Capability != want.Capability {
		t.Fatalf("selection identity changed: want=%+v got=%+v", want, got)
	}
	if len(got.SelectedBLegs) != len(want.SelectedBLegs) {
		t.Fatalf("selected legs changed: want=%+v got=%+v", want.SelectedBLegs, got.SelectedBLegs)
	}
	for i := range want.SelectedBLegs {
		wantLeg, gotLeg := want.SelectedBLegs[i], got.SelectedBLegs[i]
		if gotLeg.CallID != wantLeg.CallID || gotLeg.ALegID != wantLeg.ALegID || gotLeg.BLegID != wantLeg.BLegID ||
			gotLeg.AttemptSeq != wantLeg.AttemptSeq || gotLeg.Outcome != wantLeg.Outcome || gotLeg.Surfaced != wantLeg.Surfaced ||
			len(gotLeg.ObservationRefs) != len(wantLeg.ObservationRefs) {
			t.Fatalf("selected leg %d changed: want=%+v got=%+v", i, wantLeg, gotLeg)
		}
		for j := range wantLeg.ObservationRefs {
			if gotLeg.ObservationRefs[j] != wantLeg.ObservationRefs[j] {
				t.Fatalf("selected leg %d ref %d changed: want=%+v got=%+v", i, j, wantLeg.ObservationRefs[j], gotLeg.ObservationRefs[j])
			}
		}
	}
	if len(got.ObservationRefs) != len(want.ObservationRefs) {
		t.Fatalf("selection refs changed: want=%+v got=%+v", want.ObservationRefs, got.ObservationRefs)
	}
	for i := range want.ObservationRefs {
		if got.ObservationRefs[i] != want.ObservationRefs[i] {
			t.Fatalf("selection ref %d changed: want=%+v got=%+v", i, want.ObservationRefs[i], got.ObservationRefs[i])
		}
	}
}
