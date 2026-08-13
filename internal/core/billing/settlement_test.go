package billing

import (
	"errors"
	"testing"
	"time"
)

func TestSettlementSourceKeysAreBoundToDurableEvidence(t *testing.T) {
	t.Parallel()
	customer, err := CustomerSettlementSourceKey("acct:turn")
	if err != nil {
		t.Fatal(err)
	}
	if customer != "customer-settlement:v1:acct:turn" {
		t.Fatalf("customer source = %q", customer)
	}
	provider, err := ProviderCostSourceKey("acct:turn:bleg-1")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "provider-cost:v1:acct:turn:bleg-1" {
		t.Fatalf("provider source = %q", provider)
	}
	if _, err := CustomerSettlementSourceKey(""); !errors.Is(err, ErrSettlementInvalid) {
		t.Fatalf("empty customer source error = %v", err)
	}
	if _, err := ProviderCostSourceKey(""); !errors.Is(err, ErrSettlementInvalid) {
		t.Fatalf("empty provider source error = %v", err)
	}
}

func TestResultFingerprintIncludesEveryLURCost(t *testing.T) {
	t.Parallel()
	result := Result{
		TURKey:         "acct:turn",
		CustomerCharge: Money{Nano: 10, Currency: "USD"},
		OperatorCosts: []OperatorCostResult{
			{LURKey: "acct:turn:b1", Amount: Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true},
		},
	}
	first, err := result.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	result.OperatorCosts[0].Amount.Nano++
	second, err := result.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("operator cost mutation did not change result fingerprint")
	}
}

func TestApplyBillingInputRejectsUnreconciledOrMissingLURCost(t *testing.T) {
	t.Parallel()
	record := settlementTestRecord()
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	auth := Authorization{ID: "auth-1", AccountID: "acct", TURKey: sealed.Key, Amount: Money{Nano: 20, Currency: "USD"}}
	for _, result := range []Result{
		{TURKey: sealed.Key, CustomerCharge: Money{Currency: "USD"}, UnreconciledCost: true},
		{TURKey: sealed.Key, CustomerCharge: Money{Currency: "USD"}, OperatorCosts: []OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: Money{Currency: "USD"}, Reconciled: false}}},
	} {
		if err := (ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}).Validate(); !errors.Is(err, ErrSettlementInvalid) {
			t.Fatalf("validation error = %v, want settlement invalid", err)
		}
	}
}

func TestApplyBillingInputRejectsChargeExceedingAuthorization(t *testing.T) {
	t.Parallel()
	sealed, err := settlementTestRecord().Seal()
	if err != nil {
		t.Fatal(err)
	}
	auth := Authorization{ID: "auth-1", AccountID: "acct", TURKey: sealed.Key, Amount: Money{Nano: 20, Currency: "USD"}}
	reconciled := OperatorCostResult{LURKey: sealed.Legs[0].Key, Amount: Money{Nano: 1, Currency: "USD"}, AmountPresent: true, Reconciled: true}
	valid := ApplyBillingInput{
		Record: sealed, Authorization: auth,
		Result: Result{TURKey: sealed.Key, CustomerCharge: Money{Nano: 12, Currency: "USD"}, OperatorCosts: []OperatorCostResult{reconciled}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid settlement: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ApplyBillingInput)
	}{
		{
			name: "charge exceeds authorization",
			mutate: func(in *ApplyBillingInput) {
				in.Result.CustomerCharge.Nano = in.Authorization.Amount.Nano + 1
			},
		},
		{
			name: "tur key mismatch",
			mutate: func(in *ApplyBillingInput) {
				in.Result.TURKey = "acct:other-turn"
			},
		},
		{
			name: "authorization not bound to record",
			mutate: func(in *ApplyBillingInput) {
				in.Authorization.TURKey = "acct:other-turn"
			},
		},
		{
			name: "customer currency mismatch",
			mutate: func(in *ApplyBillingInput) {
				in.Result.CustomerCharge.Currency = "EUR"
			},
		},
		{
			name: "negative customer charge",
			mutate: func(in *ApplyBillingInput) {
				in.Result.CustomerCharge.Nano = -1
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := valid
			in.Result.OperatorCosts = append([]OperatorCostResult(nil), valid.Result.OperatorCosts...)
			tt.mutate(&in)
			if err := in.Validate(); !errors.Is(err, ErrSettlementInvalid) {
				t.Fatalf("validation error = %v, want ErrSettlementInvalid", err)
			}
		})
	}
}

func settlementTestRecord() TurnUsageRecord {
	now := time.Unix(100, 0).UTC()
	return TurnUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, AccountID: "acct", TurnID: "turn", ALegID: "a-1", AuthorizationID: "auth-1",
		StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: TurnOutcomeCompleted,
		CustomerPricingRef: VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: VersionRef{ID: "policy", Version: "v1"},
		Legs: []LegUsageRecord{{ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: LegOutcomeWinner, Surfaced: SurfacedYes}},
	}
}
