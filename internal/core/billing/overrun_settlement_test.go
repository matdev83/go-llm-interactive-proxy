package billing

import (
	"errors"
	"testing"
	"time"
)

// Task 14.2 RED: actual incurred amounts are retained even when they exceed
// the admitted quote. Overruns settle under the existing account policy with
// explicit breach state instead of truncating usage to the estimate.

func TestEvaluateSettleRetainsActualAboveMaxWithBreachState(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	admitted, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	before, err := SafetyMargin(acct, []CallExposure{admitted})
	if err != nil {
		t.Fatal(err)
	}
	result, err := EvaluateSettle(acct, []CallExposure{admitted}, SettleExposureInput{
		CallID: "call-1", Actual: exposureUSD(55), Now: admitted.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("overrun settle = %v, want success with breach state", err)
	}
	if !result.Breached || result.OverrunNano != 15 {
		t.Fatalf("breach = %+v, want Breached with OverrunNano 15", result)
	}
	if result.Account.BalanceNano != 45 {
		t.Fatalf("balance after overrun settle = %d, want actual 55 debited (100-55)", result.Account.BalanceNano)
	}
	if result.Exposure.Max.Nano != 40 || !result.Exposure.IsOpen() && result.Exposure.Status != ExposureClosed {
		t.Fatalf("close must retain original max 40: %+v", result.Exposure)
	}
	// Exact margin accounting: the margin decreases by exactly the overrun,
	// never more. Actual cost is retained, not rewritten to the quote.
	if result.SafetyMarginBefore.Nano != before.Nano {
		t.Fatalf("SafetyMarginBefore = %d, want %d", result.SafetyMarginBefore.Nano, before.Nano)
	}
	if result.SafetyMarginAfter.Nano != result.SafetyMarginBefore.Nano-15 {
		t.Fatalf("SafetyMarginAfter = %d, want before-15 (exact overrun)", result.SafetyMarginAfter.Nano)
	}
	if acct.BalanceNano != 100 {
		t.Fatalf("overrun settle mutated the input account: %+v", acct)
	}
}

func TestEvaluateSettleOverrunBeyondSpendableFailsUnderAccountPolicy(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	admitted, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateSettle(acct, []CallExposure{admitted}, SettleExposureInput{
		CallID: "call-1", Actual: exposureUSD(120),
	})
	if !errors.Is(err, ErrInsufficientSpendable) {
		t.Fatalf("overrun beyond floor = %v, want ErrInsufficientSpendable", err)
	}
	if acct.BalanceNano != 100 {
		t.Fatalf("failed settle mutated balance: %+v", acct)
	}
}
