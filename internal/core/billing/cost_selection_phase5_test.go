package billing

import (
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func phase5CostLeg(t *testing.T, callID BillingCallID, id string, seq int, outcome LegOutcome, surfaced SurfacedState, amount *int64) CallLegUsageRecord {
	t.Helper()
	leg := testCallLegUsageRecord(callID, id)
	leg.AttemptSeq = seq
	leg.Outcome = outcome
	leg.Surfaced = surfaced
	if amount == nil {
		leg.Evidence = FinalBillingEvidence{Source: EvidenceSourceUnavailable, Authority: EvidenceAuthorityUnavailable}
	} else {
		leg.Evidence.Cost = MoneyEvidence{NanoUnits: *amount, Currency: "USD", Present: true}
		leg.Evidence.Source = EvidenceSourceProviderReported
		leg.Evidence.Authority = EvidenceAuthorityAuthoritative
	}
	return leg
}

func TestPhase5OperatorCOGSIncludesAllExecutedLegsAndRetailSelectsWinner(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amounts := []int64{3, 5, 2}
	legs := []CallLegUsageRecord{
		phase5CostLeg(t, callID, "b-retry", 1, LegOutcomeFailed, SurfacedNo, &amounts[0]),
		phase5CostLeg(t, callID, "b-loser", 2, LegOutcomeLoser, SurfacedNo, &amounts[1]),
		phase5CostLeg(t, callID, "b-winner", 3, LegOutcomeWinner, SurfacedYes, &amounts[2]),
	}
	got, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 10 || got.Completeness != CostCompletenessKnown || !got.Payable {
		t.Fatalf("operator COGS = %+v, want known payable subtotal 10", got)
	}
	selected, err := SelectRetailBLegs(legs, TurnOutcomeCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].BLegID != "b-winner" {
		t.Fatalf("retail selection = %#v, want surfaced winner only", selected)
	}
}

func TestPhase5OperatorCOGSMissingAttemptIsPartialAndNeverStartedIsExcluded(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	known := int64(7)
	legs := []CallLegUsageRecord{
		phase5CostLeg(t, callID, "b-known", 1, LegOutcomeFailed, SurfacedNo, &known),
		phase5CostLeg(t, callID, "b-unknown", 2, LegOutcomeCanceled, SurfacedNo, nil),
		phase5CostLeg(t, callID, "b-shell", 3, LegOutcomeNeverStarted, SurfacedNo, nil),
	}
	got, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != known || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("operator COGS = %+v, want partial known subtotal 7", got)
	}
	if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != legs[1].CallID.String()+":"+legs[1].BLegID {
		t.Fatalf("unknown legs = %v, want attempted leg only", got.UnknownLegKeys)
	}
	if len(got.ExcludedLegKeys) != 1 || got.ExcludedLegKeys[0] != legs[2].CallID.String()+":"+legs[2].BLegID {
		t.Fatalf("excluded legs = %v, want never-started shell only", got.ExcludedLegKeys)
	}
}

func phase5ChargeObservation(t *testing.T, callID BillingCallID, bLegID, id, item string, amount *string, payer metering.PaymentParty, covers ...metering.ChargeCoverageRef) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	charge := metering.ReportedCharge{ChargeItemID: item, Kind: metering.ChargeKindAggregate, Payer: payer, Covers: covers}
	if amount != nil {
		value, err := metering.ParseDecimal(*amount)
		if err != nil {
			t.Fatal(err)
		}
		charge.Amount = &value
		charge.Currency = "USD"
	}
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase5:test:v1", Charges: []metering.ReportedCharge{charge},
	}
}

func phase5V2Leg(t *testing.T, callID BillingCallID, id string, observation metering.Observation) CallLegUsageRecord {
	t.Helper()
	leg := phase5CostLeg(t, callID, id, 1, LegOutcomeWinner, SurfacedYes, nil)
	leg.ALegID = "a-1"
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	leg.Observations = []metering.Observation{observation}
	return leg
}

func TestPhase5OperatorCOGSUsesInclusiveParentOnceAndRejectsPendingOrBYOK(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	childRef := metering.ChargeRef{StoreID: "store-1", ObservationID: "obs-child", Revision: 1, ChargeItemID: "child"}
	parent := phase5ChargeObservation(t, callID, "b-v2", "obs-parent", "parent", func() *string { v := "10"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{Ref: childRef, Relation: metering.CoverageInclusive})
	child := phase5ChargeObservation(t, callID, "b-v2", "obs-child", "child", func() *string { v := "4"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Charges[0].Covers[0].Ref = childRef
	legs := []CallLegUsageRecord{phase5V2Leg(t, callID, "b-v2", parent), phase5V2Leg(t, callID, "b-v2", child)}
	// A CallLegUsageRecord is one B-leg closure, so two source observations are
	// carried by one record for graph validation.
	legs[0].Observations = append(legs[0].Observations, child)
	legs = legs[:1]
	got, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 10_000_000_000 || got.Completeness != CostCompletenessKnown {
		t.Fatalf("inclusive COGS = %+v, want parent amount 10 once", got)
	}

	pending := parent
	pending.ID = "obs-pending"
	pending.SourceEventKey = "pending-source"
	pending.StreamID = "pending-stream"
	pending.Charges[0].Covers[0].Ref.ObservationID = "missing"
	got, err = AttributeOperatorCOGS([]CallLegUsageRecord{phase5V2Leg(t, callID, "b-v2", pending)}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Completeness != CostCompletenessPartial || got.Payable || got.KnownSubtotal.Nano != 10_000_000_000 {
		t.Fatalf("pending COGS = %+v, want partial/non-payable parent subtotal", got)
	}

	byok := child
	byok.ID = "obs-byok"
	byok.SourceEventKey = "byok-source"
	byok.StreamID = "byok-stream"
	byok.Charges[0].Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	byokLeg := phase5V2Leg(t, callID, "b-v2", byok)
	// A V1 projection cannot override an explicit customer-paid V2 charge.
	byokLeg.Evidence = FinalBillingEvidence{
		Cost:      MoneyEvidence{NanoUnits: 99, Currency: "USD", Present: true},
		Source:    EvidenceSourceProviderReported,
		Authority: EvidenceAuthorityAuthoritative,
	}
	got, err = AttributeOperatorCOGS([]CallLegUsageRecord{byokLeg}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 0 || got.Completeness != CostCompletenessKnown {
		t.Fatalf("BYOK COGS = %+v, want known non-operator zero", got)
	}
}

func TestPhase5OperatorCOGSRejectsContradictoryCoverage(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	childRef := metering.ChargeRef{StoreID: "store-1", ObservationID: "obs-child", Revision: 1, ChargeItemID: "child"}
	a := phase5ChargeObservation(t, callID, "b-v2", "obs-a", "a", func() *string { v := "2"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{Ref: childRef, Relation: metering.CoverageInclusive})
	b := phase5ChargeObservation(t, callID, "b-v2", "obs-b", "b", func() *string { v := "3"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{Ref: childRef, Relation: metering.CoverageAdditive})
	leg := phase5V2Leg(t, callID, "b-v2", a)
	leg.Observations = append(leg.Observations, b)
	_, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if !errors.Is(err, metering.ErrInvalidCoverage) {
		t.Fatalf("err = %v, want invalid coverage", err)
	}
}

func TestPhase5OperatorCOGSMarksPendingSupersessionIncomplete(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	observation := phase5ChargeObservation(t, callID, "b-v2", "obs-correction", "correction", func() *string { v := "6"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	observation.Semantics = metering.SemanticsCorrection
	observation.Supersedes = []metering.ObservationRef{{
		StoreID: "store-1", ObservationID: "obs-before-terminal", Revision: 1, PayloadHash: "pending-payload-hash",
	}}
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{phase5V2Leg(t, callID, "b-v2", observation)}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 6_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("pending supersession COGS = %+v, want known subtotal 6 and partial/non-payable", got)
	}
	if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-v2" {
		t.Fatalf("pending supersession unknown legs = %v, want owning B-leg", got.UnknownLegKeys)
	}
}

func TestPhase5OperatorCOGSAppliesChargeCorrectionOnce(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-v2", "obs-base", "provider-total", func() *string { v := "4"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	correction := phase5ChargeObservation(t, callID, "b-v2", "obs-correction", "provider-total", func() *string { v := "6"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.StreamID = base.StreamID
	correction.Sequence = base.Sequence + 1
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef}
	leg := phase5V2Leg(t, callID, "b-v2", base)
	leg.Observations = append(leg.Observations, correction)

	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 6_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
		t.Fatalf("corrected COGS = %+v, want effective correction amount 6 once", got)
	}
}

func TestPhase5CallLegReplayIgnoresObservationReceiptTime(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	observation := phase5ChargeObservation(t, callID, "b-v2", "obs-receipt", "provider-total", func() *string { v := "1"; return &v }(), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	first, err := phase5V2Leg(t, callID, "b-v2", observation).Seal()
	if err != nil {
		t.Fatal(err)
	}
	replayed := first.Clone()
	replayed.Observations[0].ReceivedAt = replayed.Observations[0].ReceivedAt.Add(time.Hour)
	replayed, err = replayed.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(first, replayed); err != nil {
		t.Fatalf("receipt-only replay conflict = %v", err)
	}
}

func TestPhase5OperatorCOGSDoesNotPayConflictingSourceProjection(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amount := int64(9)
	leg := phase5CostLeg(t, callID, "b-conflict", 1, LegOutcomeFailed, SurfacedNo, &amount)
	leg.EvidenceConflicts = []EvidenceConflict{{
		Identity: "source-conflict\x00revision:1", ExistingHash: "existing", IncomingHash: "incoming",
	}}
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 0 || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("conflicting COGS = %+v, want zero known subtotal and partial/non-payable", got)
	}
	if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-conflict" {
		t.Fatalf("conflicting unknown legs = %v, want owning B-leg", got.UnknownLegKeys)
	}
}
