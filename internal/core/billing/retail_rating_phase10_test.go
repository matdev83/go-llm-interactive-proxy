package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase10RetailRatingUsesFrozenSelectionIndependentTariffAndSingleScopeFees(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-retry", "b-winner")
	call.SubmissionID = "submission-1"
	winner := phase10RetailLeg(t, call.CallID, "b-winner", 2, LegOutcomeWinner, SurfacedYes,
		phase10RetailObservation(t, call.CallID, "b-winner", "winner-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "2"},
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "3"},
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, quantity: "4"},
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, quantity: "5"},
		))
	retry := phase10RetailLeg(t, call.CallID, "b-retry", 1, LegOutcomeFailed, SurfacedNo,
		phase10RetailObservation(t, call.CallID, "b-retry", "retry-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "100"},
		))
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{retry, winner}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("text-input", metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, "0.50"),
		phase10RetailLinearRule("cache-read", metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, "0.25"),
		phase10RetailLinearRule("image-input", metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, "2"),
		phase10RetailLinearRule("image-output", metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, "3"),
		phase10RetailFixedRule("call-fee", economics.FixedFeeScopeCall, "4"),
		phase10RetailFixedRule("submission-fee", economics.FixedFeeScopeSubmission, "6"),
	})
	in := RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{retry, winner}, Selection: selection,
		Policy: policy, Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	}
	first, err := RateSelectedRetailBLegs(context.Background(), in)
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	if got, want := first.CustomerCharge.Nano, int64(34_750_000_000); got != want {
		t.Fatalf("customer charge = %d, want %d", got, want)
	}
	if len(first.Valuation.Lines) != 6 {
		t.Fatalf("retail lines = %d, want four quantity plus two fixed lines: %+v", len(first.Valuation.Lines), first.Valuation.Lines)
	}
	if got := phase10RetailLineCount(first.Valuation.Lines, RetailChargeKindCommercialFee, "call-fee"); got != 1 {
		t.Fatalf("call fee line count = %d, want one", got)
	}
	if got := phase10RetailLineCount(first.Valuation.Lines, RetailChargeKindCommercialFee, "submission-fee"); got != 1 {
		t.Fatalf("submission fee line count = %d, want one", got)
	}
	for _, line := range first.Valuation.Lines {
		if line.ChargeKind == RetailChargeKindInferenceUsage && len(line.SourceObservationRefs) != 1 {
			t.Fatalf("inference line refs = %+v, want only selected winner", line)
		}
		for _, ref := range line.SourceObservationRefs {
			if ref.ObservationID == "retry-usage" {
				t.Fatalf("unselected retry leaked into retail line: %+v", line)
			}
		}
	}
	if first.Valuation.Totals[0].RoundedAmount.NanoUnits != first.CustomerCharge.Nano || !first.Valuation.Totals[0].RoundedAmount.Present {
		t.Fatalf("summary = %+v, charge = %+v; rounded summary must equal detail total", first.Valuation.Totals, first.CustomerCharge)
	}
	second, err := RateSelectedRetailBLegs(context.Background(), in)
	if err != nil {
		t.Fatalf("replay rating: %v", err)
	}
	if first.Fingerprint != second.Fingerprint || first.Valuation.ID != second.Valuation.ID || first.CustomerCharge != second.CustomerCharge {
		t.Fatalf("replay changed result: first=%+v second=%+v", first, second)
	}

	// Provider tariff/cost readiness is outside this independent customer
	// valuation. Changing an unselected retry quantity also cannot change it.
	mutated := in
	mutated.Legs = append([]CallLegUsageRecord(nil), in.Legs...)
	mutated.Legs[0] = mutated.Legs[0].Clone()
	mutated.Legs[0].Observations[0].Measures[0].Value = phase10RetailDecimal("999")
	unchanged, err := RateSelectedRetailBLegs(context.Background(), mutated)
	if err != nil {
		t.Fatalf("unselected retry mutation: %v", err)
	}
	if unchanged.CustomerCharge != first.CustomerCharge || unchanged.Valuation.ID != first.Valuation.ID {
		t.Fatalf("unselected retry changed independent retail: first=%+v mutated=%+v", first.CustomerCharge, unchanged.CustomerCharge)
	}

	changedTariff := tariff.Clone()
	changedTariff.Rules[0].UnitPrice = phase10RetailDecimal("1")
	changedTariff.Content = economics.SnapshotContentRef{}
	changedTariff, err = changedTariff.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	mutated.Tariff = changedTariff
	changed, err := RateSelectedRetailBLegs(context.Background(), mutated)
	if err != nil {
		t.Fatalf("independent tariff mutation: %v", err)
	}
	if changed.CustomerCharge == first.CustomerCharge {
		t.Fatalf("customer tariff mutation did not change retail charge: %v", changed.CustomerCharge)
	}
}

func TestPhase10RetailRatingKeepsProxyServiceMetersSeparateFromInference(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-1"
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes,
		phase10RetailObservation(t, call.CallID, "b-winner", "provider-image", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, quantity: "2"},
		))
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	proxy := phase10RetailProxyObservation(t, call.CallID, "customer-egress", "12", metering.ComponentKey{Direction: metering.DirectionNone, Component: "proxy_egress_bytes", Unit: metering.UnitByte, SchemaID: "proxy.service.v1"})
	inferenceTariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("image", metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, "2"),
	})
	proxyTariff := phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "proxy-tariff", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{
		phase10RetailLinearRule("egress", metering.ComponentKey{Direction: metering.DirectionNone, Component: "proxy_egress_bytes", Unit: metering.UnitByte, SchemaID: "proxy.service.v1"}, "0.10"),
	})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: inferenceTariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		ProxyService: &RetailProxyServiceInput{Tariff: proxyTariff, Observations: []metering.Observation{proxy}},
	})
	if err != nil {
		t.Fatalf("proxy rating: %v", err)
	}
	if result.ProxyServiceValuation == nil || len(result.ProxyServiceValuation.Lines) != 1 {
		t.Fatalf("proxy valuation = %+v, want one separate service line", result.ProxyServiceValuation)
	}
	proxyLine := result.ProxyServiceValuation.Lines[0]
	if proxyLine.ChargeKind != RetailChargeKindProxyService || proxyLine.Component == nil || proxyLine.Component.Component != "proxy_egress_bytes" {
		t.Fatalf("proxy line = %+v, want separately named proxy service line", proxyLine)
	}
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind == RetailChargeKindInferenceUsage && line.Component != nil && line.Component.Component == "proxy_egress_bytes" {
			t.Fatalf("proxy meter substituted for inference usage: %+v", line)
		}
	}
	if got, want := result.CustomerCharge.Nano, int64(4_000_000_000+1_200_000_000); got != want {
		t.Fatalf("inference plus proxy charge = %d, want %d", got, want)
	}
	_ = callID // keep the generated identity visibly independent of proxy observation IDs.
}

func TestPhase10RetailRatingMissingRateOrSubmissionIdentityIsTypedAndNonPayable(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-1"
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes,
		phase10RetailObservation(t, call.CallID, "b-winner", "unpriced-image", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, quantity: "1"},
		))
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailFixedRule("submission-fee", economics.FixedFeeScopeSubmission, "6"),
	})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if !errors.Is(err, ErrRetailRateIncomplete) || result.Valuation.Completeness == economics.CompletenessComplete {
		t.Fatalf("missing quantity rate error/result = %v / %+v, want typed incomplete non-payable valuation", err, result.Valuation)
	}
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind == RetailChargeKindInferenceUsage && line.RoundedAmount != nil && line.RoundedAmount.Present {
			t.Fatalf("missing-rate line unexpectedly payable: %+v", line)
		}
	}

	call.SubmissionID = ""
	call.ExpectedBLegIDs = []string{"b-winner"}
	selection, err = SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if !errors.Is(err, ErrRetailSubmissionIdentityMissing) {
		t.Fatalf("missing submission identity error = %v, want %v", err, ErrRetailSubmissionIdentityMissing)
	}
	_ = callID
}

func TestPhase10RetailRatingMissingSelectedQuantityIsNotZero(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-1"
	observation := phase10RetailObservation(t, call.CallID, "b-winner", "missing-quantity", metering.OriginProvider, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "retail.v1"}, quantity: "1"})
	observation.Measures[0].Value = nil
	observation.Measures[0].Quality = metering.QualityUnavailable
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, observation)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("text-input", metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, "0.50"),
	})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if !errors.Is(err, ErrRetailRateIncomplete) || !errors.Is(err, ErrQuantityIncomplete) {
		t.Fatalf("charge-only rating error = %v, want typed incomplete quantity", err)
	}
	if result.CustomerCharge != (Money{}) || result.Valuation.Completeness == economics.CompletenessComplete {
		t.Fatalf("charge-only rating became payable/complete: charge=%+v valuation=%+v", result.CustomerCharge, result.Valuation)
	}
	if len(result.Valuation.InputObservations) != 1 || result.Valuation.InputObservations[0].ObservationID != "missing-quantity" {
		t.Fatalf("charge-only selected ref was not retained: %+v", result.Valuation.InputObservations)
	}
}

func TestPhase10RetailRatingSeparatesSelectedModelTariffsWithoutDuplicateLines(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-a", "b-b")
	first := phase10RetailLeg(t, call.CallID, "b-a", 1, LegOutcomeFailed, SurfacedNo,
		phase10RetailObservation(t, call.CallID, "b-a", "model-a-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "1"}))
	first.BackendID, first.ModelID = "backend-a", "model-a"
	second := phase10RetailLeg(t, call.CallID, "b-b", 2, LegOutcomeWinner, SurfacedYes,
		phase10RetailObservation(t, call.CallID, "b-b", "model-b-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "1"}))
	second.BackendID, second.ModelID = "backend-b", "model-b"
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{first, second}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	defaultTariff := phase10RetailTariff(t, nil)
	modelATariff := phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "model-a-pricing", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{phase10RetailLinearRule("text-a", key, "0.50")})
	modelBTariff := phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "model-b-pricing", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{phase10RetailLinearRule("text-b", key, "2")})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{first, second}, Selection: selection, Policy: policy,
		Tariff: defaultTariff, ModelTariffs: []ModelCustomerTariff{
			{BackendID: "backend-a", ModelID: "model-a", Tariff: modelATariff},
			{BackendID: "backend-b", ModelID: "model-b", Tariff: modelBTariff},
		}, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("model tariff rating: %v", err)
	}
	if got, want := result.CustomerCharge.Nano, int64(2_500_000_000); got != want {
		t.Fatalf("model tariff charge = %d, want %d", got, want)
	}
	if len(result.InferenceValuation.Lines) != 2 || result.InferenceValuation.Lines[0].ID == result.InferenceValuation.Lines[1].ID {
		t.Fatalf("model tariff lines = %+v, want two distinct line identities", result.InferenceValuation.Lines)
	}
}

type phase10RetailMeasure struct {
	key      metering.ComponentKey
	quantity string
}

func phase10RetailObservation(t *testing.T, callID BillingCallID, bLegID, id, origin string, boundary metering.Boundary, measures ...phase10RetailMeasure) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionProviderResponse
	if origin == metering.OriginLocal {
		acquisition = metering.AcquisitionLocalTransport
	}
	items := make([]metering.Measure, 0, len(measures))
	for _, item := range measures {
		items = append(items, metering.Measure{Key: item.key, Value: phase10RetailDecimal(item.quantity), Quality: metering.QualityObserved})
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: boundary, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase10:retail:v1", Measures: items,
	}
}

func phase10RetailProxyObservation(t *testing.T, callID BillingCallID, id, quantity string, key metering.ComponentKey) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_001, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveCustomer,
		Boundary: metering.BoundaryFrontendEgress, Lifecycle: metering.LifecycleLogicalRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "store-1", BillingCallID: callID.String()},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String()},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase10:proxy:v1",
		Measures: []metering.Measure{{Key: key, Value: phase10RetailDecimal(quantity), Quality: metering.QualityObserved}},
	}
}

func phase10RetailLeg(t *testing.T, callID BillingCallID, id string, seq int, outcome LegOutcome, surfaced SurfacedState, observation metering.Observation) CallLegUsageRecord {
	t.Helper()
	leg := phase5CostLeg(t, callID, id, seq, outcome, surfaced, nil)
	leg.ALegID = "a-1"
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	leg.Observations = []metering.Observation{observation}
	return leg
}

func phase10RetailTariff(t *testing.T, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	return phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"}, rules)
}

func phase10RetailTariffWithRef(t *testing.T, ref economics.RatingSnapshotRef, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshot(ref, "USD", rules)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func phase10RetailLinearRule(id string, key metering.ComponentKey, price string) economics.RatingRule {
	return economics.RatingRule{ID: id, Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: phase10RetailDecimal(price)}
}

func phase10RetailFixedRule(id string, scope economics.FixedFeeScope, amount string) economics.RatingRule {
	return economics.RatingRule{ID: id, Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: phase10RetailDecimal(amount), FixedScope: scope}
}

func phase10RetailDecimal(value string) *metering.Decimal {
	d, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &d
}

func phase10RetailLineCount(lines []economics.LineItem, kind, ruleID string) int {
	count := 0
	for _, line := range lines {
		if line.ChargeKind == kind && line.RuleID == ruleID {
			count++
		}
	}
	return count
}
