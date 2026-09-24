package billing

import (
	"context"
	"errors"
	"testing"
)

// Phase 17.3 B2b2 RED (core domain): provider charge/payable posting-time
// ownership fence. Must FAIL before B2b2 store/worker fence, PASS after.

func b2b2MustCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestB2b2ResolveOwnerDefaultsV1AndValidates(t *testing.T) {
	t.Parallel()
	owner, err := ResolveProviderCostOwner(ApplyProviderCostInput{})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV1 {
		t.Fatalf("empty owner = %q, want v1", owner)
	}
	owner, err = ResolveProviderCostOwner(ApplyProviderCostInput{PostingOwner: PostingOwnerV2})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("V2 owner = %q", owner)
	}
	if _, err := ResolveProviderCostOwner(ApplyProviderCostInput{PostingOwner: "bogus"}); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus owner err = %v, want Invalid", err)
	}
	callID := b2b2MustCallID(t)
	legKey, err := CallLegUsageKey(callID, "b-1")
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := ProviderCostSourceKey(legKey)
	if err != nil {
		t.Fatal(err)
	}
	claim := CutoverClaimMetadata{Kind: PostingOperationProviderCharge, OperationKey: opKey, AccountID: "acct", CallID: callID, Owner: PostingOwnerV2, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	if _, err := ResolveProviderCostOwner(ApplyProviderCostInput{PostingOwner: PostingOwnerV1, Claim: &claim}); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("owner/claim mismatch err = %v, want Conflict", err)
	}
	owner, err = ResolveProviderCostOwner(ApplyProviderCostInput{Claim: &claim})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("claim owner = %q, want v2", owner)
	}
}

func TestB2b2ValidateClaimRequiresCanonicalIdentity(t *testing.T) {
	t.Parallel()
	callID := b2b2MustCallID(t)
	legKey, err := CallLegUsageKey(callID, "b-1")
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := ProviderCostSourceKey(legKey)
	if err != nil {
		t.Fatal(err)
	}
	good := CutoverClaimMetadata{Kind: PostingOperationProviderCharge, OperationKey: opKey, AccountID: "acct-b2b2", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: AccountingCutoverV1Draining}
	if err := ValidateProviderCostClaim(good, "acct-b2b2", callID, legKey); err != nil {
		t.Fatalf("good claim must validate: %v", err)
	}
	badKind := good
	badKind.Kind = PostingOperationCustomerSettlement
	if err := ValidateProviderCostClaim(badKind, "acct-b2b2", callID, legKey); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("wrong kind err = %v, want Invalid", err)
	}
	badKey := good
	badKey.OperationKey = "tampered"
	if err := ValidateProviderCostClaim(badKey, "acct-b2b2", callID, legKey); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong key err = %v, want Conflict", err)
	}
	if err := ValidateProviderCostClaim(good, "other", callID, legKey); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong account must conflict")
	}
	otherCall := b2b2MustCallID(t)
	if err := ValidateProviderCostClaim(good, "acct-b2b2", otherCall, legKey); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong call must conflict")
	}
	if err := ValidateProviderChargeOperationKind("customer_call_settlement"); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("customer kind err = %v, want Invalid", err)
	}
	if err := ValidateProviderChargeOperationKind(""); err != nil {
		t.Fatalf("empty kind must allow legacy default: %v", err)
	}
}

// fakeProviderClaimProvider implements the narrow claim port for worker tests.
type fakeProviderClaimProvider struct {
	meta CutoverClaimMetadata
	err  error
}

func (f fakeProviderClaimProvider) GetCutoverClaimMetadata(context.Context, PostingOperationKind, string) (CutoverClaimMetadata, error) {
	return f.meta, f.err
}

func TestB2b2WorkerPassesClaimMetadata(t *testing.T) {
	t.Parallel()
	callID := b2b2MustCallID(t)
	leg := testCallLegUsageRecord(callID, "b-1")
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	meta := CutoverClaimMetadata{Kind: PostingOperationProviderCharge, OperationKey: opKey, AccountID: "acct-b2b2-worker", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	reader := &providerCostWorkReaderStub{work: []ProviderCostWork{
		{AccountID: "acct-b2b2-worker", CallID: callID, Leg: leg},
	}}
	store := &providerCostStoreStub{}
	worker, err := NewCallProviderCostWorkerWithClaim(reader, store, providerCostResolverStub{}, fakeProviderClaimProvider{meta: meta}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if len(store.applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(store.applied))
	}
	got := store.applied[0]
	if got.Claim == nil {
		t.Fatalf("worker must pass B2a claim metadata, got nil (reread-only TOCTOU)")
	}
	if got.Claim.OperationKey != opKey || got.Claim.Owner != PostingOwnerV1 || got.Claim.MarkerEpoch != 3 {
		t.Fatalf("worker claim mismatch: %#v", got.Claim)
	}
	if got.PostingOwner != PostingOwnerV1 {
		t.Fatalf("worker owner = %q, want v1", got.PostingOwner)
	}
}

func TestB2b2ProductionWorkerWithoutClaimPortMustReject(t *testing.T) {
	t.Parallel()
	reader := &providerCostWorkReaderStub{}
	store := &providerCostStoreStub{}
	if _, err := NewCallProviderCostWorkerWithClaim(reader, store, providerCostResolverStub{}, nil, 8); err == nil {
		t.Fatalf("production worker without claim metadata port must reject (no optional bypass)")
	}
}
