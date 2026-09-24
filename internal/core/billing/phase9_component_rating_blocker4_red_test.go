package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Blocker4_COGSOneChargeToManyCorrectionReplacesBase(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase9Blocker4Observation(t, callID, "b-blocker4-correction", "blocker4-correction-base", "usage", "10")
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker4-correction-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	correction := phase9Blocker4Observation(t, callID, "b-blocker4-correction", "blocker4-correction-successor", "usage", "8")
	correction.Charges = append(correction.Charges, phase9Blocker4Charge("surcharge", "2"))
	correction.Semantics = metering.SemanticsCorrection
	correction.StreamID = base.StreamID
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}

	orders := [][]metering.Observation{
		{base, correction},
		{correction, base},
	}
	for order, observations := range orders {
		leg := phase5V2Leg(t, callID, "b-blocker4-correction", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 10_000_000_000 || got.Completeness != CostCompletenessPartial || got.Payable {
			t.Fatalf("order %d COGS=%+v, want partial/non-payable successor subtotal 10", order, got)
		}
		if len(got.UnknownLegKeys) != 1 || got.UnknownLegKeys[0] != callID.String()+":b-blocker4-correction" {
			t.Fatalf("order %d unknown legs=%v, want correction B-leg only", order, got.UnknownLegKeys)
		}
	}
}

func TestPhase9Blocker4_COGSOneChargeToManyReplacementReplacesBase(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase9Blocker4Observation(t, callID, "b-blocker4-replacement", "blocker4-replacement-base", "usage", "10")
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker4-replacement-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	replacement := phase9Blocker4Observation(t, callID, "b-blocker4-replacement", "blocker4-replacement-successor", "usage", "7")
	replacement.Charges = append(replacement.Charges, phase9Blocker4Charge("surcharge", "3"))
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	assertPhase9Blocker4COGS(t, callID, "b-blocker4-replacement", []metering.Observation{base, replacement}, 10_000_000_000)
}

func TestPhase9Blocker4_COGSManyChargeToOneReplacementKeepsSibling(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase9Blocker4Observation(t, callID, "b-blocker4-reverse", "blocker4-reverse-base", "usage", "10")
	base.Charges = append(base.Charges, phase9Blocker4Charge("surcharge", "3"))
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker4-reverse-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	replacement := phase9Blocker4Observation(t, callID, "b-blocker4-reverse", "blocker4-reverse-successor", "usage", "7")
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	assertPhase9Blocker4COGS(t, callID, "b-blocker4-reverse", []metering.Observation{replacement, base}, 10_000_000_000)
}

func TestPhase9Blocker4_COGSCardinalityMatchingIsDeterministicWhenShuffled(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase9Blocker4Observation(t, callID, "b-blocker4-shuffle", "blocker4-shuffle-base", "usage", "10")
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker4-shuffle-stream"
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	replacement := phase9Blocker4Observation(t, callID, "b-blocker4-shuffle", "blocker4-shuffle-successor", "usage", "7")
	replacement.Charges = append(replacement.Charges, phase9Blocker4Charge("surcharge", "3"))
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 2
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	orders := [][]metering.Observation{
		{base, replacement},
		{replacement, base},
	}
	for order, observations := range orders {
		leg := phase5V2Leg(t, callID, "b-blocker4-shuffle", observations[0])
		leg.Observations = append([]metering.Observation(nil), observations...)
		got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
		if err != nil {
			t.Fatalf("order %d COGS: %v", order, err)
		}
		if got.KnownSubtotal.Nano != 10_000_000_000 || got.Completeness != CostCompletenessKnown || !got.Payable {
			t.Fatalf("order %d COGS=%+v, want known/payable subtotal 10", order, got)
		}
		if len(got.IncludedLegKeys) != 1 || got.IncludedLegKeys[0] != callID.String()+":b-blocker4-shuffle" {
			t.Fatalf("order %d included legs=%v, want one B-leg", order, got.IncludedLegKeys)
		}
	}
}

func TestPhase9Blocker4_COGSRejectsCardinalityMatchAcrossProviderAccount(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base := phase9Blocker4Observation(t, callID, "b-blocker4-cross", "blocker4-cross-base", "usage", "10")
	base.Semantics = metering.SemanticsCumulative
	base.StreamID = "blocker4-cross-stream"
	base.Sequence = 1
	base.Correlation.ProviderAccountKey = "provider-account-a"
	base.Subject.ProviderAccountKey = "provider-account-a"
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}

	replacement := phase9Blocker4Observation(t, callID, "b-blocker4-cross", "blocker4-cross-successor", "usage", "7")
	replacement.Charges = append(replacement.Charges, phase9Blocker4Charge("surcharge", "3"))
	replacement.Semantics = metering.SemanticsReplacement
	replacement.StreamID = base.StreamID
	replacement.Sequence = 2
	replacement.Correlation.ProviderAccountKey = "provider-account-b"
	replacement.Subject.ProviderAccountKey = "provider-account-b"
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	leg := phase5V2Leg(t, callID, "b-blocker4-cross", base)
	leg.Observations = []metering.Observation{replacement, base}
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if !errors.Is(err, metering.ErrInvalidRevision) {
		t.Fatalf("COGS error=%v, want ErrInvalidRevision", err)
	}
	if got.Payable || got.Completeness != CostCompletenessPartial || got.KnownSubtotal.Nano != 0 {
		t.Fatalf("COGS=%+v, want partial/non-payable zero subtotal", got)
	}
}

func phase9Blocker4Observation(t *testing.T, callID BillingCallID, bLegID, id, item, amount string) metering.Observation {
	t.Helper()
	return phase5ChargeObservation(t, callID, bLegID, id, item, &amount, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
}

func phase9Blocker4Charge(item, amount string) metering.ReportedCharge {
	value := phase9Decimal(amount)
	return metering.ReportedCharge{
		ChargeItemID: item,
		Kind:         metering.ChargeKindAggregate,
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Currency:     "USD",
		Amount:       value,
	}
}

func assertPhase9Blocker4COGS(t *testing.T, callID BillingCallID, bLegID string, observations []metering.Observation, wantNano int64) {
	t.Helper()
	leg := phase5V2Leg(t, callID, bLegID, observations[0])
	leg.Observations = append([]metering.Observation(nil), observations...)
	got, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatalf("COGS: %v", err)
	}
	if got.KnownSubtotal.Nano != wantNano || got.Completeness != CostCompletenessKnown || !got.Payable {
		t.Fatalf("COGS=%+v, want known/payable subtotal %d", got, wantNano)
	}
	if len(got.UnknownLegKeys) != 0 {
		t.Fatalf("unknown legs=%v, want none", got.UnknownLegKeys)
	}
}
