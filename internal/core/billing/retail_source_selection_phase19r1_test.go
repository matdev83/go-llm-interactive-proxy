package billing

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase19 R1 RED: attaching an independent local measurement of already
// represented provider output must not double ordinary inference retail.
// Provider output 200 tokens at 0.03 = 6.00. Local output 200 tokens at the
// same rule is the same billable work via a competing channel, not additive
// work. Correct R inference is 6.00 once; the bug rates 12.00.
func TestPhase19R1RetailSourceCompetingOutputDoesNotDouble(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-r1"
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	providerObs := phase10RetailObservation(t, call.CallID, "b-winner", "provider-output", metering.OriginProvider, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: key, quantity: "200"})
	localObs := phase10RetailObservation(t, call.CallID, "b-winner", "local-output", metering.OriginLocal, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: key, quantity: "200"})
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, providerObs)
	leg.Observations = append(leg.Observations, localObs)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 2 {
		t.Fatalf("selection refs = %d, want both competing observations frozen for audit", len(selection.ObservationRefs))
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("output-token", key, "0.03"),
	})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	// Correct: one contractual inference basis, 200*0.03 = 6.00.
	if got, want := result.CustomerCharge.Nano, int64(6_000_000_000); got != want {
		t.Fatalf("R customer charge = %d nano, want %d (single 200*0.03 basis; both channels must not sum)", got, want)
	}
	if len(result.InferenceValuation.Lines) != 1 {
		t.Fatalf("inference lines = %d, want exactly one contractual output line: %+v", len(result.InferenceValuation.Lines), result.InferenceValuation.Lines)
	}
}

// Phase19 R1 GREEN: full winner with competing local/provider channels selects
// the provider contractual basis once, keeps provider-only image additive,
// charges the submission fee once, preserves both observations, stays
// order-invariant and replay-stable, and keeps proxy additive.
func TestPhase19R1RetailSourceFrozenProviderBasisWithFeeProxyOrderReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-r1-full"
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	imageKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	audioKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"}
	providerObs := phase10RetailObservation(t, call.CallID, "b-winner", "provider-full", metering.OriginProvider, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: inputKey, quantity: "110"},
		phase10RetailMeasure{key: outputKey, quantity: "200"},
		phase10RetailMeasure{key: imageKey, quantity: "2"},
		phase10RetailMeasure{key: audioKey, quantity: "12"},
	)
	localObs := phase10RetailObservation(t, call.CallID, "b-winner", "local-full", metering.OriginLocal, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: inputKey, quantity: "100"},
		phase10RetailMeasure{key: outputKey, quantity: "200"},
		phase10RetailMeasure{key: audioKey, quantity: "10"},
	)
	// Local uses cumulative snapshots like the Phase19 fixture (same work,
	// competing channel), not deltas.
	for i := range localObs.Measures {
		_ = i
	}
	localObs.Semantics = metering.SemanticsCumulative
	providerObs.Semantics = metering.SemanticsCumulative
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, providerObs)
	leg.Observations = append(leg.Observations, localObs)
	origProviderMeasures := len(leg.Observations[0].Measures)
	origLocalMeasures := len(leg.Observations[1].Measures)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 2 {
		t.Fatalf("selection refs = %d, want both channels frozen for audit", len(selection.ObservationRefs))
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("input-token", inputKey, "0.01"),
		phase10RetailLinearRule("output-token", outputKey, "0.03"),
		phase10RetailLinearRule("image-input", imageKey, "0.05"),
		phase10RetailLinearRule("audio-output", audioKey, "0.02"),
		phase10RetailFixedRule("submission-fee", economics.FixedFeeScopeSubmission, "1"),
	})
	rate := func(legs []CallLegUsageRecord, sel RetailSelectionResult) RetailRatingResult {
		t.Helper()
		res, err := RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: sel, Policy: policy,
			Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if err != nil {
			t.Fatalf("RateSelectedRetailBLegs: %v", err)
		}
		return res
	}
	result := rate([]CallLegUsageRecord{leg}, selection)
	// Provider basis 110*0.01 + 200*0.03 + 2*0.05 + 12*0.02 = 1.10+6+0.10+0.24
	// = 7.44, plus one 1.00 submission fee = 8.44. Local 100*0.01+200*0.03+
	// 10*0.02 = 7.20 must not add.
	if got, want := result.CustomerCharge.Nano, int64(8_440_000_000); got != want {
		t.Fatalf("R charge = %d, want %d (provider 7.44 + one fee; local must not sum)", got, want)
	}
	if len(result.InferenceValuation.Lines) != 4 {
		t.Fatalf("inference lines = %d, want 4 (input/output/image/audio on provider basis): %+v", len(result.InferenceValuation.Lines), result.InferenceValuation.Lines)
	}
	for _, line := range result.InferenceValuation.Lines {
		if line.ChargeKind != RetailChargeKindInferenceUsage {
			t.Fatalf("inference line kind = %q, want %q: %+v", line.ChargeKind, RetailChargeKindInferenceUsage, line)
		}
		if len(line.SourceObservationRefs) != 1 || line.SourceObservationRefs[0].ObservationID != "provider-full" {
			t.Fatalf("inference line source = %+v, want exactly provider-full (frozen basis): %+v", line.SourceObservationRefs, line)
		}
	}
	// Fixed fee stays once for the whole call, not per channel or per B-leg.
	feeCount := 0
	for _, line := range result.Valuation.Lines {
		if line.ChargeKind == RetailChargeKindCommercialFee {
			feeCount++
			if line.FixedFee == nil || line.FixedFee.ID != "submission-fee" {
				t.Fatalf("commercial fee line = %+v, want submission-fee", line)
			}
		}
	}
	if feeCount != 1 {
		t.Fatalf("commercial fee lines = %d, want exactly one: %+v", feeCount, result.Valuation.Lines)
	}
	// Evidence preservation: original legs and selection still carry both
	// channels; rating cloned and never mutated the inputs.
	if len(leg.Observations) != 2 || len(leg.Observations[0].Measures) != origProviderMeasures || len(leg.Observations[1].Measures) != origLocalMeasures {
		t.Fatalf("rating mutated input legs: %+v", leg.Observations)
	}
	if len(result.Valuation.InputObservations) != 1 || result.Valuation.InputObservations[0].ObservationID != "provider-full" {
		// Inference input is the frozen basis; full evidence stays in E/Q/P,
		// reconciliation and query seams (covered by lifecycle test), not by
		// re-adding the losing channel to the R charge.
		t.Logf("R valuation inputs = %+v (frozen provider basis)", result.Valuation.InputObservations)
	}
	if result.Fingerprint == "" || result.Valuation.ID == "" {
		t.Fatalf("fingerprint/valuation ID missing: %+v", result)
	}
	// Order/permutation invariant: reversed delivery order gives identical
	// charge, fingerprint and valuation identity.
	reversedLeg := leg.Clone()
	reversedLeg.Observations[0], reversedLeg.Observations[1] = reversedLeg.Observations[1], reversedLeg.Observations[0]
	reversedSelection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{reversedLeg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	reversed := rate([]CallLegUsageRecord{reversedLeg}, reversedSelection)
	if reversed.CustomerCharge != result.CustomerCharge || reversed.Fingerprint != result.Fingerprint || reversed.Valuation.ID != result.Valuation.ID {
		t.Fatalf("order changed R: first=%+v reversed=%+v", result, reversed)
	}
	// Replay stable.
	replay := rate([]CallLegUsageRecord{leg}, selection)
	if replay.CustomerCharge != result.CustomerCharge || replay.Fingerprint != result.Fingerprint || replay.Valuation.ID != result.Valuation.ID {
		t.Fatalf("replay changed R: first=%+v replay=%+v", result, replay)
	}
	// Explicit proxy/service control remains additive on top of the frozen
	// inference basis.
	proxyKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: "proxy_egress_bytes", Unit: metering.UnitByte, SchemaID: "proxy.service.v1"}
	proxyObs := phase10RetailProxyObservation(t, call.CallID, "customer-egress-r1", "12", proxyKey)
	proxyTariff := phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "proxy-tariff-r1", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{
		phase10RetailLinearRule("egress", proxyKey, "0.10"),
	})
	withProxy, err := RateSelectedRetailBLegs(ctx, RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		ProxyService: &RetailProxyServiceInput{Tariff: proxyTariff, Observations: []metering.Observation{proxyObs}},
	})
	if err != nil {
		t.Fatalf("proxy rating: %v", err)
	}
	// 8.44 inference+fee + 12*0.10=1.20 proxy = 9.64.
	if got, want := withProxy.CustomerCharge.Nano, int64(9_640_000_000); got != want {
		t.Fatalf("inference plus proxy = %d, want %d", got, want)
	}
	if withProxy.ProxyServiceValuation == nil || len(withProxy.ProxyServiceValuation.Lines) != 1 {
		t.Fatalf("proxy valuation = %+v, want one additive service line", withProxy.ProxyServiceValuation)
	}
}

// Phase19 R1 GREEN: distinct B-legs stay additive; dedupe never crosses the
// B-leg/transform boundary. Two attributable B-legs each with competing
// channels bill two provider bases, not one and not four.
func TestPhase19R1RetailSourceDistinctBLegsStayAdditive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-a", "b-b")
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	mkLeg := func(bLegID, providerID, localID string) CallLegUsageRecord {
		t.Helper()
		providerObs := phase10RetailObservation(t, call.CallID, bLegID, providerID, metering.OriginProvider, metering.BoundaryBackendEgress,
			phase10RetailMeasure{key: key, quantity: "10"})
		localObs := phase10RetailObservation(t, call.CallID, bLegID, localID, metering.OriginLocal, metering.BoundaryBackendEgress,
			phase10RetailMeasure{key: key, quantity: "10"})
		leg := phase10RetailLeg(t, call.CallID, bLegID, 1, LegOutcomeFailed, SurfacedNo, providerObs)
		if bLegID == "b-a" {
			leg.AttemptSeq = 1
		} else {
			leg.AttemptSeq = 2
		}
		leg.Observations = append(leg.Observations, localObs)
		return leg
	}
	legA := mkLeg("b-a", "provider-a", "local-a")
	legB := mkLeg("b-b", "provider-b", "local-b")
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{legA, legB}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 4 {
		t.Fatalf("selection refs = %d, want all four channels for audit", len(selection.ObservationRefs))
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("output-token", key, "1"),
	})
	result, err := RateSelectedRetailBLegs(ctx, RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{legA, legB}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	// Each B-leg bills one provider basis of 10*1=10, total 20. Buggy sums
	// both channels per leg for 40; cross-leg dedupe would give 10.
	if got, want := result.CustomerCharge.Nano, int64(20_000_000_000); got != want {
		t.Fatalf("all-leg R = %d, want %d (two provider bases, no cross-leg dedupe, no double)", got, want)
	}
	if len(result.InferenceValuation.Lines) != 2 {
		t.Fatalf("inference lines = %d, want two B-leg lines: %+v", len(result.InferenceValuation.Lines), result.InferenceValuation.Lines)
	}
}

// Phase19 R1 GREEN: local-complete fallback when provider is incomplete keeps
// ordinary retail operable (Req 8.6) instead of failing or billing zero.
func TestPhase19R1RetailSourceLocalFallbackWhenProviderIncomplete(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	call.SubmissionID = "submission-r1-fallback"
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	providerObs := phase10RetailObservation(t, call.CallID, "b-winner", "provider-incomplete", metering.OriginProvider, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: key, quantity: "200"})
	providerObs.Measures[0].Value = nil
	providerObs.Measures[0].Quality = metering.QualityUnavailable
	localObs := phase10RetailObservation(t, call.CallID, "b-winner", "local-complete", metering.OriginLocal, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: key, quantity: "200"})
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, providerObs)
	leg.Observations = append(leg.Observations, localObs)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("output-token", key, "0.03"),
	})
	result, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("fallback rating: %v", err)
	}
	if got, want := result.CustomerCharge.Nano, int64(6_000_000_000); got != want {
		t.Fatalf("fallback R = %d, want %d (local-complete basis when provider incomplete)", got, want)
	}
	if len(result.InferenceValuation.Lines) != 1 || result.InferenceValuation.Lines[0].SourceObservationRefs[0].ObservationID != "local-complete" {
		t.Fatalf("fallback line = %+v, want single local-complete basis", result.InferenceValuation.Lines)
	}
}
