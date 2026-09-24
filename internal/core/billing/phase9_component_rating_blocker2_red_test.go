package billing

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Blocker2_COGSCorrectionTaintIsChargeLocalAndOrderIndependent(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-blocker2", "blocker2-base", "affected", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker2-stream"
	base.Sequence = 1
	base.Charges = append(base.Charges, phase9Blocker2Charge("healthy", "4"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	correction := phase5ChargeObservation(t, callID, "b-blocker2", "blocker2-correction", "affected", new("6"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correctionRef, err := correction.Ref(correction.Subject.StoreID)
	if err != nil {
		t.Fatalf("correction ref: %v", err)
	}

	delta := phase5ChargeObservation(t, callID, "b-blocker2", "blocker2-delta", "affected", new("2"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	delta.Semantics = metering.SemanticsDelta
	delta.StreamID = base.StreamID
	delta.Sequence = 3
	deltaRef, err := delta.Ref(delta.Subject.StoreID)
	if err != nil {
		t.Fatalf("delta ref: %v", err)
	}

	replacement := phase5ChargeObservation(t, callID, "b-blocker2", "blocker2-replacement", "affected", new("8"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 4
	replacement.Supersedes = []metering.ObservationRef{correctionRef, deltaRef}

	cases := []struct {
		name         string
		observations []metering.Observation
		wantSubtotal int64
		wantComplete CostCompleteness
		wantPayable  bool
	}{
		{
			name:         "correction keeps healthy sibling payable",
			observations: []metering.Observation{base, correction},
			wantSubtotal: 4_000_000_000,
			wantComplete: CostCompletenessPartial,
		},
		{
			name:         "transitive delta remains tainted",
			observations: []metering.Observation{delta, correction, base},
			wantSubtotal: 4_000_000_000,
			wantComplete: CostCompletenessPartial,
		},
		{
			name:         "exact replacement clears affected taint",
			observations: []metering.Observation{replacement, delta, correction, base},
			wantSubtotal: 12_000_000_000,
			wantComplete: CostCompletenessKnown,
			wantPayable:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			orders := [][]metering.Observation{
				tc.observations,
				reversePhase9Blocker2(tc.observations),
			}
			for order, observations := range orders {
				leg := phase5V2Leg(t, callID, "b-blocker2", observations[0])
				leg.Observations = append([]metering.Observation(nil), observations...)
				got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
				if err != nil {
					t.Fatalf("order %d COGS: %v", order, err)
				}
				if got.KnownSubtotal.Nano != tc.wantSubtotal || got.Completeness != tc.wantComplete || got.Payable != tc.wantPayable {
					t.Fatalf("order %d COGS=%+v, want subtotal=%d completeness=%s payable=%t", order, got, tc.wantSubtotal, tc.wantComplete, tc.wantPayable)
				}
				if tc.wantComplete == CostCompletenessPartial && len(got.UnknownLegKeys) != 1 {
					t.Fatalf("order %d unknown legs=%v, want affected B-leg only", order, got.UnknownLegKeys)
				}
			}
		})
	}
}

func TestPhase9Blocker2_COGSUnusableCorrectionDoesNotPoisonHealthyBLeg(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-blocker2-affected", "blocker2-cross-base", "affected", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Authority = metering.AuthorityUnavailableClaim
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker2-cross-affected-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase5ChargeObservation(t, callID, "b-blocker2-affected", "blocker2-cross-correction", "affected", new("6"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	healthy := phase5ChargeObservation(t, callID, "b-blocker2-healthy", "blocker2-cross-healthy", "healthy", new("4"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	healthy.StreamID = "blocker2-cross-healthy-stream"
	healthy.Sequence = 1

	affected := phase5V2Leg(t, callID, "b-blocker2-affected", base)
	affected.Observations = []metering.Observation{base, correction}
	healthyLeg := phase5V2Leg(t, callID, "b-blocker2-healthy", healthy)
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{affected, healthyLeg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS: %v", err)
	}
	if got.KnownSubtotal.Nano != 4_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("COGS=%+v, want healthy subtotal 4 with affected correction incomplete", got)
	}
	if len(got.IncludedLegKeys) != 1 || got.IncludedLegKeys[0] != callID.String()+":b-blocker2-healthy" {
		t.Fatalf("included legs=%v, want healthy B-leg only", got.IncludedLegKeys)
	}
	if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-blocker2-affected" {
		t.Fatalf("unknown legs=%v, want affected B-leg only", got.UnknownLegKeys)
	}
}

func TestPhase9Blocker2_COGSUnavailableBaseTaintIsChargeLocal(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-blocker2-authority", "blocker2-authority-base", "affected", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Authority = metering.AuthorityUnavailableClaim
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker2-authority-stream"
	base.Sequence = 1
	base.Charges = append(base.Charges, phase9Blocker2Charge("healthy", "4"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase5ChargeObservation(t, callID, "b-blocker2-authority", "blocker2-authority-correction", "affected", new("6"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = append(correction.Charges, phase9Blocker2Charge("healthy", "5"))
	leg := phase5V2Leg(t, callID, "b-blocker2-authority", base)
	leg.Observations = []metering.Observation{correction, base}
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS: %v", err)
	}
	if got.KnownSubtotal.Nano != 5_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("COGS=%+v, want healthy corrected charge 5 with affected authority taint", got)
	}
}

func TestPhase9Blocker2_COGSNilBaseCorrectionKeepsCorrectionSiblingUsable(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase5ChargeObservation(t, callID, "b-blocker2-sibling", "blocker2-sibling-base", "affected", nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker2-sibling-stream"
	base.Sequence = 1
	base.Charges = append(base.Charges, phase9Blocker2Charge("healthy", "4"))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase5ChargeObservation(t, callID, "b-blocker2-sibling", "blocker2-sibling-correction", "affected", new("6"), metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = append(correction.Charges, phase9Blocker2Charge("healthy", "5"))
	leg := phase5V2Leg(t, callID, "b-blocker2-sibling", base)
	leg.Observations = []metering.Observation{base, correction}
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS: %v", err)
	}
	if got.KnownSubtotal.Nano != 5_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("COGS=%+v, want healthy corrected charge 5 with affected taint", got)
	}
}

func phase9Blocker2Charge(item, amount string) metering.ReportedCharge {
	value := phase9Decimal(amount)
	return metering.ReportedCharge{
		ChargeItemID: item,
		Kind:         metering.ChargeKindAggregate,
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Currency:     "USD",
		Amount:       value,
	}
}

func reversePhase9Blocker2(observations []metering.Observation) []metering.Observation {
	out := make([]metering.Observation, len(observations))
	for i := range observations {
		out[len(observations)-1-i] = observations[i]
	}
	return out
}
