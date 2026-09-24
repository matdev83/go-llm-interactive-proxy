package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3B1 RED: durable per-operation posting ownership pins.
// Three canonical namespaces: customer call settlement, provider charge/payable,
// financial adjustment. Pin key is StoreID + canonical logical identity, one
// owner only (v1/v2) with marker epoch/generation/version snapshot.

func TestPostingOwnershipKindsAreDistinct(t *testing.T) {
	t.Parallel()
	kinds := []PostingOperationKind{
		PostingOperationCustomerSettlement,
		PostingOperationProviderCharge,
		PostingOperationFinancialAdjustment,
	}
	seen := map[PostingOperationKind]bool{}
	for _, k := range kinds {
		if string(k) == "" {
			t.Fatalf("pin kind must not be empty")
		}
		if seen[k] {
			t.Fatalf("duplicate pin kind %q", k)
		}
		seen[k] = true
		if !k.Valid() {
			t.Fatalf("kind %q must be valid", k)
		}
	}
	if (PostingOperationKind("bogus")).Valid() {
		t.Fatalf("bogus kind must be invalid")
	}
}

func TestPostingOwnershipOwnerReusesWriterLineage(t *testing.T) {
	t.Parallel()
	if PostingOwnerV1 != HistoricalV1WriterVersion {
		t.Fatalf("V1 pin owner %q must equal HistoricalV1WriterVersion %q", PostingOwnerV1, HistoricalV1WriterVersion)
	}
	if PostingOwnerV2 != V2WriterVersion {
		t.Fatalf("V2 pin owner %q must equal V2WriterVersion %q", PostingOwnerV2, V2WriterVersion)
	}
	if PostingPinPinned == PostingPinCompleted {
		t.Fatalf("pin statuses must be distinct")
	}
	if !PostingPinPinned.Valid() || !PostingPinCompleted.Valid() {
		t.Fatalf("pin statuses must be valid")
	}
	if (PostingPinStatus("bogus")).Valid() {
		t.Fatalf("bogus pin status must be invalid")
	}
}

func mustTestCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCustomerPostingOperationKeyUsesCanonicalSettlementIdentity(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	key, err := CustomerPostingOperationKey("acct-b1-customer", callID)
	if err != nil {
		t.Fatalf("CustomerPostingOperationKey: %v", err)
	}
	want, err := CustomerSettlementSourceKey("acct-b1-customer", callID)
	if err != nil {
		t.Fatal(err)
	}
	if key != want {
		t.Fatalf("customer pin key = %q, want canonical %q", key, want)
	}
	if _, err := CustomerPostingOperationKey("", callID); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty account err = %v, want ErrPostingOwnershipInvalid", err)
	}
	if _, err := CustomerPostingOperationKey("acct", BillingCallID("bogus")); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus call err = %v, want ErrPostingOwnershipInvalid", err)
	}
}

func TestProviderPostingOperationKeyUsesCanonicalLineage(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	storeID := "b1-store"
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: "acct-b1-provider", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "b-1"}
	key, err := ProviderPostingOperationKey(storeID, "acct-b1-provider", callID, subject)
	if err != nil {
		t.Fatalf("ProviderPostingOperationKey B-leg: %v", err)
	}
	legKey, err := CallLegUsageKey(callID, "b-1")
	if err != nil {
		t.Fatal(err)
	}
	want, err := ProviderCostSourceKey(legKey)
	if err != nil {
		t.Fatal(err)
	}
	if key != want {
		t.Fatalf("provider pin key = %q, want canonical %q", key, want)
	}
	// Provider-charge subject extends the same canonical lineage.
	chargeSubject := metering.SubjectRef{Kind: metering.SubjectProviderCharge, StoreID: storeID, AccountID: "acct-b1-provider", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "b-1", ProviderAccountKey: "prov-acct", ProviderChargeID: "ch-1"}
	chargeKey, err := ProviderPostingOperationKey(storeID, "acct-b1-provider", callID, chargeSubject)
	if err != nil {
		t.Fatalf("ProviderPostingOperationKey charge: %v", err)
	}
	if chargeKey == key {
		t.Fatalf("provider-charge pin key must differ from B-leg key")
	}
	// Cross-kind: B-leg subject with mismatched call must fail closed.
	badSubject := subject
	badSubject.BillingCallID = mustTestCallID(t).String()
	if _, err := ProviderPostingOperationKey(storeID, "acct-b1-provider", callID, badSubject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("mismatched subject call err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Cross-kind: a_leg subject must never validate as provider charge.
	aLegSubject := metering.SubjectRef{Kind: metering.SubjectALeg, StoreID: storeID, AccountID: "acct-b1-provider", ALegID: "a-1"}
	if _, err := ProviderPostingOperationKey(storeID, "acct-b1-provider", callID, aLegSubject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("a-leg subject err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Malformed: colon in B-leg must fail (canonical lineage forbids ':').
	colonSubject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: "acct-b1-provider", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "bad:leg"}
	if _, err := ProviderPostingOperationKey(storeID, "acct-b1-provider", callID, colonSubject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("colon B-leg err = %v, want ErrPostingOwnershipInvalid", err)
	}
}

func TestFinancialAdjustmentPostingOperationKeyUsesHeadIdentity(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	storeID := "b1-store"
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: "acct-b1-adj", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "b-1"}
	key, err := FinancialAdjustmentPostingOperationKey(storeID, "acct-b1-adj", callID, "head-b-1", subject)
	if err != nil {
		t.Fatalf("FinancialAdjustmentPostingOperationKey: %v", err)
	}
	if key == "" {
		t.Fatalf("adjustment pin key must not be empty")
	}
	again, err := FinancialAdjustmentPostingOperationKey(storeID, "acct-b1-adj", callID, "head-b-1", subject)
	if err != nil {
		t.Fatal(err)
	}
	if again != key {
		t.Fatalf("adjustment pin key must be deterministic: %q vs %q", again, key)
	}
	other, err := FinancialAdjustmentPostingOperationKey(storeID, "acct-b1-adj", callID, "head-b-2", subject)
	if err != nil {
		t.Fatal(err)
	}
	if other == key {
		t.Fatalf("different head keys must produce different pin keys")
	}
	// Cross-kind: customer settlement key must never validate as adjustment head.
	if _, err := FinancialAdjustmentPostingOperationKey(storeID, "acct-b1-adj", callID, "", subject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("empty head err = %v, want ErrPostingOwnershipInvalid", err)
	}
	badSubject := metering.SubjectRef{Kind: metering.SubjectALeg, StoreID: storeID, AccountID: "acct-b1-adj", ALegID: "a-1"}
	if _, err := FinancialAdjustmentPostingOperationKey(storeID, "acct-b1-adj", callID, "head-b-1", badSubject); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("a-leg adjustment subject err = %v, want ErrPostingOwnershipInvalid", err)
	}
}

func TestAcquirePostingPinRequestValidation(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	storeID := "b1-store"
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: "acct-b1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "b-1"}
	// Customer forbids subject/head.
	badCustomer := AcquirePostingPinRequest{Kind: PostingOperationCustomerSettlement, AccountID: "acct-b1", CallID: callID, Subject: subject, Owner: PostingOwnerV1, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if err := badCustomer.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("customer with subject err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Provider forbids head.
	badProvider := AcquirePostingPinRequest{Kind: PostingOperationProviderCharge, AccountID: "acct-b1", CallID: callID, Subject: subject, HeadKey: "head-b-1", Owner: PostingOwnerV1, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if err := badProvider.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("provider with head err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Adjustment requires head.
	badAdj := AcquirePostingPinRequest{Kind: PostingOperationFinancialAdjustment, AccountID: "acct-b1", CallID: callID, Subject: subject, Owner: PostingOwnerV2, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if err := badAdj.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("adjustment without head err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Bogus owner/kind/expected.
	bogusOwner := AcquirePostingPinRequest{Kind: PostingOperationCustomerSettlement, AccountID: "acct-b1", CallID: callID, Owner: "bogus", ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if err := bogusOwner.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus owner err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bogusKind := AcquirePostingPinRequest{Kind: PostingOperationKind("bogus"), AccountID: "acct-b1", CallID: callID, Owner: PostingOwnerV1, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if err := bogusKind.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus kind err = %v, want ErrPostingOwnershipInvalid", err)
	}
	zeroExpected := AcquirePostingPinRequest{Kind: PostingOperationCustomerSettlement, AccountID: "acct-b1", CallID: callID, Owner: PostingOwnerV1}
	if err := zeroExpected.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("zero expected err = %v, want ErrPostingOwnershipInvalid", err)
	}
}

func TestPostingOwnershipMarkerAllowanceForNew(t *testing.T) {
	t.Parallel()
	if !IsPostingOwnerAllowedForNew(AccountingCutoverV1Active, PostingOwnerV1) {
		t.Fatalf("v1_active must permit V1 new")
	}
	if IsPostingOwnerAllowedForNew(AccountingCutoverV1Active, PostingOwnerV2) {
		t.Fatalf("v1_active must forbid V2 new")
	}
	if !IsPostingOwnerAllowedForNew(AccountingCutoverV2Shadow, PostingOwnerV1) {
		t.Fatalf("v2_shadow must permit V1 new")
	}
	if IsPostingOwnerAllowedForNew(AccountingCutoverV2Shadow, PostingOwnerV2) {
		t.Fatalf("v2_shadow must forbid V2 new")
	}
	if IsPostingOwnerAllowedForNew(AccountingCutoverV1Draining, PostingOwnerV1) {
		t.Fatalf("v1_draining must forbid V1 new (only replay/drain)")
	}
	if IsPostingOwnerAllowedForNew(AccountingCutoverV1Draining, PostingOwnerV2) {
		t.Fatalf("v1_draining must forbid V2 new")
	}
	if !IsPostingOwnerAllowedForNew(AccountingCutoverV2Active, PostingOwnerV2) {
		t.Fatalf("v2_active must permit V2 new")
	}
	if IsPostingOwnerAllowedForNew(AccountingCutoverV2Active, PostingOwnerV1) {
		t.Fatalf("v2_active must forbid V1 new (history only)")
	}
}

func TestPostingPinReplayDetection(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b1", callID)
	if err != nil {
		t.Fatal(err)
	}
	pin := PostingPin{StoreID: "s", Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b1", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 1, MarkerEpoch: 1, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Active, Status: PostingPinPinned}
	req := AcquirePostingPinRequest{Kind: PostingOperationCustomerSettlement, AccountID: "acct-b1", CallID: callID, Owner: PostingOwnerV1, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}
	if !IsPostingPinReplay(pin, req, opKey) {
		t.Fatalf("exact replay must be detected")
	}
	otherOwner := req
	otherOwner.Owner = PostingOwnerV2
	if IsPostingPinReplay(pin, otherOwner, opKey) {
		t.Fatalf("conflicting owner must not be replay")
	}
	if IsPostingPinReplay(pin, req, "different-key") {
		t.Fatalf("identity mismatch must not be replay")
	}
	otherKind := req
	otherKind.Kind = PostingOperationProviderCharge
	if IsPostingPinReplay(pin, otherKind, opKey) {
		t.Fatalf("cross-kind must not be replay")
	}
}

func TestPostingPinValidateBounds(t *testing.T) {
	t.Parallel()
	callID := mustTestCallID(t)
	opKey, err := CustomerPostingOperationKey("acct-b1", callID)
	if err != nil {
		t.Fatal(err)
	}
	base := PostingPin{StoreID: "s", Kind: PostingOperationCustomerSettlement, OperationKey: opKey, AccountID: "acct-b1", CallID: callID, Owner: PostingOwnerV1, MarkerVersion: 1, MarkerEpoch: 1, MarkerGeneration: 1, MarkerState: AccountingCutoverV1Active, Status: PostingPinPinned, CreatedAtUnix: 10, UpdatedAtUnix: 10}
	if err := base.Validate(); err != nil {
		t.Fatalf("base pin must validate: %v", err)
	}
	bad := base
	bad.StoreID = "  "
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("blank store err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bad = base
	bad.Owner = "bogus"
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus owner err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bad = base
	bad.MarkerVersion = 0
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("zero marker version err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bad = base
	bad.Status = PostingPinStatus("bogus")
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus status err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bad = base
	bad.Status = PostingPinCompleted
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("completed without completion keys err = %v, want ErrPostingOwnershipInvalid", err)
	}
	bad = base
	bad.CompletionOperationKey = "op"
	if err := bad.Validate(); !errors.Is(err, ErrPostingOwnershipInvalid) {
		t.Fatalf("pinned with completion err = %v, want ErrPostingOwnershipInvalid", err)
	}
}
