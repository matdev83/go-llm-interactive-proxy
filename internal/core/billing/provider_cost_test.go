package billing

import "testing"

func TestRateProviderCostIsIndependentPerLeg(t *testing.T) {
	callID := mustBillingCallID(t)
	leg := testCallLegUsageRecord(callID, "b-cost")
	got, err := RateProviderCost(leg, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount.Nano != 11 || !got.Authoritative || !got.Reconciled {
		t.Fatalf("authoritative cost = %+v", got)
	}
}
