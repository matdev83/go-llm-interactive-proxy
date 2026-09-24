package billing

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 B2b4 GREEN (core domain): synchronous adjustment ownership
// identities. Cost pass-through uses canonical head lineage; direct uses
// scoped operation lineage. No fake B-leg/head; selected-cost head identity
// unchanged.

func b2b4MustCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestB2b4CostPassThroughPinKeyIsCanonicalHeadLineage(t *testing.T) {
	t.Parallel()
	callID := b2b4MustCallID(t)
	key, err := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", callID)
	if err != nil {
		t.Fatal(err)
	}
	if !IsCostPassThroughAdjustmentPinKey(key) {
		t.Fatalf("key %q must be cost-pass-through prefix", key)
	}
	if IsSelectedCostAdjustmentPinKey(key) || IsDirectAdjustmentPinKey(key) {
		t.Fatalf("key %q must not collide with other financial_adjustment namespaces", key)
	}
	wantHead := CostPassThroughHeadKey("acct-b2b4", callID)
	if wantHead != "cost-pass-through-head:v1:acct-b2b4:"+callID.String() {
		t.Fatalf("head %q must be canonical account/call lineage", wantHead)
	}
	again, err := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", callID)
	if err != nil || again != key {
		t.Fatalf("pin key must be deterministic: %q vs %q (%v)", key, again, err)
	}
	otherCall := b2b4MustCallID(t)
	other, err := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", otherCall)
	if err != nil {
		t.Fatal(err)
	}
	if other == key {
		t.Fatalf("different calls must not share head pin")
	}
	if _, err := CostPassThroughFinancialAdjustmentPostingOperationKey("", "acct", callID); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty store err = %v, want Invalid", err)
	}
	if _, err := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "", callID); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty account err = %v, want Invalid", err)
	}
}

func TestB2b4DirectPinKeyIsCanonicalScopedLineage(t *testing.T) {
	t.Parallel()
	key, err := DirectFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", "src-1")
	if err != nil {
		t.Fatal(err)
	}
	if !IsDirectAdjustmentPinKey(key) {
		t.Fatalf("key %q must be direct prefix", key)
	}
	if IsSelectedCostAdjustmentPinKey(key) || IsCostPassThroughAdjustmentPinKey(key) {
		t.Fatalf("key %q must not collide", key)
	}
	again, err := DirectFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", "src-1")
	if err != nil || again != key {
		t.Fatalf("direct key must be deterministic")
	}
	other, err := DirectFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", "src-2")
	if err != nil {
		t.Fatal(err)
	}
	if other == key {
		t.Fatalf("different sources must not share direct pin")
	}
	scoped := ScopedOperationKey("adjustment", "acct-b2b4", "src-1")
	if !strings.Contains(scoped, "src-1") {
		t.Fatalf("scoped key %q must carry source", scoped)
	}
	if _, err := DirectFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", ""); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty source err = %v, want Invalid", err)
	}
}

func TestB2b4ResolveOwnersDefaultV1(t *testing.T) {
	t.Parallel()
	owner, err := ResolveCostPassThroughAdjustmentOwner(CostPassThroughRevisionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV1 {
		t.Fatalf("empty cost owner = %q, want v1", owner)
	}
	owner, err = ResolveDirectAdjustmentOwner(AdjustmentInput{})
	if err != nil {
		t.Fatal(err)
	}
	if owner != PostingOwnerV1 {
		t.Fatalf("empty direct owner = %q, want v1", owner)
	}
	if _, err := ResolveCostPassThroughAdjustmentOwner(CostPassThroughRevisionInput{PostingOwner: "bogus"}); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus cost owner err = %v, want Invalid", err)
	}
	if _, err := ResolveDirectAdjustmentOwner(AdjustmentInput{PostingOwner: "bogus"}); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus direct owner err = %v, want Invalid", err)
	}
	callID := b2b4MustCallID(t)
	opKey, _ := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "acct", callID)
	claim := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: opKey, AccountID: "acct", CallID: callID, Owner: PostingOwnerV2, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	if _, err := ResolveCostPassThroughAdjustmentOwner(CostPassThroughRevisionInput{PostingOwner: PostingOwnerV1, Claim: &claim}); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("cost owner/claim mismatch err = %v, want Conflict", err)
	}
	directKey, _ := DirectFinancialAdjustmentPostingOperationKey("test", "acct", "src-1")
	directClaim := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: directKey, AccountID: "acct", Owner: PostingOwnerV2, MarkerVersion: 3, MarkerEpoch: 3, MarkerState: AccountingCutoverV1Draining}
	if _, err := ResolveDirectAdjustmentOwner(AdjustmentInput{PostingOwner: PostingOwnerV1, Claim: &directClaim}); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("direct owner/claim mismatch err = %v, want Conflict", err)
	}
}

func TestB2b4ValidateClaimsRequireCanonicalIdentity(t *testing.T) {
	t.Parallel()
	callID := b2b4MustCallID(t)
	opKey, _ := CostPassThroughFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", callID)
	good := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: opKey, AccountID: "acct-b2b4", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: AccountingCutoverV1Draining}
	if err := ValidateCostPassThroughAdjustmentClaim(good, "test", "acct-b2b4", callID); err != nil {
		t.Fatalf("good cost claim: %v", err)
	}
	badKey := good
	badKey.OperationKey = "tampered"
	if err := ValidateCostPassThroughAdjustmentClaim(badKey, "test", "acct-b2b4", callID); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("bad cost key err = %v, want Conflict", err)
	}
	badCall := good
	badCall.CallID = b2b4MustCallID(t)
	if err := ValidateCostPassThroughAdjustmentClaim(badCall, "test", "acct-b2b4", callID); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("bad cost call must conflict")
	}
	directKey, _ := DirectFinancialAdjustmentPostingOperationKey("test", "acct-b2b4", "src-1")
	dgood := CutoverClaimMetadata{Kind: PostingOperationFinancialAdjustment, OperationKey: directKey, AccountID: "acct-b2b4", Owner: PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: AccountingCutoverV1Draining}
	if err := ValidateDirectAdjustmentClaim(dgood, "test", "acct-b2b4", "src-1"); err != nil {
		t.Fatalf("good direct claim: %v", err)
	}
	dbad := dgood
	// Tamper within the direct namespace so prefix stays direct and the
	// failure surfaces as Conflict (canonical mismatch), not Invalid.
	if len(dbad.OperationKey) > 0 {
		dbad.OperationKey = dbad.OperationKey[:len(dbad.OperationKey)-1] + "0"
		if dbad.OperationKey == dgood.OperationKey {
			dbad.OperationKey += "0"
		}
	}
	if err := ValidateDirectAdjustmentClaim(dbad, "test", "acct-b2b4", "src-1"); !errors.Is(err, ErrPostingOwnershipConflict) {
		t.Fatalf("bad direct key err = %v, want Conflict", err)
	}
	withCall := dgood
	withCall.CallID = b2b4MustCallID(t)
	if err := ValidateDirectAdjustmentClaim(withCall, "test", "acct-b2b4", "src-1"); !errors.Is(err, ErrPostingOwnershipConflict) && !errors.Is(err, ErrCutoverCoordinatorInvalid) {
		t.Fatalf("direct claim with call must fail, got %v", err)
	}
}

func TestB2b4PinValidationAcceptsSyncShapesKeepsHeadUnchanged(t *testing.T) {
	t.Parallel()
	callID := b2b4MustCallID(t)
	// Cost pass-through pin validates via head lineage, no subject.
	costKey, _ := CostPassThroughFinancialAdjustmentPostingOperationKey("s", "acct", callID)
	costPin := PostingPin{StoreID: "s", Kind: PostingOperationFinancialAdjustment, OperationKey: costKey, AccountID: "acct", CallID: callID, HeadKey: CostPassThroughHeadKey("acct", callID), Owner: PostingOwnerV1, MarkerVersion: 1, MarkerEpoch: 1, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Active, Status: PostingPinPinned, CreatedAtUnix: 10, UpdatedAtUnix: 10}
	if err := costPin.Validate(); err != nil {
		t.Fatalf("cost pin must validate: %v", err)
	}
	// Direct pin validates via source in head_key, no call/subject.
	directKey, _ := DirectFinancialAdjustmentPostingOperationKey("s", "acct", "src-1")
	directPin := PostingPin{StoreID: "s", Kind: PostingOperationFinancialAdjustment, OperationKey: directKey, AccountID: "acct", HeadKey: "src-1", Owner: PostingOwnerV1, MarkerVersion: 1, MarkerEpoch: 1, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Active, Status: PostingPinPinned, CreatedAtUnix: 10, UpdatedAtUnix: 10}
	if err := directPin.Validate(); err != nil {
		t.Fatalf("direct pin must validate: %v", err)
	}
	// Selected-cost head pin still requires subject/head (unchanged).
	emptySubject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "s", AccountID: "acct", ALegID: "a", BillingCallID: callID.String(), BLegID: "b"}
	if _, err := FinancialAdjustmentPostingOperationKey("s", "acct", callID, "", emptySubject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty head must still fail for selected-cost")
	}
}
