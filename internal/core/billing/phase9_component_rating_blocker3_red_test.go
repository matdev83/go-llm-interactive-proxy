package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Blocker3_OperatorCOGSRejectsUnlinkedAggregateComponentOverlap(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	component := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "phase9.blocker3.v1",
	}
	aggregate := phase5ChargeObservation(t, callID, "b-blocker3-overlap", "blocker3-aggregate", "total", stringPtrBlocker3("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	aggregate.Semantics = metering.SemanticsCumulative
	aggregate.StreamID = "blocker3-overlap-stream"
	aggregate.Sequence = 1
	child := phase5ChargeObservation(t, callID, "b-blocker3-overlap", "blocker3-component", "image", stringPtrBlocker3("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Charges[0].Kind = metering.ChargeKindComponent
	child.Charges[0].Component = &component
	child.StreamID = aggregate.StreamID
	child.Sequence = 2
	leg := phase5V2Leg(t, callID, "b-blocker3-overlap", aggregate)
	leg.Observations = append(leg.Observations, child)

	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if !errors.Is(err, metering.ErrInvalidCoverage) {
		t.Fatalf("COGS error=%v, want ErrInvalidCoverage", err)
	}
	if got.Payable || got.Completeness != CostCompletenessPartial || got.KnownSubtotal.Nano != 0 {
		t.Fatalf("COGS=%+v, want partial/non-payable zero subtotal", got)
	}
}

func TestPhase9Blocker3_OperatorCOGSAllowsExplicitSurchargeChild(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	component := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "phase9.blocker3.v1",
	}
	child := phase5ChargeObservation(t, callID, "b-blocker3-surcharge", "blocker3-surcharge-child", "surcharge", stringPtrBlocker3("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Charges[0].Kind = metering.ChargeKindSurcharge
	child.Charges[0].Component = &component
	child.StreamID = "blocker3-surcharge-stream"
	child.Sequence = 2
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	aggregate := phase5ChargeObservation(t, callID, "b-blocker3-surcharge", "blocker3-surcharge-total", "total", stringPtrBlocker3("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "surcharge"}, Relation: metering.CoverageAdditive,
	})
	aggregate.Semantics = metering.SemanticsCumulative
	aggregate.StreamID = child.StreamID
	aggregate.Sequence = 1
	leg := phase5V2Leg(t, callID, "b-blocker3-surcharge", aggregate)
	leg.Observations = append(leg.Observations, child)

	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS error=%v, want explicit surcharge to be payable", err)
	}
	if !got.Payable || got.Completeness != CostCompletenessKnown || got.KnownSubtotal.Nano != 14_000_000_000 {
		t.Fatalf("COGS=%+v, want known/payable subtotal 14", got)
	}
}

func TestPhase9Blocker3_OperatorCOGSUnresolvedCoverageIsPartialNotInvalid(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	aggregate := phase5ChargeObservation(t, callID, "b-blocker3-pending", "blocker3-pending-total", "total", stringPtrBlocker3("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{StoreID: "store-1", ObservationID: "blocker3-missing-child", Revision: 1, ChargeItemID: "child"}, Relation: metering.CoverageInclusive,
	})
	aggregate.Semantics = metering.SemanticsCumulative
	aggregate.StreamID = "blocker3-pending-stream"
	aggregate.Sequence = 1
	leg := phase5V2Leg(t, callID, "b-blocker3-pending", aggregate)

	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS error=%v, want typed partial result for pending coverage", err)
	}
	if got.Payable || got.Completeness != CostCompletenessPartial || got.KnownSubtotal.Nano != 10_000_000_000 {
		t.Fatalf("COGS=%+v, want partial/non-payable subtotal 10", got)
	}
	if len(got.PendingCoverage) != 1 || got.PendingCoverage[0].Ref.ObservationID != "blocker3-missing-child" {
		t.Fatalf("pending coverage=%+v, want unresolved child reference", got.PendingCoverage)
	}
	if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-blocker3-pending" {
		t.Fatalf("unknown legs=%v, want owning B-leg", got.UnknownLegKeys)
	}
	if errors.Is(err, metering.ErrInvalidCoverage) {
		t.Fatalf("pending coverage was conflated with invalid overlap: %v", err)
	}
}

func TestPhase9Blocker3_OperatorCOGSIgnoresSupersededInvalidOverlap(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	component := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentImage,
		Unit:      metering.UnitImage,
		SchemaID:  "phase9.blocker3.v1",
	}
	child := phase5ChargeObservation(t, callID, "b-blocker3-superseded", "blocker3-superseded-child", "child", stringPtrBlocker3("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Charges[0].Kind = metering.ChargeKindComponent
	child.Charges[0].Component = &component
	child.StreamID = "blocker3-superseded-stream"
	child.Sequence = 2
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	base := phase5ChargeObservation(t, callID, "b-blocker3-superseded", "blocker3-invalid-base", "total", stringPtrBlocker3("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"}, Relation: metering.CoverageAdditive,
	})
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = child.StreamID
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-blocker3-superseded", "blocker3-valid-replacement", "total", stringPtrBlocker3("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"}, Relation: metering.CoverageInclusive,
	})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 3
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	for order, observations := range [][]metering.Observation{
		{base, child, replacement},
		{replacement, child, base},
		{child, replacement, base},
	} {
		leg := phase5V2Leg(t, callID, "b-blocker3-superseded", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS error=%v, want effective graph only", order, err)
		}
		if !got.Payable || got.Completeness != CostCompletenessKnown || got.KnownSubtotal.Nano != 8_000_000_000 {
			t.Fatalf("order %d COGS=%+v, want known/payable subtotal 8", order, got)
		}
	}
}

func stringPtrBlocker3(value string) *string { return &value }
