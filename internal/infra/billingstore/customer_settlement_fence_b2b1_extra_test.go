package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// Phase 17.3 B2b1 extras: V2 positive, crash/fault atomicity, V1/V2 contenders,
// bogus claim validation, file reopen, unit/exposure exact. Customer only.

func TestB2b1V2ActiveV2PostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-v2-positive")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b1-v2pos", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 200, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b1MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 80, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Advance to v2_active with pending work (force CAS for test; production
	// Activate would block until drained, but the V2 fence must still authorize
	// V2 new work once active).
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b1-v2p-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b1-v2p-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	// F2A: admission now durably pins V1 ownership at AdmitExposure time.
	// Genuine V2 new work after activation carries no prior V1 pin (future V2
	// admission path, F3). Reusing the same V1-admitted call for V2 would
	// correctly conflict as one-operation-one-owner. Simulate V2-new work by
	// clearing the V1 pin so the V2 seam acquires fresh V2 ownership.
	v2opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationCustomerSettlement), v2opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b1-v2p-active"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckV2NewWorkAuthorized(ctx); err != nil {
		t.Fatalf("V2 must be authorized in active: %v", err)
	}
	// V2 settlement via the same atomic seam (real money, V2 ownership, not fake).
	// Valid V2 component valuation is required at the generic fence; a
	// scalar-only result would fail before reaching the pin.
	res := f3BoundResult(t, call, 40)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV2})
	if err != nil {
		t.Fatalf("v2_active V2 settle: %v (V2 seam must post, not fake)", err)
	}
	if settled.Replayed {
		t.Fatalf("first V2 settlement must not be replayed")
	}
	if got := b2b1Balance(t, store, acct.ID); got != 160 {
		t.Fatalf("V2 balance = %d, want 160", got)
	}
	if n := b2b1JournalCount(t, store, acct.ID); n != 1 {
		t.Fatalf("V2 journals = %d, want 1", n)
	}
	if b2b1ExposureOpen(t, store, callID) {
		t.Fatalf("V2 exposure must close")
	}
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("V2 pin missing: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() {
		t.Fatalf("V2 pin must be completed, got %#v", pin)
	}
	if pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("V2 pin marker = %q, want v2_active", pin.MarkerState)
	}
	// V2 exact replay returns outcome without second money.
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV2})
	if err != nil {
		t.Fatalf("V2 replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("V2 second must be replayed")
	}
	if got := b2b1Balance(t, store, acct.ID); got != 160 {
		t.Fatalf("V2 replay balance = %d, want 160", got)
	}
}

func TestB2b1BogusClaimFailsEvenWhenRereadWouldAllow(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-bogus-claim")
	ctx := context.Background()
	_ = b2b1EnsureShadow(t, store)
	callID := b2b1MustCallID(t)
	acct := billing.Account{ID: "acct-b2b1-bogus", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b1-bogus-drain"); err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	good, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	res := f3BoundResult(t, call, 25)
	// Stale epoch claim must fence even though pin+marker would otherwise allow.
	stale := good
	stale.MarkerEpoch += 100
	stale.MarkerVersion += 100
	beforeBal := b2b1Balance(t, store, acct.ID)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV1, Claim: &stale}); !isFenceErr(err) {
		t.Fatalf("stale claim err = %v, want fence (claim must be validated, not ignored)", err)
	}
	if got := b2b1Balance(t, store, acct.ID); got != beforeBal {
		t.Fatalf("bogus claim mutated balance")
	}
	// Wrong owner claim must conflict even though reread V1 pin would allow.
	wrongOwner := good
	wrongOwner.Owner = billing.PostingOwnerV2
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV2, Claim: &wrongOwner}); !isFenceErr(err) {
		t.Fatalf("wrong-owner claim err = %v, want fence/conflict", err)
	}
	// Correct claim posts (proves fence is claim-sensitive, not blanket).
	correct := good
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV1, Claim: &correct})
	if err != nil {
		t.Fatalf("correct claim must post: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("correct claim first must not be replayed")
	}
	if got := b2b1Balance(t, store, acct.ID); got != 75 {
		t.Fatalf("correct claim balance = %d, want 75", got)
	}
}

func TestB2b1CrashAtBoundariesIsAtomic(t *testing.T) {
	t.Parallel()
	points := []string{"b2b1-pin-acquire", "b2b1-before-effects", "b2b1-before-journal", "b2b1-before-pin-complete", "b2b1-before-commit"}
	for _, point := range points {
		func(pt string) {
			store := b2b1NewStore(t, "b2b1-crash-"+pt)
			ctx := context.Background()
			call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-crash-"+pt, 100, 60, 25)
			// F2A: admission now pins V1 at AdmitExposure time. The
			// b2b1-pin-acquire fault proves atomic pin+money acquisition when
			// no pin exists (legacy/classification gap). Clear the admission
			// pin for that point to force the acquisition path.
			if pt == "b2b1-pin-acquire" {
				opKey, _ := billing.CustomerPostingOperationKey("acct-b2b1-crash-"+pt, call.CallID)
				if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
					store.StoreID(), string(billing.PostingOperationCustomerSettlement), opKey).Exec(ctx); err != nil {
					t.Fatalf("point %s clear pin: %v", pt, err)
				}
			}
			// Fail once at this boundary.
			failed := false
			store.settlementFaultHook = func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b1 injected crash at %s", p)
				}
				return nil
			}
			beforeBal := b2b1Balance(t, store, "acct-b2b1-crash-"+pt)
			_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
			if err == nil {
				t.Fatalf("point %s must fail", pt)
			}
			// Zero effects: no journal, balance unchanged, exposure open, not processed, pin not completed.
			if got := b2b1Balance(t, store, "acct-b2b1-crash-"+pt); got != beforeBal {
				t.Fatalf("point %s mutated balance %d -> %d", pt, beforeBal, got)
			}
			if n := b2b1JournalCount(t, store, "acct-b2b1-crash-"+pt); n != 0 {
				t.Fatalf("point %s wrote %d journals", pt, n)
			}
			if !b2b1ExposureOpen(t, store, call.CallID) {
				t.Fatalf("point %s must leave exposure open", pt)
			}
			if st := b2b1ClaimStatus(t, store, call.CallID); st == "processed" {
				t.Fatalf("point %s must not mark processed", pt)
			}
			opKey, _ := billing.CustomerPostingOperationKey("acct-b2b1-crash-"+pt, call.CallID)
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("point %s Get pin: %v", pt, perr)
			}
			// Retry after crash succeeds exactly once.
			store.settlementFaultHook = nil
			settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
			if err != nil {
				t.Fatalf("point %s retry: %v", pt, err)
			}
			if settled.Replayed {
				t.Fatalf("point %s retry must not be replayed (first never committed)", pt)
			}
			if got := b2b1Balance(t, store, "acct-b2b1-crash-"+pt); got != 75 {
				t.Fatalf("point %s retry balance = %d, want 75", pt, got)
			}
			if n := b2b1JournalCount(t, store, "acct-b2b1-crash-"+pt); n != 1 {
				t.Fatalf("point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
}

func TestB2b1ConcurrentV1VsV2SingleWinner(t *testing.T) {
	t.Parallel()
	// v1_active: V1 wins, V2 fenced. Exactly once.
	v1store := b2b1NewStore(t, "b2b1-contend-v1wins")
	v1ctx := context.Background()
	v1call, v1exp, v1res := b2b1SetupAccountCallExposure(t, v1store, "acct-b2b1-v1wins", 500, 400, 50)
	// Valid V2 valuation for the V2 contender so the fence proves ownership
	// (V2 not authorized in v1_active), not merely missing valuation.
	v1v2res := v1res
	v1v2res.CustomerValuation = f3BoundComponentValuation(t, v1call, v1res.CustomerCharge)
	v1v2res.Fingerprint = v1v2res.CustomerValuation.Fingerprint()
	var wg sync.WaitGroup
	var v1Err, v2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, v1Err = v1store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{Call: v1call, Exposure: v1exp, Result: v1res, PostingOwner: billing.PostingOwnerV1})
	}()
	go func() {
		defer wg.Done()
		_, v2Err = v1store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{Call: v1call, Exposure: v1exp, Result: v1v2res, PostingOwner: billing.PostingOwnerV2})
	}()
	wg.Wait()
	if v1Err != nil {
		t.Fatalf("v1_active V1 contender must win: %v", v1Err)
	}
	if !isFenceErr(v2Err) {
		t.Fatalf("v1_active V2 contender err = %v, want fence", v2Err)
	}
	if got := b2b1Balance(t, v1store, "acct-b2b1-v1wins"); got != 450 {
		t.Fatalf("V1-wins balance = %d, want 450 (exactly once)", got)
	}
	if n := b2b1JournalCount(t, v1store, "acct-b2b1-v1wins"); n != 1 {
		t.Fatalf("V1-wins journals = %d, want 1", n)
	}
	opKey, _ := billing.CustomerPostingOperationKey("acct-b2b1-v1wins", v1call.CallID)
	pin, err := v1store.GetPostingPin(v1ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("V1-wins pin must be V1 completed, got %#v", pin)
	}
	// v2_active: V2 wins, V1 fenced. Exactly once.
	v2store := b2b1NewStore(t, "b2b1-contend-v2wins")
	v2ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b1-v2wins", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 500, State: billing.AccountReady, Version: 1}
	if err := v2store.CreateAccount(v2ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b1MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := v2store.AppendCallUsage(v2ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := v2store.AppendCallLegUsage(v2ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	exp, err := v2store.AdmitExposure(v2ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 400, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := v2store.EnsureAccountingCutover(v2ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := v2store.TransitionAccountingCutover(v2ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b1-cv2-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := v2store.TransitionAccountingCutover(v2ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b1-cv2-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	// F2A: admission pins V1; genuine V2-new work after activation carries no
	// prior V1 pin. Clear it to simulate V2-new ownership (same as V2 positive).
	cv2opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		v2store.StoreID(), string(billing.PostingOperationCustomerSettlement), cv2opKey).Exec(v2ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := v2store.GetAccountingCutover(v2ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2store.TransitionAccountingCutover(v2ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b1-cv2-active"}); err != nil {
		t.Fatal(err)
	}
	res := f3BoundResult(t, call, 50)
	var wv1Err, wv2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, wv1Err = v2store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV1})
	}()
	go func() {
		defer wg.Done()
		_, wv2Err = v2store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV2})
	}()
	wg.Wait()
	if wv2Err != nil {
		t.Fatalf("v2_active V2 contender must win: %v", wv2Err)
	}
	if !isFenceErr(wv1Err) {
		t.Fatalf("v2_active V1 contender err = %v, want fence", wv1Err)
	}
	if got := b2b1Balance(t, v2store, acct.ID); got != 450 {
		t.Fatalf("V2-wins balance = %d, want 450", got)
	}
	if n := b2b1JournalCount(t, v2store, acct.ID); n != 1 {
		t.Fatalf("V2-wins journals = %d, want 1", n)
	}
}

func TestB2b1FileReopenPreservesPinAndOutcome(t *testing.T) {
	t.Parallel()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "b2b1-reopen-file.db")))
	open := func(storeID string) (*DurableStore, func()) {
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
		s, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		return s, func() { _ = s.Close() }
	}
	store, closeFn := open("b2b1-file-reopen")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b1-file", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b1MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-file-fp"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err != nil {
		t.Fatal(err)
	}
	closeFn()
	reopened, close2 := open("b2b1-file-reopen")
	defer close2()
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("file reopen pin: %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("file reopen pin must be completed")
	}
	if got := b2b1Balance(t, reopened, acct.ID); got != 75 {
		t.Fatalf("file reopen balance = %d, want 75", got)
	}
	if n := b2b1JournalCount(t, reopened, acct.ID); n != 1 {
		t.Fatalf("file reopen journals = %d, want 1", n)
	}
	if b2b1ExposureOpen(t, reopened, callID) {
		t.Fatalf("file reopen exposure must be closed")
	}
	// Unit exact: no unit ops for plain money settlement.
	if n := b2b1UnitCount(t, reopened); n != 0 {
		t.Fatalf("file reopen units = %d, want 0", n)
	}
}

func TestB2b1ConflictingOwnerEpochAmountSourceFail(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-conflicts")
	ctx := context.Background()
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-conf", 100, 60, 25)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err != nil {
		t.Fatal(err)
	}
	beforeBal := b2b1Balance(t, store, "acct-b2b1-conf")
	// Conflicting amount.
	confAmt := res
	confAmt.CustomerCharge.Nano = 26
	confAmt.Fingerprint = "different"
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: confAmt}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conf amount err = %v, want OperationConflict", err)
	}
	// Conflicting owner (V2 for V1 pin). Use valid V2 valuation so the fence
	// proves pin ownership, not merely missing valuation.
	v2res := res
	v2res.CustomerValuation = f3BoundComponentValuation(t, call, res.CustomerCharge)
	v2res.Fingerprint = v2res.CustomerValuation.Fingerprint()
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: v2res, PostingOwner: billing.PostingOwnerV2}); !isFenceErr(err) {
		t.Fatalf("conf owner err = %v, want fence/conflict", err)
	}
	// Conflicting source (different call with same exposure must mismatch).
	otherCall := call
	otherCall.CallID = b2b1MustCallID(t)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: otherCall, Exposure: exp, Result: res}); err == nil {
		t.Fatalf("conf source must fail")
	}
	if got := b2b1Balance(t, store, "acct-b2b1-conf"); got != beforeBal {
		t.Fatalf("conflicts mutated balance")
	}
	_ = time.Now
}
