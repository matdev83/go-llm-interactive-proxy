package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair9_COGSUsesReplacementCoverageNotStaleInclusiveEdge(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9", "repair9-cogs-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}, metering.ChargeCoverageRef{})
	parent.Charges[0].Covers = nil
	parent.Semantics = metering.SemanticsCumulative
	parent.StreamID = "repair9-cogs-stream"
	parent.Sequence = 1
	child := phase5ChargeObservation(t, callID, "b-repair9", "repair9-cogs-child", "child", new("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Semantics = metering.SemanticsDelta
	child.StreamID = parent.StreamID
	child.Sequence = 2
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"}, Relation: metering.CoverageInclusive}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9", "repair9-cogs-replacement", "total", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = parent.StreamID
	replacement.Sequence = 3
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{
		{parent, child, replacement},
		{replacement, child, parent},
		{child, replacement, parent},
	} {
		leg := phase5V2Leg(t, callID, "b-repair9", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 12_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want payable 12 from replacement 8 plus child 4", order, got)
		}
	}
}

func TestPhase9Repair9_COGSIgnoresSupersededUnknownCoverage(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9-missing", "repair9-missing-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Semantics = metering.SemanticsCumulative
	parent.StreamID = "repair9-missing-stream"
	parent.Sequence = 1
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{
		Ref:      metering.ChargeRef{StoreID: parent.Subject.StoreID, ObservationID: "repair9-missing-child", Revision: 1, ChargeItemID: "child"},
		Relation: metering.CoverageInclusive,
	}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9-missing", "repair9-missing-replacement", "total", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = parent.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{
		{parent, replacement},
		{replacement, parent},
	} {
		leg := phase5V2Leg(t, callID, "b-repair9-missing", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 8_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want payable 8 with superseded unknown edge ignored", order, got)
		}
		if len(got.PendingCoverage) != 0 || len(got.UnknownLegKeys) != 0 {
			t.Fatalf("order %d stale coverage diagnostics survived effective replacement: %+v", order, got)
		}
	}
}

func TestPhase9Repair9_COGSIgnoresSupersededHistoricalPendingRevision(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9-pending", "repair9-pending-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Semantics = metering.SemanticsCorrection
	parent.StreamID = "repair9-pending-stream"
	parent.Sequence = 1
	parent.Supersedes = []metering.ObservationRef{{
		StoreID: parent.Subject.StoreID, ObservationID: "repair9-historical", Revision: 1, PayloadHash: "repair9-historical-hash",
	}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9-pending", "repair9-pending-replacement", "total", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = parent.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{{parent, replacement}, {replacement, parent}} {
		leg := phase5V2Leg(t, callID, "b-repair9-pending", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 8_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want payable 8 with superseded pending history ignored", order, got)
		}
	}
}

func TestPhase9Repair9_COGSRetainsReplacementInclusiveCoverage(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9-retain", "repair9-retain-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Semantics = metering.SemanticsCumulative
	parent.StreamID = "repair9-retain-stream"
	parent.Sequence = 1
	child := phase5ChargeObservation(t, callID, "b-repair9-retain", "repair9-retain-child", "child", new("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Semantics = metering.SemanticsDelta
	child.StreamID = parent.StreamID
	child.Sequence = 2
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	inclusive := metering.ChargeCoverageRef{
		Ref:      metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"},
		Relation: metering.CoverageInclusive,
	}
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{inclusive}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9-retain", "repair9-retain-replacement", "total", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = parent.StreamID
	replacement.Sequence = 3
	replacement.Supersedes = []metering.ObservationRef{parentRef}
	replacement.Charges[0].Covers = []metering.ChargeCoverageRef{inclusive}

	for order, observations := range [][]metering.Observation{
		{parent, child, replacement},
		{replacement, child, parent},
		{child, replacement, parent},
	} {
		leg := phase5V2Leg(t, callID, "b-repair9-retain", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 8_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want payable 8 with effective inclusive coverage", order, got)
		}
	}
}

func TestPhase9Repair9_COGSRejectsEffectiveAggregateAdditiveComponentOverlap(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9-overlap", "repair9-overlap-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Semantics = metering.SemanticsCumulative
	parent.StreamID = "repair9-overlap-stream"
	parent.Sequence = 1
	child := phase5ChargeObservation(t, callID, "b-repair9-overlap", "repair9-overlap-child", "child", new("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	child.Charges[0].Kind = metering.ChargeKindComponent
	child.Charges[0].Component = parentComponentRepair9()
	child.Semantics = metering.SemanticsDelta
	child.StreamID = parent.StreamID
	child.Sequence = 2
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"}, Relation: metering.CoverageAdditive}}
	leg := phase5V2Leg(t, callID, "b-repair9-overlap", parent)
	leg.Observations = append(leg.Observations, child)
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if !errors.Is(err, metering.ErrInvalidCoverage) {
		t.Fatalf("COGS error=%v, want ErrInvalidCoverage", err)
	}
	if got.Payable || got.Completeness != CostCompletenessPartial || got.KnownSubtotal.Nano != 0 {
		t.Fatalf("invalid overlap COGS=%+v, want partial/non-payable with no rated subtotal", got)
	}
}

func TestPhase9Repair9_COGSNilBaseCorrectionIsIncompleteInAnyOrder(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-repair9-nil", "repair9-nil-base", "usage", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Authority = metering.AuthorityUnavailableClaim
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "repair9-nil-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase5ChargeObservation(t, callID, "b-repair9-nil", "repair9-nil-correction", "usage", new("6"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}

	for order, observations := range [][]metering.Observation{{base, correction}, {correction, base}} {
		leg := phase5V2Leg(t, callID, "b-repair9-nil", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.Payable || got.Completeness != CostCompletenessPartial || got.KnownSubtotal.Nano != 0 {
			t.Fatalf("order %d COGS=%+v, want incomplete/non-payable zero subtotal", order, got)
		}
	}
}

func TestPhase9Repair9_COGSPartialReplacementRetainsCoverageDiagnostic(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	parent := phase5ChargeObservation(t, callID, "b-repair9-partial", "repair9-partial-parent", "total", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	parent.Semantics = metering.SemanticsCumulative
	parent.StreamID = "repair9-partial-stream"
	parent.Sequence = 1
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{
		Ref:      metering.ChargeRef{StoreID: parent.Subject.StoreID, ObservationID: "repair9-partial-child", Revision: 1, ChargeItemID: "child"},
		Relation: metering.CoverageInclusive,
	}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9-partial", "repair9-partial-replacement", "surcharge", new("3"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = parent.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{
		{parent, replacement},
		{replacement, parent},
	} {
		leg := phase5V2Leg(t, callID, "b-repair9-partial", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 13_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
			t.Fatalf("order %d COGS=%+v, want partial/non-payable subtotal 13", order, got)
		}
		if len(got.PendingCoverage) != 1 || got.PendingCoverage[0].Ref.ObservationID != "repair9-partial-child" {
			t.Fatalf("order %d pending coverage=%+v, want unresolved child diagnostic", order, got.PendingCoverage)
		}
		if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-repair9-partial" {
			t.Fatalf("order %d unknown legs=%v, want owning B-leg", order, got.UnknownLegKeys)
		}
	}
}

func TestPhase9Repair9_COGSOneToManyReplacementDropsOneChargeBase(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-repair9-many", "repair9-many-base", "usage", new("10"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Charges[0].Component = parentComponentRepair9()
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "repair9-many-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	replacement := phase5ChargeObservation(t, callID, "b-repair9-many", "repair9-many-replacement", "usage", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Charges = append(replacement.Charges, phase5ChargeObservation(t, callID, "b-repair9-many", "repair9-many-surcharge", "surcharge", new("2"), metering.PaymentParty{Kind: metering.PaymentPartyOperator}).Charges[0])
	for i := range replacement.Charges {
		replacement.Charges[i].Component = parentComponentRepair9()
	}
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	for order, observations := range [][]metering.Observation{{base, replacement}, {replacement, base}} {
		leg := phase5V2Leg(t, callID, "b-repair9-many", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 10_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want replacement total 10", order, got)
		}
	}
}

//go:fix inline
func stringPtrRepair9(value string) *string { return new(value) }

func parentComponentRepair9() *metering.ComponentKey {
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair9.v1"}
	return &key
}
