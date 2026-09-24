package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3B1 RED: durable per-operation posting ownership pins.
// Covers all 3 kinds, exact replay, conflicting owner, stale marker epoch,
// draining no-new but V1 replay/complete, v2_active V2-only, completion
// replay/conflict, store isolation, concurrent contenders, restart,
// malformed/cross-kind, SQLite (+ configured PG in postgres file).

func newPinSQLiteStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	store, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return store
}

func mustPinCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pinBLegSubject(storeID, accountID string, callID billing.BillingCallID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID, ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID}
}

func pinChargeSubject(storeID, accountID string, callID billing.BillingCallID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectProviderCharge, StoreID: storeID, AccountID: accountID, ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID, ProviderAccountKey: "prov-acct", ProviderChargeID: "ch-1"}
}

func ensurePinMarker(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	marker, err := store.EnsureAccountingCutover(context.Background())
	if err != nil {
		t.Fatalf("EnsureAccountingCutover: %v", err)
	}
	return marker
}

func advancePinMarker(t *testing.T, store *DurableStore, current billing.AccountingCutoverMarker, next billing.AccountingCutoverState, transitionID string) billing.AccountingCutoverMarker {
	t.Helper()
	advanced, err := store.TransitionAccountingCutover(context.Background(), billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: next, TransitionID: transitionID})
	if err != nil {
		t.Fatalf("Transition to %q: %v", next, err)
	}
	return advanced
}

func TestPostingOwnershipCustomerAcquireAndGetDistinguishesUnposted(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-customer-get")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	accountID := "acct-b1-customer"
	pin, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: accountID, CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
	if err != nil {
		t.Fatalf("Acquire customer: %v", err)
	}
	if pin.Status != billing.PostingPinPinned {
		t.Fatalf("new pin status = %q, want pinned", pin.Status)
	}
	if pin.IsCompleted() {
		t.Fatalf("new pin must not be completed")
	}
	if pin.CompletionOperationKey != "" || pin.CompletionTransactionID != "" {
		t.Fatalf("unposted pin must carry no completion outcome")
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("Get customer: %v", err)
	}
	if got.OperationKey != opKey || got.Owner != billing.PostingOwnerV1 || got.Status != billing.PostingPinPinned {
		t.Fatalf("Get mismatch: %#v", got)
	}
	if got.IsCompleted() {
		t.Fatalf("Get of unposted pin must not be completed")
	}
}

func TestPostingOwnershipAllThreeKindsAcquire(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-three-kinds")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	// Customer.
	customerCall := mustPinCallID(t)
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-three", CallID: customerCall, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); err != nil {
		t.Fatalf("Acquire customer: %v", err)
	}
	// Provider B-leg.
	providerCall := mustPinCallID(t)
	providerSubject := pinBLegSubject(store.StoreID(), "acct-b1-three", providerCall, "b-1")
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationProviderCharge, AccountID: "acct-b1-three", CallID: providerCall, Subject: providerSubject, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); err != nil {
		t.Fatalf("Acquire provider B-leg: %v", err)
	}
	// Provider charge subject is a distinct canonical identity.
	chargeSubject := pinChargeSubject(store.StoreID(), "acct-b1-three", providerCall, "b-1")
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationProviderCharge, AccountID: "acct-b1-three", CallID: providerCall, Subject: chargeSubject, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); err != nil {
		t.Fatalf("Acquire provider charge: %v", err)
	}
	// Financial adjustment per head.
	adjCall := mustPinCallID(t)
	adjSubject := pinBLegSubject(store.StoreID(), "acct-b1-three", adjCall, "b-9")
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationFinancialAdjustment, AccountID: "acct-b1-three", CallID: adjCall, Subject: adjSubject, HeadKey: "head-b-9", Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); err != nil {
		t.Fatalf("Acquire adjustment: %v", err)
	}
}

func TestPostingOwnershipExactReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-replay")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	req := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-replay", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}
	first, err := store.AcquirePostingPin(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AcquirePostingPin(ctx, req)
	if err != nil {
		t.Fatalf("exact replay must succeed: %v", err)
	}
	if second.OperationKey != first.OperationKey || second.Owner != first.Owner || second.MarkerVersion != first.MarkerVersion || second.Status != first.Status {
		t.Fatalf("replay mismatch: %#v vs %#v", second, first)
	}
}

func TestPostingOwnershipConflictingOwnerIsConflict(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-conflict-owner")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	v1Req := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-conflict", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}
	if _, err := store.AcquirePostingPin(ctx, v1Req); err != nil {
		t.Fatal(err)
	}
	v2Req := v1Req
	v2Req.Owner = billing.PostingOwnerV2
	if _, err := store.AcquirePostingPin(ctx, v2Req); !errors.Is(err, billing.ErrPostingOwnershipConflict) && !errors.Is(err, billing.ErrPostingOwnershipFence) {
		t.Fatalf("conflicting owner err = %v, want Conflict/Fence", err)
	}
}

func TestPostingOwnershipStaleMarkerEpochIsFence(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-stale")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	stale := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-stale", CallID: mustPinCallID(t), Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version + 100, ExpectedMarkerEpoch: marker.Epoch + 100}
	if _, err := store.AcquirePostingPin(ctx, stale); !errors.Is(err, billing.ErrPostingOwnershipFence) {
		t.Fatalf("stale err = %v, want ErrPostingOwnershipFence", err)
	}
}

func TestPostingOwnershipDrainingForbidsNewButReplaysV1(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-draining")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	// Pin one V1 operation before draining.
	existingCall := mustPinCallID(t)
	existingReq := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-drain", CallID: existingCall, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}
	existing, err := store.AcquirePostingPin(ctx, existingReq)
	if err != nil {
		t.Fatal(err)
	}
	// Advance to draining: v1_active -> v2_shadow -> v1_draining.
	shadow := advancePinMarker(t, store, marker, billing.AccountingCutoverV2Shadow, "b1-drain-shadow")
	draining := advancePinMarker(t, store, shadow, billing.AccountingCutoverV1Draining, "b1-drain-draining")
	// New unpinned V1 must be forbidden.
	fresh := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-drain", CallID: mustPinCallID(t), Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: draining.Version, ExpectedMarkerEpoch: draining.Epoch}
	if _, err := store.AcquirePostingPin(ctx, fresh); !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("draining new V1 err = %v, want Fence/Invalid", err)
	}
	// Existing V1 replay with fresh marker must succeed (drain).
	replayReq := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-drain", CallID: existingCall, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: draining.Version, ExpectedMarkerEpoch: draining.Epoch}
	replayed, err := store.AcquirePostingPin(ctx, replayReq)
	if err != nil {
		t.Fatalf("draining V1 replay must succeed: %v", err)
	}
	if replayed.OperationKey != existing.OperationKey {
		t.Fatalf("draining replay key mismatch")
	}
	// Existing V1 completion (drain) must succeed.
	completed, err := store.CompletePostingPin(ctx, billing.CompletePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-drain", CallID: existingCall, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: draining.Version, ExpectedMarkerEpoch: draining.Epoch, CompletionOperationKey: existing.OperationKey, CompletionTransactionID: "tx-drain-1"})
	if err != nil {
		t.Fatalf("draining V1 complete must succeed: %v", err)
	}
	if !completed.IsCompleted() || completed.CompletionTransactionID != "tx-drain-1" {
		t.Fatalf("draining completion mismatch: %#v", completed)
	}
	// V2 new during draining must be forbidden.
	v2Fresh := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-drain", CallID: mustPinCallID(t), Owner: billing.PostingOwnerV2, ExpectedMarkerVersion: draining.Version, ExpectedMarkerEpoch: draining.Epoch}
	if _, err := store.AcquirePostingPin(ctx, v2Fresh); !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrPostingOwnershipInvalid) && !errors.Is(err, billing.ErrPostingOwnershipConflict) {
		t.Fatalf("draining V2 new err = %v, want Fence/Invalid/Conflict", err)
	}
}

func TestPostingOwnershipV2ActiveIsV2Only(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-v2active")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	shadow := advancePinMarker(t, store, marker, billing.AccountingCutoverV2Shadow, "b1-v2a-shadow")
	draining := advancePinMarker(t, store, shadow, billing.AccountingCutoverV1Draining, "b1-v2a-draining")
	active := advancePinMarker(t, store, draining, billing.AccountingCutoverV2Active, "b1-v2a-active")
	// V1 new must be forbidden.
	v1Fresh := billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-v2a", CallID: mustPinCallID(t), Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: active.Version, ExpectedMarkerEpoch: active.Epoch}
	if _, err := store.AcquirePostingPin(ctx, v1Fresh); !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("v2_active V1 new err = %v, want Fence/Invalid", err)
	}
	// V2 new must succeed.
	v2Call := mustPinCallID(t)
	v2Pin, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-v2a", CallID: v2Call, Owner: billing.PostingOwnerV2, ExpectedMarkerVersion: active.Version, ExpectedMarkerEpoch: active.Epoch})
	if err != nil {
		t.Fatalf("v2_active V2 new must succeed: %v", err)
	}
	if v2Pin.Owner != billing.PostingOwnerV2 || v2Pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("v2 pin mismatch: %#v", v2Pin)
	}
	// V2 replay must succeed.
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-v2a", CallID: v2Call, Owner: billing.PostingOwnerV2, ExpectedMarkerVersion: active.Version, ExpectedMarkerEpoch: active.Epoch}); err != nil {
		t.Fatalf("v2_active V2 replay must succeed: %v", err)
	}
}

func TestPostingOwnershipCompletionReplayAndConflict(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-completion")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	accountID := "acct-b1-complete"
	acquired, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: accountID, CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	completeReq := billing.CompletePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: accountID, CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch, CompletionOperationKey: acquired.OperationKey, CompletionTransactionID: "tx-complete-1"}
	completed, err := store.CompletePostingPin(ctx, completeReq)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !completed.IsCompleted() {
		t.Fatalf("completed pin must report completed")
	}
	// Exact replay with same outcome must succeed.
	replayed, err := store.CompletePostingPin(ctx, completeReq)
	if err != nil {
		t.Fatalf("completion replay must succeed: %v", err)
	}
	if replayed.CompletionTransactionID != "tx-complete-1" {
		t.Fatalf("completion replay mismatch: %#v", replayed)
	}
	// Conflicting completion outcome must be rejected.
	conflictReq := completeReq
	conflictReq.CompletionTransactionID = "tx-different"
	if _, err := store.CompletePostingPin(ctx, conflictReq); !errors.Is(err, billing.ErrPostingOwnershipConflict) {
		t.Fatalf("conflicting completion err = %v, want ErrPostingOwnershipConflict", err)
	}
	// Get must now show completed with posting outcome.
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsCompleted() || got.CompletionOperationKey != acquired.OperationKey {
		t.Fatalf("Get after complete mismatch: %#v", got)
	}
}

func TestPostingOwnershipStoreIsolation(t *testing.T) {
	t.Parallel()
	base := newSQLiteTestStore(t)
	ctx := context.Background()
	storeA, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "b1-iso-a"})
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "b1-iso-b"})
	if err != nil {
		t.Fatal(err)
	}
	markerA := ensurePinMarker(t, storeA)
	_ = ensurePinMarker(t, storeB)
	callID := mustPinCallID(t)
	if _, err := storeA.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-iso", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: markerA.Version, ExpectedMarkerEpoch: markerA.Epoch}); err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b1-iso", callID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
		t.Fatalf("isolated Get err = %v, want ErrPostingOwnershipNotFound", err)
	}
}

func TestPostingOwnershipConcurrentContenders(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-contenders")
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	const contenders = 8
	var wg sync.WaitGroup
	errs := make([]error, contenders)
	pins := make([]billing.PostingPin, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p, err := store.AcquirePostingPin(context.Background(), billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-contend", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
			pins[idx] = p
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("contender %d: %v", i, errs[i])
		}
	}
	for i := 1; i < contenders; i++ {
		if pins[i].OperationKey != pins[0].OperationKey || pins[i].Owner != pins[0].Owner {
			t.Fatalf("contender diverged: %#v vs %#v", pins[i], pins[0])
		}
	}
}

func TestPostingOwnershipRestartSafe(t *testing.T) {
	t.Parallel()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "billing.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "b1-restart"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	accountID := "acct-b1-restart"
	acquired, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: accountID, CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePostingPin(ctx, billing.CompletePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: accountID, CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch, CompletionOperationKey: acquired.OperationKey, CompletionTransactionID: "tx-restart-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB2.SetMaxOpenConns(4)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		_ = sqlDB2.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB2)
	reopened, err := NewDurableStore(context.Background(), bunDB2, Config{StoreID: "b1-restart"})
	if err != nil {
		_ = bunDB2.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsCompleted() || got.CompletionTransactionID != "tx-restart-1" || got.OperationKey != acquired.OperationKey {
		t.Fatalf("restart mismatch: %#v vs %#v", got, acquired)
	}
}

func TestPostingOwnershipMalformedAndCrossKind(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-malformed")
	ctx := context.Background()
	marker := ensurePinMarker(t, store)
	callID := mustPinCallID(t)
	// Customer with provider subject must fail closed.
	subject := pinBLegSubject(store.StoreID(), "acct-b1-bad", callID, "b-1")
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-bad", CallID: callID, Subject: subject, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("customer with subject err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Provider with customer-style missing subject must fail.
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationProviderCharge, AccountID: "acct-b1-bad", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("provider without subject err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Adjustment with provider subject but missing head must fail.
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationFinancialAdjustment, AccountID: "acct-b1-bad", CallID: callID, Subject: subject, Owner: billing.PostingOwnerV2, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("adjustment without head err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Bogus kind must fail.
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationKind("bogus"), "key"); !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("bogus kind Get err = %v, want ErrPostingOwnershipInvalid", err)
	}
	// Empty operation key must fail.
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, ""); !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("empty key Get err = %v, want ErrPostingOwnershipInvalid", err)
	}
}

func TestPostingOwnershipContextAware(t *testing.T) {
	t.Parallel()
	store := newPinSQLiteStore(t, "b1-ctx")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AcquirePostingPin(canceled, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "a", CallID: mustPinCallID(t), Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: 1, ExpectedMarkerEpoch: 1}); err == nil {
		t.Fatalf("canceled Acquire must fail")
	}
	if _, err := store.GetPostingPin(canceled, billing.PostingOperationCustomerSettlement, "k"); err == nil {
		t.Fatalf("canceled Get must fail")
	}
	var nilCtx context.Context
	if _, err := store.AcquirePostingPin(nilCtx, billing.AcquirePostingPinRequest{}); err == nil {
		t.Fatalf("nil Acquire must fail")
	}
}
