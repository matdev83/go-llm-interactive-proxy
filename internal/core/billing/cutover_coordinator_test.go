package billing

import (
	"errors"
	"testing"
)

// Phase 17.3B2a RED: cutover coordinator domain — new-work gates, claim
// eligibility, V2 authorization, bounded config, claim metadata for B2b.

func TestCutoverCoordinatorV1WorkAllowedOnlyBeforeDraining(t *testing.T) {
	t.Parallel()
	if !IsV1FinancialWorkAllowed(AccountingCutoverV1Active) {
		t.Fatalf("v1_active must allow V1 new work")
	}
	if !IsV1FinancialWorkAllowed(AccountingCutoverV2Shadow) {
		t.Fatalf("v2_shadow must allow V1 new work")
	}
	if IsV1FinancialWorkAllowed(AccountingCutoverV1Draining) {
		t.Fatalf("v1_draining must fence V1 new work")
	}
	if IsV1FinancialWorkAllowed(AccountingCutoverV2Active) {
		t.Fatalf("v2_active must fence V1 new work")
	}
	if IsV1FinancialWorkAllowed(AccountingCutoverState("bogus")) {
		t.Fatalf("bogus state must not allow V1 work")
	}
}

func TestCutoverCoordinatorV2AuthorizedOnlyInActive(t *testing.T) {
	t.Parallel()
	if IsV2NewWorkAuthorized(AccountingCutoverV1Active) {
		t.Fatalf("v1_active must not authorize V2 new work")
	}
	if IsV2NewWorkAuthorized(AccountingCutoverV2Shadow) {
		t.Fatalf("v2_shadow must not authorize V2 new work")
	}
	if IsV2NewWorkAuthorized(AccountingCutoverV1Draining) {
		t.Fatalf("v1_draining must not authorize V2 new work")
	}
	if !IsV2NewWorkAuthorized(AccountingCutoverV2Active) {
		t.Fatalf("v2_active must authorize V2 new work")
	}
	if IsV2NewWorkAuthorized(AccountingCutoverState("bogus")) {
		t.Fatalf("bogus state must not authorize V2 work")
	}
}

func TestCutoverCoordinatorClaimEligibility(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b2a", callID)
	if err != nil {
		t.Fatal(err)
	}
	v1Pinned := PostingPin{StoreID: "s", Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b2a", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 3, MarkerEpoch: 3, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Draining, Status: PostingPinPinned, CreatedAtUnix: 10, UpdatedAtUnix: 10}
	// Pre-drain states allow ordinary V1 claims without pin gating.
	if !IsV1ClaimEligible(AccountingCutoverV1Active, v1Pinned) {
		t.Fatalf("v1_active must allow V1 claim")
	}
	if !IsV1ClaimEligible(AccountingCutoverV2Shadow, v1Pinned) {
		t.Fatalf("v2_shadow must allow V1 claim")
	}
	// Draining allows only correctly pinned V1.
	if !IsV1ClaimEligible(AccountingCutoverV1Draining, v1Pinned) {
		t.Fatalf("draining must allow V1-pinned claim")
	}
	v2Pin := v1Pinned
	v2Pin.Owner = PostingOwnerV2
	v2Pin.MarkerState = AccountingCutoverV2Active
	v2Pin.MarkerGeneration = AccountingCutoverGenerationV2
	if IsV1ClaimEligible(AccountingCutoverV1Draining, v2Pin) {
		t.Fatalf("draining must not allow V2 pin as V1 claim")
	}
	// Active forbids all V1 claims.
	if IsV1ClaimEligible(AccountingCutoverV2Active, v1Pinned) {
		t.Fatalf("v2_active must forbid V1 claim")
	}
}

func TestCutoverCoordinatorConfigBounded(t *testing.T) {
	t.Parallel()
	if _, err := NewCutoverCoordinatorConfig(0); !errors.Is(err, ErrCutoverCoordinatorInvalid) {
		t.Fatalf("zero batch err = %v, want Invalid", err)
	}
	if _, err := NewCutoverCoordinatorConfig(100000); !errors.Is(err, ErrCutoverCoordinatorInvalid) {
		t.Fatalf("oversize batch err = %v, want Invalid", err)
	}
	cfg, err := NewCutoverCoordinatorConfig(50)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BatchSize != 50 {
		t.Fatalf("batch = %d, want 50", cfg.BatchSize)
	}
}

func TestCutoverClaimMetadataForB2b(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b2a-meta", callID)
	if err != nil {
		t.Fatal(err)
	}
	pin := PostingPin{StoreID: "s", Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b2a-meta", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 4, MarkerEpoch: 4, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Draining, Status: PostingPinPinned, CreatedAtUnix: 10, UpdatedAtUnix: 10}
	meta := CutoverClaimMetadataForPin(pin)
	if meta.Owner != PostingOwnerV1 || meta.MarkerVersion != 4 || meta.MarkerEpoch != 4 {
		t.Fatalf("metadata mismatch: %#v", meta)
	}
	if meta.OperationKey != opKey || meta.CallID != callID || meta.Kind != PostingOperationCustomerSettlement {
		t.Fatalf("metadata identity mismatch: %#v", meta)
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("metadata must validate: %v", err)
	}
	bad := meta
	bad.Owner = "bogus"
	if err := bad.Validate(); !errors.Is(err, ErrCutoverCoordinatorInvalid) {
		t.Fatalf("bogus owner err = %v, want Invalid", err)
	}
}
