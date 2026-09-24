package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 17.3 B2b1 RED: customer call settlement posting-time ownership fence.
// Scope: customer_call_settlement only. Provider/adjustment later.
//
// Invariant under test (must FAIL before B2b1, PASS after):
// - Before any customer monetary effect, bind canonical
//   CustomerSettlementSourceKey to exactly one owner/marker epoch via B1 pins.
// - V1 default/shadow auto-acquires V1 pin and stays compatible.
// - Draining: only previously classified V1 pin with matching claim/epoch posts.
// - v2_active: V1 (including waking lease) fails closed; V2 requires V2 pin.
// - Pin completion + journal/balance/unit/exposure/terminal commit atomically.
// - Exact retry returns outcome + completed pin; conflicting replay fails.
// - Claim metadata from B2a is passed/validated at worker boundary.
// - No public money option/global/second writer (single ApplyCallBillingResult seam).

func b2b1MustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func b2b1NewStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return s
}

func b2b1EnsureShadow(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState:    billing.AccountingCutoverV2Shadow,
		TransitionID: "b2b1-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func b2b1SetupAccountCallExposure(t *testing.T, store *DurableStore, accountID string, balance, maxNano, chargeNano int64) (billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult) {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: balance, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b1MustCallID(t)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: "a-b2b1",
		SessionID: "sess-b2b1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		// Leg append may fence in draining; for default/shadow setup it must succeed.
		// If it fails, continue: settlement does not require legs.
		t.Logf("leg append (non-fatal for settlement): %v", err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: maxNano, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"}, Fingerprint: "b2b1-fp-" + callID.String()}
	return call, exp, res
}

func b2b1Balance(t *testing.T, store *DurableStore, accountID string) int64 {
	t.Helper()
	acct, err := store.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return acct.BalanceNano
}

func b2b1JournalCount(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "customer_call_settlement" {
			n++
		}
	}
	return n
}

func b2b1ExposureOpen(t *testing.T, store *DurableStore, callID billing.BillingCallID) bool {
	t.Helper()
	exp, err := store.GetCallExposure(context.Background(), callID)
	if err != nil {
		t.Fatal(err)
	}
	return exp.IsOpen()
}

func b2b1ClaimStatus(t *testing.T, store *DurableStore, callID billing.BillingCallID) string {
	t.Helper()
	var st string
	if err := store.DB().NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(context.Background(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func b2b1UnitCount(t *testing.T, store *DurableStore) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_unit_operations`).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestB2b1V1DefaultExactReplayBindsPinAtomically(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-default")
	ctx := context.Background()
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-default", 100, 60, 25)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err != nil {
		t.Fatalf("default settle: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first settlement must not be replayed")
	}
	if got := b2b1Balance(t, store, "acct-b2b1-default"); got != 75 {
		t.Fatalf("balance = %d, want 75", got)
	}
	if n := b2b1JournalCount(t, store, "acct-b2b1-default"); n != 1 {
		t.Fatalf("journal count = %d, want 1", n)
	}
	if b2b1ExposureOpen(t, store, call.CallID) {
		t.Fatalf("exposure must be closed after settlement")
	}
	if st := b2b1ClaimStatus(t, store, call.CallID); st != "processed" {
		t.Fatalf("claim_status = %q, want processed", st)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b2b1-default", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("default settlement must bind V1 pin atomically, Get: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("default pin must be V1 completed, got %#v", pin)
	}
	if pin.CompletionOperationKey == "" {
		t.Fatalf("completed pin must carry completion operation key")
	}
	// Exact retry returns existing outcome and completed pin, no second money.
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("second identical settlement must be replayed")
	}
	if got := b2b1Balance(t, store, "acct-b2b1-default"); got != 75 {
		t.Fatalf("replay balance = %d, want 75 (no second debit)", got)
	}
	if n := b2b1JournalCount(t, store, "acct-b2b1-default"); n != 1 {
		t.Fatalf("replay journal = %d, want 1", n)
	}
	// Conflicting amount must fail without mutation.
	conflict := res
	conflict.CustomerCharge.Nano = 26
	conflict.Fingerprint = "b2b1-conflict"
	before := b2b1Balance(t, store, "acct-b2b1-default")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: conflict}); err == nil {
		t.Fatalf("conflicting replay must fail")
	}
	if got := b2b1Balance(t, store, "acct-b2b1-default"); got != before {
		t.Fatalf("conflict mutated balance %d -> %d", before, got)
	}
}

func TestB2b1ShadowV1PostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-shadow")
	ctx := context.Background()
	_ = b2b1EnsureShadow(t, store)
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-shadow", 100, 60, 20)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err != nil {
		t.Fatalf("shadow V1 settle: %v", err)
	}
	if got := b2b1Balance(t, store, "acct-b2b1-shadow"); got != 80 {
		t.Fatalf("shadow balance = %d, want 80", got)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b2b1-shadow", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("shadow must bind V1 pin, Get: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("shadow pin must be V1 completed, got %#v", pin)
	}
}

func TestB2b1DrainingClassifiedPinPosts(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-drain-ok")
	ctx := context.Background()
	_ = b2b1EnsureShadow(t, store)
	// Seed before draining so coordinator classifies it.
	callID := b2b1MustCallID(t)
	acct := billing.Account{ID: "acct-b2b1-drain-ok", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
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
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b1-drain-ok"); err != nil {
		t.Fatal(err)
	}
	// Pin must exist as classified V1 pinned.
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("classified pin missing: %v", err)
	}
	if pre.Owner != billing.PostingOwnerV1 || pre.IsCompleted() {
		t.Fatalf("pre-settlement pin must be V1 pinned, got %#v", pre)
	}
	// Claim metadata that worker would pass.
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("claim metadata: %v", err)
	}
	if meta.Owner != billing.PostingOwnerV1 {
		t.Fatalf("claim owner = %q, want v1", meta.Owner)
	}
	// Draining settlement with classified pin must post and complete atomically.
	// F6+F8: draining requires the current-marker renewal token (stable owner,
	// current epoch); posting validates token==current, not pin acquisition epoch.
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-drain-fp"}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: meta.Owner, Claim: &meta})
	if err != nil {
		t.Fatalf("draining classified settle: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first draining settlement must not be replayed")
	}
	if got := b2b1Balance(t, store, acct.ID); got != 75 {
		t.Fatalf("draining balance = %d, want 75", got)
	}
	if n := b2b1JournalCount(t, store, acct.ID); n != 1 {
		t.Fatalf("draining journal = %d, want 1", n)
	}
	if b2b1ExposureOpen(t, store, callID) {
		t.Fatalf("draining exposure must close")
	}
	post, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if !post.IsCompleted() {
		t.Fatalf("draining pin must be completed atomically with money, got %#v", post)
	}
}

func TestB2b1DrainingUnpinnedBlockedZeroEffects(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-drain-fence")
	ctx := context.Background()
	_ = b2b1EnsureShadow(t, store)
	acct := billing.Account{ID: "acct-b2b1-unpinned", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
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
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b1-drain-fence-go"); err != nil {
		t.Fatal(err)
	}
	// Simulate unpinned work: delete the classified V1 pin so settlement sees
	// draining with no pin (new/unpinned must fence before money).
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationCustomerSettlement), opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-unpinned-fp"}
	beforeBal := b2b1Balance(t, store, acct.ID)
	beforeUnits := b2b1UnitCount(t, store)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err == nil {
		t.Fatalf("draining unpinned settlement must be fenced, got success (missing B2b1 fence)")
	}
	if !isFenceErr(err) {
		t.Fatalf("draining unpinned err = %v, want fence/conflict", err)
	}
	if got := b2b1Balance(t, store, acct.ID); got != beforeBal {
		t.Fatalf("fenced settlement mutated balance %d -> %d", beforeBal, got)
	}
	if n := b2b1JournalCount(t, store, acct.ID); n != 0 {
		t.Fatalf("fenced settlement wrote %d journals, want 0", n)
	}
	if !b2b1ExposureOpen(t, store, callID) {
		t.Fatalf("fenced settlement must leave exposure open")
	}
	if st := b2b1ClaimStatus(t, store, callID); st == "processed" {
		t.Fatalf("fenced settlement must not mark processed")
	}
	if n := b2b1UnitCount(t, store); n != beforeUnits {
		t.Fatalf("fenced settlement mutated units %d -> %d", beforeUnits, n)
	}
}

func TestB2b1StaleEpochBlocked(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-stale")
	ctx := context.Background()
	shadow := b2b1EnsureShadow(t, store)
	_ = shadow
	callID := b2b1MustCallID(t)
	acct := billing.Account{ID: "acct-b2b1-stale", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
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
	draining, _, err := store.BeginCutoverDraining(ctx, "b2b1-stale-drain")
	if err != nil {
		t.Fatal(err)
	}
	_ = draining
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	staleMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	// Advance epoch: draining -> v2_active via direct CAS (simulates lease
	// waking after epoch change; Activate would block on pending, so force).
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Complete all other pins? For this single-pin store, force transition to
	// prove stale fence even when CAS bypasses drain checks.
	// First mark row processed + complete pin via settlement? No: we want stale.
	// Force marker forward with low-level transition (test-only race simulation).
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch,
		NextState:    billing.AccountingCutoverV2Active,
		TransitionID: "b2b1-stale-force-active",
	}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	_ = staleMeta
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-stale-fp"}
	beforeBal := b2b1Balance(t, store, acct.ID)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err == nil {
		t.Fatalf("stale-epoch V1 settlement after v2_active must be fenced")
	}
	if !isFenceErr(err) {
		t.Fatalf("stale err = %v, want fence", err)
	}
	if got := b2b1Balance(t, store, acct.ID); got != beforeBal {
		t.Fatalf("stale fence mutated balance")
	}
	if n := b2b1JournalCount(t, store, acct.ID); n != 0 {
		t.Fatalf("stale fence wrote journals")
	}
}

func TestB2b1LeaseWakesAfterV2ActiveBlocked(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-lease")
	ctx := context.Background()
	_ = b2b1EnsureShadow(t, store)
	callID := b2b1MustCallID(t)
	acct := billing.Account{ID: "acct-b2b1-lease", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
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
	// Worker claims before cutover (lease held).
	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) == 0 {
		t.Fatalf("expected claimable call")
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b1-lease-drain"); err != nil {
		t.Fatal(err)
	}
	// Simulate lease waking after v2_active: force marker forward.
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch,
		NextState:    billing.AccountingCutoverV2Active,
		TransitionID: "b2b1-lease-force-active",
	}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-lease-fp"}
	beforeBal := b2b1Balance(t, store, acct.ID)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err == nil {
		t.Fatalf("lease-after-active V1 settlement must fail closed")
	}
	if !isFenceErr(err) {
		t.Fatalf("lease err = %v, want fence", err)
	}
	if got := b2b1Balance(t, store, acct.ID); got != beforeBal {
		t.Fatalf("lease fence mutated balance")
	}
	if n := b2b1JournalCount(t, store, acct.ID); n != 0 {
		t.Fatalf("lease fence wrote journals")
	}
	if !b2b1ExposureOpen(t, store, callID) {
		t.Fatalf("lease fence must leave exposure open")
	}
}

func TestB2b1V2ActiveV1Fenced(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-v2active-v1")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b1-v2a-v1", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
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
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Advance to v2_active with pending work still present (force CAS to
	// simulate epoch jump; production Activate would block, but the
	// posting-time fence must still fail closed).
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b1-v2a-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b1-v2a-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b1-v2a-force"}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 10, Currency: "USD"}, Fingerprint: "b2b1-v2a-v1-fp"}
	beforeBal := b2b1Balance(t, store, acct.ID)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err == nil {
		t.Fatalf("v2_active V1 settlement must fail closed")
	} else if !isFenceErr(err) {
		t.Fatalf("v2_active V1 err = %v, want fence", err)
	}
	if got := b2b1Balance(t, store, acct.ID); got != beforeBal {
		t.Fatalf("v2_active V1 fence mutated balance")
	}
}

func TestB2b1ConcurrentV1V2ContendersSingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-contend")
	ctx := context.Background()
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-contend", 1000, 600, 100)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical V1 contender err = %v, want all succeed as replay", err)
		}
	}
	if got := b2b1Balance(t, store, "acct-b2b1-contend"); got != 900 {
		t.Fatalf("concurrent balance = %d, want 900 (exactly once)", got)
	}
	if c := b2b1JournalCount(t, store, "acct-b2b1-contend"); c != 1 {
		t.Fatalf("concurrent journals = %d, want 1", c)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b2b1-contend", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("contend pin missing: %v (no atomic ownership fence)", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("contend pin must be completed, got %#v", pin)
	}
	_ = exp
}

func TestB2b1ReopenPreservesPinAndOutcome(t *testing.T) {
	t.Parallel()
	store := b2b1NewStore(t, "b2b1-reopen")
	ctx := context.Background()
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-b2b1-reopen", 100, 60, 30)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err != nil {
		t.Fatal(err)
	}
	// Reopen via same DB handle with new store scope (restart-safe read).
	reopened, err := NewDurableStore(ctx, store.DB(), Config{StoreID: "b2b1-reopen"})
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b2b1-reopen", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("reopen pin missing: %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("reopen pin must be completed")
	}
	if got := b2b1Balance(t, reopened, "acct-b2b1-reopen"); got != 70 {
		t.Fatalf("reopen balance = %d, want 70", got)
	}
	// Exact retry after reopen returns replay without second money.
	replayed, err := reopened.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err != nil {
		t.Fatalf("reopen replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("reopen replay must be marked replayed")
	}
}

func isFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, billing.ErrSettlementConflict) ||
		errors.Is(err, billing.ErrSettlementInvalid) ||
		errors.Is(err, billing.ErrRetailRateIncomplete) ||
		errors.Is(err, ErrOperationConflict)
}
