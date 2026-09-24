package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 B2b3 GREEN (core domain): financial adjustment posting-time
// ownership fence. Covers default/shadow owner resolution, claim validation
// against canonical head identity, and operation-kind fencing.

func b2b3MustCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func b2b3TestSubject(storeID, accountID string, callID BillingCallID) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-b2b3", BillingCallID: callID.String(), BLegID: "b-b2b3",
	}
}

func TestB2b3ResolveOwnerDefaultsV1AndValidates(t *testing.T) {
	t.Parallel()
	owner, err := ResolveFinancialAdjustmentOwner(SelectedCostAdjustmentInput{})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV1 {
		t.Fatalf("empty owner = %q, want v1", owner)
	}
	owner, err = ResolveFinancialAdjustmentOwner(SelectedCostAdjustmentInput{PostingOwner: PostingOwnerV2})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("V2 owner = %q", owner)
	}
	if _, err := ResolveFinancialAdjustmentOwner(SelectedCostAdjustmentInput{PostingOwner: "bogus"}); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus owner err = %v, want Invalid", err)
	}
	callID := b2b3MustCallID(t)
	subject := b2b3TestSubject("test", "acct", callID)
	opKey, err := FinancialAdjustmentPostingOperationKey("test", "acct", callID, "head-1", subject)
	if err != nil {
		t.Fatal(err)
	}
	claim := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: opKey, AccountID: "acct", CallID: callID, Owner: PostingOwnerV2, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	if _, err := ResolveFinancialAdjustmentOwner(SelectedCostAdjustmentInput{PostingOwner: PostingOwnerV1, Claim: &claim}); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("owner/claim mismatch err = %v, want Conflict", err)
	}
	owner, err = ResolveFinancialAdjustmentOwner(SelectedCostAdjustmentInput{Claim: &claim})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("claim owner = %q, want v2", owner)
	}
}

func b2b3TestValuation() SelectedCostValuation {
	decimal := metering.DecimalFromNanoUnits(10_000_000_000)
	v, err := NewSelectedCostValuation(SelectedCostValuationRef{
		ValuationID: "b2b3-test-val", Revision: 1, InputSetHash: "0000000000000000000000000000000000000000000000000000000000000001",
	}, OperatorCostSelectionResult{
		Status: OperatorCostSelectionStatusFinal, Provenance: OperatorCostProvenanceAttempted,
		Currency: "USD", Amount: &MonetaryExactAmount{Currency: "USD", Decimal: &decimal},
	})
	if err != nil {
		panic(err)
	}
	return v
}

func TestB2b3ValidateClaimRequiresCanonicalHeadIdentity(t *testing.T) {
	t.Parallel()
	callID := b2b3MustCallID(t)
	subject := b2b3TestSubject("test", "acct-b2b3", callID)
	opKey, err := FinancialAdjustmentPostingOperationKey("test", "acct-b2b3", callID, "head-b2b3", subject)
	if err != nil {
		t.Fatal(err)
	}
	input := SelectedCostAdjustmentInput{AccountID: "acct-b2b3", CallID: callID, HeadKey: "head-b2b3", Subject: subject, Selected: b2b3TestValuation()}
	good := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: opKey, AccountID: "acct-b2b3", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: AccountingCutoverV1Draining}
	if err := ValidateFinancialAdjustmentClaim(good, input); err != nil {
		t.Fatalf("good claim must validate: %v", err)
	}
	badKind := good
	badKind.Kind = PostingOperationCustomerSettlement
	if err := ValidateFinancialAdjustmentClaim(badKind, input); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("wrong kind err = %v, want Invalid", err)
	}
	badKey := good
	badKey.OperationKey = "tampered"
	if err := ValidateFinancialAdjustmentClaim(badKey, input); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong key err = %v, want Conflict", err)
	}
	badAccount := good
	badAccount.AccountID = "other"
	if err := ValidateFinancialAdjustmentClaim(badAccount, input); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong account must conflict")
	}
	otherCall := b2b3MustCallID(t)
	badCall := good
	badCall.CallID = otherCall
	if err := ValidateFinancialAdjustmentClaim(badCall, input); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong call must conflict")
	}
	otherSubject := b2b3TestSubject("test", "acct-b2b3", callID)
	otherSubject.BLegID = "b-other"
	otherInput := SelectedCostAdjustmentInput{AccountID: "acct-b2b3", CallID: callID, HeadKey: "head-b2b3", Subject: otherSubject, Selected: b2b3TestValuation()}
	if err := ValidateFinancialAdjustmentClaim(good, otherInput); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong subject must conflict")
	}
	if err := ValidateFinancialAdjustmentOperationKind("customer_call_settlement"); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("customer kind err = %v, want Invalid", err)
	}
	if err := ValidateFinancialAdjustmentOperationKind("provider_charge"); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("provider kind err = %v, want Invalid", err)
	}
	if err := ValidateFinancialAdjustmentOperationKind(""); err != nil {
		t.Fatalf("empty kind must allow legacy default: %v", err)
	}
	if err := ValidateFinancialAdjustmentOperationKind("financial_adjustment"); err != nil {
		t.Fatalf("financial_adjustment kind must pass: %v", err)
	}
}
