package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Phase 17.3 B2b1 domain + worker claim boundary (customer_call_settlement only).

func TestB2b1ResolveOwnerDefaultsV1AndValidates(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	// Empty preserves legacy V1.
	owner, err := ResolveCustomerSettlementOwner(ApplyCallBillingInput{})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV1 {
		t.Fatalf("empty owner = %q, want v1", owner)
	}
	// Explicit V2 allowed at domain layer (store fences by state).
	owner, err = ResolveCustomerSettlementOwner(ApplyCallBillingInput{PostingOwner: PostingOwnerV2})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("V2 owner = %q", owner)
	}
	// Bogus owner fails closed.
	if _, err := ResolveCustomerSettlementOwner(ApplyCallBillingInput{PostingOwner: "bogus"}); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus owner err = %v, want Invalid", err)
	}
	// Claim owner mismatch fails as conflict (proves claim is validated, not ignored).
	opKey, err := CustomerPostingOperationKey("acct-b2b1-dom", callID)
	if err != nil {
		t.Fatal(err)
	}
	claim := CutoverClaimMetadata{Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b2b1-dom", CallID: callID, Owner: PostingOwnerV2, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	if _, err := ResolveCustomerSettlementOwner(ApplyCallBillingInput{PostingOwner: PostingOwnerV1, Claim: &claim}); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("owner/claim mismatch err = %v, want Conflict", err)
	}
	// Claim alone selects owner.
	owner, err = ResolveCustomerSettlementOwner(ApplyCallBillingInput{Claim: &claim})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV2 {
		t.Fatalf("claim owner = %q, want v2", owner)
	}
}

func TestB2b1ValidateClaimRequiresCanonicalIdentity(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b2b1-claim", callID)
	if err != nil {
		t.Fatal(err)
	}
	good := CutoverClaimMetadata{Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b2b1-claim", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: AccountingCutoverV1Draining}
	if err := ValidateCustomerSettlementClaim(good, "acct-b2b1-claim", callID); err != nil {
		t.Fatalf("good claim must validate: %v", err)
	}
	// Wrong kind (provider) must fail.
	badKind := good
	badKind.Kind = PostingOperationProviderCharge
	if err := ValidateCustomerSettlementClaim(badKind, "acct-b2b1-claim", callID); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("wrong kind err = %v, want Invalid", err)
	}
	// Wrong key must fail as conflict.
	badKey := good
	badKey.OperationKey = "tampered"
	if err := ValidateCustomerSettlementClaim(badKey, "acct-b2b1-claim", callID); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong key err = %v, want Conflict", err)
	}
	// Wrong account must fail.
	if err := ValidateCustomerSettlementClaim(good, "other-acct", callID); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong account must conflict")
	}
	// Wrong call must fail.
	otherCall := mustTestCallID(t)
	if err := ValidateCustomerSettlementClaim(good, "acct-b2b1-claim", otherCall); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("wrong call must conflict")
	}
	// Operation kind gate rejects provider/adjustment via customer fence.
	if err := ValidateCustomerSettlementOperationKind("provider_charge"); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("provider kind err = %v, want Invalid", err)
	}
	if err := ValidateCustomerSettlementOperationKind(""); err != nil {
		t.Fatalf("empty kind must allow legacy default: %v", err)
	}
}

// fakeClaimProvider implements CutoverClaimMetadataProvider for worker tests.
type fakeClaimProvider struct {
	meta CutoverClaimMetadata
	err  error
}

func (f fakeClaimProvider) GetCutoverClaimMetadata(context.Context, PostingOperationKind, string) (CutoverClaimMetadata, error) {
	return f.meta, f.err
}

type fakeWorkerUsage struct {
	CallUsageStore
	provider CutoverClaimMetadataProvider
	claims   []CompleteCall
	exposure CallExposure
}

func (f *fakeWorkerUsage) ClaimCompleteCalls(context.Context, int) ([]CompleteCall, error) {
	return f.claims, nil
}

func (f *fakeWorkerUsage) GetCallExposure(context.Context, BillingCallID) (CallExposure, error) {
	return f.exposure, nil
}
func (f *fakeWorkerUsage) RetryCompleteCall(context.Context, BillingCallID, string) error { return nil }
func (f *fakeWorkerUsage) GetCutoverClaimMetadata(ctx context.Context, k PostingOperationKind, key string) (CutoverClaimMetadata, error) {
	return f.provider.GetCutoverClaimMetadata(ctx, k, key)
}

type fakeWorkerSettlement struct {
	got []ApplyCallBillingInput
	err error
}

func (f *fakeWorkerSettlement) ApplyCallBillingResult(_ context.Context, in ApplyCallBillingInput) (CallSettlement, error) {
	f.got = append(f.got, in)
	return CallSettlement{CallID: in.Call.CallID}, f.err
}

func TestB2b1WorkerPassesClaimMetadata(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b2b1-worker", callID)
	if err != nil {
		t.Fatal(err)
	}
	meta := CutoverClaimMetadata{Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b2b1-worker", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	closure := CallUsageRecord{SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-b2b1-worker", ALegID: "a-1", SessionID: "s-1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: TurnOutcomeCompleted, CustomerPricingRef: VersionRef{ID: "p", Version: "v1"}, ChargePolicyRef: VersionRef{ID: "c", Version: "v1"}, ExpectedBLegIDs: []string{"b-1"}}
	sealed, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}
	usage := &fakeWorkerUsage{
		provider: fakeClaimProvider{meta: meta},
		claims:   []CompleteCall{{Closure: sealed}},
		exposure: CallExposure{AccountID: sealed.AccountID, CallID: sealed.CallID.String(), Max: Money{Nano: 60, Currency: "USD"}, Status: ExposureOpen},
	}
	settlement := &fakeWorkerSettlement{}
	worker, err := NewCallPostUsageWorker(usage, settlement, fakeRatingForWorker{callID: callID}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if len(settlement.got) != 1 {
		t.Fatalf("settlement calls = %d, want 1", len(settlement.got))
	}
	got := settlement.got[0]
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

type fakeRatingForWorker struct {
	callID BillingCallID
}

func (f fakeRatingForWorker) ResolveCallRating(_ context.Context, _ CompleteCall, _ CallExposure) (CallRatingResult, error) {
	return CallRatingResult{CallID: f.callID, CustomerCharge: Money{Nano: 10, Currency: "USD"}, Fingerprint: "fp-worker"}, nil
}
