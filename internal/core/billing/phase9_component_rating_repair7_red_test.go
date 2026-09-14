package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair7_ProviderChargeSuccessorsStayUnchargeable(t *testing.T) {
	t.Parallel()
	component := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	t.Run("delta", func(t *testing.T) {
		base := phase9ProviderChargeObservation(t, "repair7-provider-base", 1, metering.SemanticsCumulative, blegSubjectForRepair7("b-main"),
			phase9ProviderCharge("repair7-affected", &component, ""), phase9ProviderCharge("repair7-sibling", &component, "4"))
		baseRef, err := base.Ref(base.Subject.StoreID)
		if err != nil {
			t.Fatalf("base ref: %v", err)
		}
		correction := phase9ProviderChargeObservation(t, "repair7-provider-correction", 2, metering.SemanticsCorrection, blegSubjectForRepair7("b-main"), phase9ProviderCharge("repair7-affected", &component, "2"))
		correction.Supersedes = []metering.ObservationRef{baseRef}
		delta := phase9ProviderChargeObservation(t, "repair7-provider-delta", 3, metering.SemanticsDelta, blegSubjectForRepair7("b-main"), phase9ProviderCharge("repair7-affected", &component, "3"))
		assertRepair7ProviderChargesIncomplete(t, []metering.Observation{delta, correction, base})
	})
	t.Run("partial replacement", func(t *testing.T) {
		base := phase9ProviderChargeObservation(t, "repair7-provider-partial-base", 1, metering.SemanticsCumulative, blegSubjectForRepair7("b-main"),
			phase9ProviderCharge("repair7-affected", &component, ""), phase9ProviderCharge("repair7-sibling", &component, "4"))
		baseRef, err := base.Ref(base.Subject.StoreID)
		if err != nil {
			t.Fatalf("base ref: %v", err)
		}
		correction := phase9ProviderChargeObservation(t, "repair7-provider-partial-correction", 2, metering.SemanticsCorrection, blegSubjectForRepair7("b-main"), phase9ProviderCharge("repair7-affected", &component, "2"))
		correction.Supersedes = []metering.ObservationRef{baseRef}
		correctionRef, err := correction.Ref(correction.Subject.StoreID)
		if err != nil {
			t.Fatalf("correction ref: %v", err)
		}
		replacement := phase9ProviderChargeObservation(t, "repair7-provider-partial-replacement", 3, metering.SemanticsReplacement, blegSubjectForRepair7("b-main"), phase9ProviderCharge("repair7-sibling", &component, "9"))
		replacement.Supersedes = []metering.ObservationRef{correctionRef}
		assertRepair7ProviderChargesIncomplete(t, []metering.Observation{replacement, correction, base})
	})
}

func assertRepair7ProviderChargesIncomplete(t *testing.T, observations []metering.Observation) {
	t.Helper()
	input := phase9RatingInput(t, economics.BasisProviderReported, observations)
	input.Rater = economics.RatingSnapshotRef{}
	input.RaterContent = nil
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("provider successor error=%v, want ErrRatingEvidenceMissing", err)
	}
	for _, line := range got.Lines {
		if line.ItemID == "repair7-affected" && line.Status == economics.RatingLineProviderReported {
			t.Fatalf("unresolved provider successor remained payable: %+v", line)
		}
	}
}

func phase9ProviderChargeObservation(t *testing.T, id string, sequence uint64, semantics string, subject metering.SubjectRef, charges ...metering.ReportedCharge) metering.Observation {
	t.Helper()
	observation := phase9Observation(t, id, metering.OriginProvider, phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage), "1")
	observation.Subject = subject
	observation.Correlation.BLegID = subject.BLegID
	observation.StreamID = "repair7-provider-stream"
	observation.Sequence = sequence
	observation.Semantics = semantics
	observation.Measures = nil
	observation.Charges = append([]metering.ReportedCharge(nil), charges...)
	return observation
}

func blegSubjectForRepair7(id string) metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store", ALegID: "a", BillingCallID: "call", BLegID: id}
}

func phase9ProviderCharge(itemID string, component *metering.ComponentKey, amount string) metering.ReportedCharge {
	charge := metering.ReportedCharge{ChargeItemID: itemID, Component: component, Kind: metering.ChargeKindComponent}
	if amount != "" {
		charge.Amount = phase9Decimal(amount)
		charge.Currency = "USD"
	}
	return charge
}
