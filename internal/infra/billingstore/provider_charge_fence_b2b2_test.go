package billingstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 B2b2 RED: provider charge/payable posting-time ownership fence.
// Scope: provider_charge only (legacy ApplyProviderCost + revision).
//
// Invariant under test (must FAIL before B2b2, PASS after):
// - Every provider payable/cost/exclusion uses canonical ProviderCostSourceKey
//   and one owner/epoch pin.
// - V1 default/shadow auto-pins V1 and stays compatible.
// - Draining: only classified V1 pin + matching claim metadata posts.
// - v2_active: V1 (including waking lease) fails closed; V2 requires V2 pin.
// - Pin completion + journal/head/exclusion commit atomically in same tx.
// - Exact nonpayable exclusions also complete the pin; replay stable.
// - Conflicting owner/epoch/source/fingerprint/amount fail closed.

func b2b2MustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func b2b2NewStore(t *testing.T, storeID string) *DurableStore {
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

func b2b2EnsureShadow(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState:    billing.AccountingCutoverV2Shadow,
		TransitionID: "b2b2-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func b2b2SetupLeg(t *testing.T, store *DurableStore, accountID, bLegID string) (billing.BillingCallID, billing.CallLegUsageRecord, billing.OperatorCostResult) {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{bLegID})
	call.AccountID = accountID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, bLegID)
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	return callID, leg, result
}

func b2b2ProviderJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	return n
}

func b2b2ProviderPin(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord) billing.PostingPin {
	t.Helper()
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(context.Background(), billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("provider pin missing for %q: %v (no atomic ownership fence)", opKey, err)
	}
	return pin
}

func b2b2IsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, ErrOperationConflict)
}

func TestB2b2V1DefaultPayableBindsPinAtomically(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-default")
	ctx := context.Background()
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-default", "b-1")
	posting, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-default", CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatalf("default provider post: %v", err)
	}
	if posting.Replayed {
		t.Fatalf("first provider posting must not be replayed")
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-default"); n != 1 {
		t.Fatalf("provider journals = %d, want 1", n)
	}
	pin := b2b2ProviderPin(t, store, "acct-b2b2-default", callID, leg)
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("default pin must be V1 completed, got %#v", pin)
	}
	if pin.CompletionOperationKey == "" {
		t.Fatalf("completed pin must carry completion operation key")
	}
	replayed, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-default", CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("second identical provider posting must be replayed")
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-default"); n != 1 {
		t.Fatalf("replay journals = %d, want 1", n)
	}
	conflict := result
	conflict.Amount.Nano++
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-default", CallID: callID, Leg: leg, Result: conflict}); err == nil {
		t.Fatalf("conflicting replay must fail")
	}
}

func TestB2b2ShadowV1PostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-shadow")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-shadow", "b-1")
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-shadow", CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatalf("shadow V1 provider post: %v", err)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-shadow"); n != 1 {
		t.Fatalf("shadow journals = %d, want 1", n)
	}
	pin := b2b2ProviderPin(t, store, "acct-b2b2-shadow", callID, leg)
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("shadow pin must be V1 completed, got %#v", pin)
	}
}

func TestB2b2DrainingClassifiedPinWithClaimPosts(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-drain-ok")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-drain-ok", "b-1")
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-drain-ok"); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("classified pin missing: %v", err)
	}
	if pre.Owner != billing.PostingOwnerV1 || pre.IsCompleted() {
		t.Fatalf("pre-posting pin must be V1 pinned, got %#v", pre)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("claim metadata: %v", err)
	}
	posted, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-drain-ok", CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV1, Claim: &meta})
	if err != nil {
		t.Fatalf("draining classified provider post: %v", err)
	}
	if posted.Replayed {
		t.Fatalf("first draining posting must not be replayed")
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-drain-ok"); n != 1 {
		t.Fatalf("draining journals = %d, want 1", n)
	}
	post, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if !post.IsCompleted() {
		t.Fatalf("draining pin must be completed atomically with money, got %#v", post)
	}
}

func TestB2b2DrainingUnpinnedBlockedZeroEffects(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-drain-fence")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-unpinned", "b-1")
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-drain-fence-go"); err != nil {
		t.Fatal(err)
	}
	sealed, _ := leg.Seal()
	opKey, _ := billing.ProviderCostSourceKey(sealed.Key)
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	before := b2b2ProviderJournals(t, store, "acct-b2b2-unpinned")
	_, applyErr := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-unpinned", CallID: callID, Leg: leg, Result: result})
	if applyErr == nil {
		t.Fatalf("draining unpinned provider posting must be fenced, got success (missing B2b2 fence)")
	}
	if !b2b2IsFenceErr(applyErr) {
		t.Fatalf("draining unpinned err = %v, want fence/conflict", applyErr)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-unpinned"); n != before {
		t.Fatalf("fenced posting wrote journals %d -> %d, want 0 new", before, n)
	}
	var workStatus string
	if err := store.DB().NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &workStatus); err != nil {
		t.Fatal(err)
	}
	if workStatus == "processed" {
		t.Fatalf("fenced posting must not mark work processed")
	}
}

func TestB2b2DrainingWithoutClaimMustReject(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-drain-noclaim")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-noclaim", "b-1")
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-drain-noclaim-go"); err != nil {
		t.Fatal(err)
	}
	// Production wrapper without metadata must reject: classified pin exists
	// but no claim is passed, so posting must fence (no optional bypass).
	_, noClaimErr := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-noclaim", CallID: callID, Leg: leg, Result: result})
	if noClaimErr == nil {
		t.Fatalf("draining provider posting without claim metadata must reject (missing B2b2 mandatory claim rule)")
	}
	if !b2b2IsFenceErr(noClaimErr) {
		t.Fatalf("no-claim err = %v, want fence", noClaimErr)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-noclaim"); n != 0 {
		t.Fatalf("no-claim fence wrote %d journals, want 0", n)
	}
}

func TestB2b2StaleEpochBlocked(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-stale")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-stale", "b-1")
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-stale-drain"); err != nil {
		t.Fatal(err)
	}
	sealed, _ := leg.Seal()
	opKey, _ := billing.ProviderCostSourceKey(sealed.Key)
	staleMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch,
		NextState:    billing.AccountingCutoverV2Active,
		TransitionID: "b2b2-stale-force-active",
	}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	before := b2b2ProviderJournals(t, store, "acct-b2b2-stale")
	_, err = store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-stale", CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV1, Claim: &staleMeta})
	if err == nil {
		t.Fatalf("stale-epoch V1 provider posting after v2_active must be fenced")
	}
	if !b2b2IsFenceErr(err) {
		t.Fatalf("stale err = %v, want fence", err)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-stale"); n != before {
		t.Fatalf("stale fence wrote journals")
	}
}

func TestB2b2LeaseWakesAfterV2ActiveBlocked(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-lease")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-lease", "b-1")
	claimed, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) == 0 {
		t.Fatalf("expected claimable provider work")
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-lease-drain"); err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch,
		NextState:    billing.AccountingCutoverV2Active,
		TransitionID: "b2b2-lease-force-active",
	}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	before := b2b2ProviderJournals(t, store, "acct-b2b2-lease")
	_, err = store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-lease", CallID: callID, Leg: leg, Result: result})
	if err == nil {
		t.Fatalf("lease-after-active V1 provider posting must fail closed")
	}
	if !b2b2IsFenceErr(err) {
		t.Fatalf("lease err = %v, want fence", err)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-lease"); n != before {
		t.Fatalf("lease fence wrote journals")
	}
}

func TestB2b2V2ActiveV1Fenced(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-v2active-v1")
	ctx := context.Background()
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-v2a-v1", "b-1")
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b2-v2a-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b2-v2a-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b2-v2a-force"}); err != nil {
		t.Fatalf("force active: %v", err)
	}
	before := b2b2ProviderJournals(t, store, "acct-b2b2-v2a-v1")
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-v2a-v1", CallID: callID, Leg: leg, Result: result}); err == nil {
		t.Fatalf("v2_active V1 provider posting must fail closed")
	} else if !b2b2IsFenceErr(err) {
		t.Fatalf("v2_active V1 err = %v, want fence", err)
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-v2a-v1"); n != before {
		t.Fatalf("v2_active V1 fence wrote journals")
	}
}

func TestB2b2ConcurrentV1V2ContendersSingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-contend")
	ctx := context.Background()
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-contend", "b-1")
	_ = ctx
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = store.ApplyProviderCost(context.Background(), billing.ApplyProviderCostInput{AccountID: "acct-b2b2-contend", CallID: callID, Leg: leg, Result: result})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical V1 contender err = %v, want all succeed as replay", err)
		}
	}
	if c := b2b2ProviderJournals(t, store, "acct-b2b2-contend"); c != 1 {
		t.Fatalf("concurrent journals = %d, want 1", c)
	}
	pin := b2b2ProviderPin(t, store, "acct-b2b2-contend", callID, leg)
	if !pin.IsCompleted() {
		t.Fatalf("contend pin must be completed, got %#v", pin)
	}
}

func TestB2b2ConcurrentV1VsV2SingleWinner(t *testing.T) {
	t.Parallel()
	v1store := b2b2NewStore(t, "b2b2-contend-v1wins")
	callID, leg, result := b2b2SetupLeg(t, v1store, "acct-b2b2-v1wins", "b-1")
	var wg sync.WaitGroup
	var v1Err, v2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, v1Err = v1store.ApplyProviderCost(context.Background(), billing.ApplyProviderCostInput{AccountID: "acct-b2b2-v1wins", CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV1})
	}()
	go func() {
		defer wg.Done()
		_, v2Err = v1store.ApplyProviderCost(context.Background(), billing.ApplyProviderCostInput{AccountID: "acct-b2b2-v1wins", CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV2})
	}()
	wg.Wait()
	if v1Err != nil {
		t.Fatalf("v1_active V1 contender must win: %v", v1Err)
	}
	if !b2b2IsFenceErr(v2Err) {
		t.Fatalf("v1_active V2 contender err = %v, want fence", v2Err)
	}
	if c := b2b2ProviderJournals(t, v1store, "acct-b2b2-v1wins"); c != 1 {
		t.Fatalf("V1-wins journals = %d, want 1", c)
	}
}

func TestB2b2ReopenPreservesPinAndOutcome(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-reopen")
	ctx := context.Background()
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-reopen", "b-1")
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-reopen", CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableStore(ctx, store.DB(), Config{StoreID: "b2b2-reopen"})
	if err != nil {
		t.Fatal(err)
	}
	pin := b2b2ProviderPin(t, reopened, "acct-b2b2-reopen", callID, leg)
	if !pin.IsCompleted() {
		t.Fatalf("reopen pin must be completed")
	}
	if c := b2b2ProviderJournals(t, reopened, "acct-b2b2-reopen"); c != 1 {
		t.Fatalf("reopen journals = %d, want 1", c)
	}
	replayed, err := reopened.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-reopen", CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatalf("reopen replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("reopen replay must be marked replayed")
	}
}

func TestB2b2ConflictingOwnerEpochAmountSourceFail(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-conflicts")
	ctx := context.Background()
	callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-conf", "b-1")
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-conf", CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	before := b2b2ProviderJournals(t, store, "acct-b2b2-conf")
	confAmt := result
	confAmt.Amount.Nano++
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-conf", CallID: callID, Leg: leg, Result: confAmt}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conf amount err = %v, want OperationConflict", err)
	}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-conf", CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV2}); !b2b2IsFenceErr(err) {
		t.Fatalf("conf owner must fence/conflict")
	}
	otherLeg := testIndependentCallLegFor(callID, "b-other")
	otherSealed, _ := otherLeg.Seal()
	otherResult := result
	otherResult.LURKey = otherSealed.Key
	// A different B-leg is a different lineage (new work), not a conflict: in
	// v1_active it auto-pins V1 and posts. Conflicting source is the same
	// sealed leg posted under a different account (pin identity mismatch).
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-other", CallID: callID, Leg: leg, Result: result}); err == nil {
		t.Fatalf("conf source (account mismatch) must fail")
	}
	if n := b2b2ProviderJournals(t, store, "acct-b2b2-conf"); n != before {
		t.Fatalf("conflicts mutated journals")
	}
}

func TestB2b2CrashAtBoundariesIsAtomic(t *testing.T) {
	t.Parallel()
	points := []string{"b2b2-pin-acquire", "b2b2-before-effects", "b2b2-before-journal", "b2b2-before-pin-complete", "b2b2-before-commit"}
	for _, point := range points {
		func(pt string) {
			store := b2b2NewStore(t, "b2b2-crash-"+pt)
			ctx := context.Background()
			callID, leg, result := b2b2SetupLeg(t, store, "acct-b2b2-crash-"+pt, "b-1")
			// F2A: leg append now acquires the V1 provider pin atomically.
			// The pin-acquire fault proves atomic acquisition when no pin
			// exists; clear it for that point to force the acquisition path.
			if pt == "b2b2-pin-acquire" {
				sealed, _ := leg.Seal()
				opKey, _ := billing.ProviderCostSourceKey(sealed.Key)
				if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
					store.StoreID(), string(billing.PostingOperationProviderCharge), opKey).Exec(ctx); err != nil {
					t.Fatalf("point %s clear pin: %v", pt, err)
				}
			}
			failed := false
			store.providerFaultHook = func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b2 injected crash at %s", p)
				}
				return nil
			}
			_, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-crash-" + pt, CallID: callID, Leg: leg, Result: result})
			if err == nil {
				t.Fatalf("point %s must fail", pt)
			}
			if n := b2b2ProviderJournals(t, store, "acct-b2b2-crash-"+pt); n != 0 {
				t.Fatalf("point %s wrote %d journals", pt, n)
			}
			sealed, _ := leg.Seal()
			opKey, _ := billing.ProviderCostSourceKey(sealed.Key)
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("point %s Get pin: %v", pt, perr)
			}
			var workStatus string
			if err := store.DB().NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &workStatus); err != nil {
				t.Fatal(err)
			}
			if workStatus == "processed" {
				t.Fatalf("point %s must not mark work processed", pt)
			}
			store.providerFaultHook = nil
			posted, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-b2b2-crash-" + pt, CallID: callID, Leg: leg, Result: result})
			if err != nil {
				t.Fatalf("point %s retry: %v", pt, err)
			}
			if posted.Replayed {
				t.Fatalf("point %s retry must not be replayed (first never committed)", pt)
			}
			if n := b2b2ProviderJournals(t, store, "acct-b2b2-crash-"+pt); n != 1 {
				t.Fatalf("point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
}

// Revision-level RED: nonpayable exclusion completes pin; replacement retains
// one authority; V2 active V2 posts; crash around head/journal/complete.

func b2b2RevisionInput(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-b2b2", BillingCallID: callID.String(), BLegID: "b-rev",
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "b2b2-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("b2b2-provider-charge-%d", revision),
			SourceEventKey: fmt.Sprintf("b2b2-provider-charge-%d", revision), Revision: revision,
			StreamID: "b2b2-provider-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(),
			ReceivedAt: time.Unix(100, 0).UTC(), MappingRef: "b2b2.provider.cost",
			Charges: []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "b2b2-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{"b-rev"},
	}
	if !payable {
		cost.IncludedLegKeys = nil
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x", revision),
		ValuationID: fmt.Sprintf("b2b2-provider-valuation-%d", revision), Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

func TestB2b2RevisionDefaultPayableBindsPin(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-rev-default")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b2-revdef", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	// Subject must carry this call.
	input := b2b2RevisionInput(t, store, acct.ID, callID, "b-rev-head", 1, 20, true)
	posted, err := store.ApplyProviderCostRevision(ctx, input)
	if err != nil {
		t.Fatalf("default revision post: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("first revision must be applied")
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("revision journals = %d, want 1", n)
	}
	// F5+F7: each revision is its own immutable pin (revision-specific).
	opKey, err := billing.ProviderRevisionPostingOperationKey(input)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("revision pin missing: %v (no atomic ownership fence)", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("revision pin must be V1 completed, got %#v", pin)
	}
	// Replacement revision is a distinct pin while head/fence orders lineage.
	replacement := b2b2RevisionInput(t, store, acct.ID, callID, "b-rev-head", 2, 15, true)
	replaced, err := store.ApplyProviderCostRevision(ctx, replacement)
	if err != nil {
		t.Fatalf("replacement revision: %v", err)
	}
	if !replaced.Applied {
		t.Fatalf("replacement must be applied")
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 2 {
		t.Fatalf("replacement journals = %d, want 2", n)
	}
	opKey2, err := billing.ProviderRevisionPostingOperationKey(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if opKey2 == opKey {
		t.Fatalf("replacement must be distinct pin, got same %q", opKey)
	}
	pin2, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey2)
	if err != nil {
		t.Fatal(err)
	}
	if pin2.Owner != billing.PostingOwnerV1 || !pin2.IsCompleted() {
		t.Fatalf("replacement must be V1 completed, got %#v", pin2)
	}
	// Immutable history: base pin still completed with original outcome.
	basePin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if !basePin.IsCompleted() || basePin.CompletionOperationKey != opKey {
		t.Fatalf("base pin must stay completed with original outcome, got %#v", basePin)
	}
	if basePin.CompletionOperationKey == pin2.CompletionOperationKey && basePin.CompletionTransactionID == pin2.CompletionTransactionID {
		// Same lineage different revisions must not share outcome identity
		// unless zero-delta adoption reuses legacy tx (not here: delta -5).
		t.Fatalf("distinct revisions must have distinct outcomes")
	}
	// Same-revision exact replay stable.
	replay, err := store.ApplyProviderCostRevision(ctx, replacement)
	if err != nil {
		t.Fatalf("replacement replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("same-revision replay must be marked replayed")
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 2 {
		t.Fatalf("replay journals = %d, want 2 (no new money)", n)
	}
}

func TestB2b2RevisionNonpayableExclusionCompletesPin(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-rev-excl")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b2-excl", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	input := b2b2RevisionInput(t, store, acct.ID, callID, "b-rev-excl-head", 1, 0, false)
	excluded, err := store.ApplyProviderCostRevision(ctx, input)
	if err != nil {
		t.Fatalf("exclusion: %v", err)
	}
	if !excluded.Ignored {
		t.Fatalf("nonpayable must be ignored exclusion, got %#v", excluded)
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 0 {
		t.Fatalf("exclusion journals = %d, want 0", n)
	}
	opKey, err := billing.ProviderRevisionPostingOperationKey(input)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("exclusion pin missing: %v (exclusions must complete pin)", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("exclusion pin must be completed, got %#v", pin)
	}
	replay, err := store.ApplyProviderCostRevision(ctx, input)
	if err != nil {
		t.Fatalf("exclusion replay: %v", err)
	}
	if !replay.Ignored {
		t.Fatalf("exclusion replay must stay ignored, got %#v", replay)
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 0 {
		t.Fatalf("exclusion replay journals = %d, want 0", n)
	}
}

func TestB2b2RevisionDrainingRequiresClaim(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-rev-drain")
	ctx := context.Background()
	_ = b2b2EnsureShadow(t, store)
	acct := billing.Account{ID: "acct-b2b2-revdrain", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	// Seed provider work via leg so coordinator classifies the B-leg lineage pin.
	// A call record provides the account lineage the classifier requires.
	// F5+F7: revisions use distinct revision-specific pins; leg classification
	// does not classify revisions. Revision classification below simulates
	// economic-work drain classification for this direct revision.
	callRec := testIndependentCallUsageFor(callID, []string{"b-rev"})
	callRec.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, callRec); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-rev")
	leg.ALegID = "a-b2b2"
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b2-rev-drain-go"); err != nil {
		t.Fatal(err)
	}
	input := b2b2RevisionInput(t, store, acct.ID, callID, "b-rev-drain-head", 1, 20, true)
	// Without claim must reject even though a classified B-leg lineage pin exists.
	// Revision pin is distinct and unclassified, so draining fences.
	if _, err := store.ApplyProviderCostRevision(ctx, input); err == nil {
		t.Fatalf("draining revision without claim must reject (missing B2b2 mandatory claim rule)")
	} else if !b2b2IsFenceErr(err) {
		t.Fatalf("draining no-claim err = %v, want fence", err)
	}
	// Classify the revision-specific pin (economic-work drain equivalent), then
	// matching claim must post.
	opKey, err := billing.ProviderRevisionPostingOperationKey(input)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.coordinatorInsertProviderRevisionPin(ctx, marker, acct.ID, callID, input.Subject, opKey); err != nil {
		t.Fatalf("classify revision pin: %v", err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("claim metadata: %v", err)
	}
	withClaim := input
	withClaim.PostingOwner = billing.PostingOwnerV1
	withClaim.Claim = &meta
	posted, err := store.ApplyProviderCostRevision(ctx, withClaim)
	if err != nil {
		t.Fatalf("draining revision with claim: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("draining revision with claim must apply")
	}
	_ = time.Now
}

func TestB2b2V2ActiveV2LegacyPostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-v2-legacy")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b2-v2leg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 13, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b2-v2leg-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b2-v2leg-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	// F2A: leg append now acquires the V1 provider pin atomically. Genuine V2
	// new work after activation carries no prior V1 pin; clear it to simulate
	// V2-new ownership (same as B2b1 V2 positive).
	v2opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), v2opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b2-v2leg-active"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckV2NewWorkAuthorized(ctx); err != nil {
		t.Fatalf("V2 must be authorized in active: %v", err)
	}
	posted, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV2})
	if err != nil {
		t.Fatalf("v2_active V2 legacy post: %v", err)
	}
	if posted.Replayed {
		t.Fatalf("first V2 posting must not be replayed")
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("V2 journals = %d, want 1", n)
	}
	opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("V2 pin missing: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() {
		t.Fatalf("V2 pin must be completed, got %#v", pin)
	}
	if pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("V2 pin marker = %q, want v2_active", pin.MarkerState)
	}
	replayed, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: result, PostingOwner: billing.PostingOwnerV2})
	if err != nil {
		t.Fatalf("V2 replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("V2 second must be replayed")
	}
}

func TestB2b2V2ActiveV2RevisionPostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b2NewStore(t, "b2b2-v2-rev")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b2-v2rev", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := b2b2MustCallID(t)
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b2-v2rev-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b2-v2rev-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b2-v2rev-active"}); err != nil {
		t.Fatal(err)
	}
	input := b2b2RevisionInput(t, store, acct.ID, callID, "b-v2rev-head", 1, 22, true)
	input.PostingOwner = billing.PostingOwnerV2
	posted, err := store.ApplyProviderCostRevision(ctx, input)
	if err != nil {
		t.Fatalf("v2_active V2 revision post: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("V2 revision must be applied")
	}
	if n := b2b2ProviderJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("V2 revision journals = %d, want 1", n)
	}
	opKey, err := billing.ProviderRevisionPostingOperationKey(input)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("V2 revision pin missing: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() {
		t.Fatalf("V2 revision pin must be completed, got %#v", pin)
	}
}

func TestB2b2RevisionCrashAtBoundariesIsAtomic(t *testing.T) {
	t.Parallel()
	points := []string{"b2b2-enter", "b2b2-pin-acquire", "b2b2-before-effects", "b2b2-before-journal", "b2b2-before-pin-complete", "b2b2-before-commit", "b2b2-replay-pin"}
	for _, point := range points {
		func(pt string) {
			store := b2b2NewStore(t, "b2b2-revcrash-"+pt)
			ctx := context.Background()
			acct := billing.Account{ID: "acct-b2b2-revcrash-" + pt, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
			if err := store.CreateAccount(ctx, acct); err != nil {
				t.Fatal(err)
			}
			callID := b2b2MustCallID(t)
			input := b2b2RevisionInput(t, store, acct.ID, callID, "b-revcrash-head", 1, 20, true)
			failed := false
			store.providerFaultHook = func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b2 revision injected crash at %s", p)
				}
				return nil
			}
			_, err := store.ApplyProviderCostRevision(ctx, input)
			// b2b2-replay-pin only fires on replay paths; first posting never
			// hits it, so a crash there must NOT fail the first post. All
			// other points must fail the first post with zero effects.
			if pt == "b2b2-replay-pin" {
				if err != nil {
					t.Fatalf("point %s must not fail first posting (replay-only hook): %v", pt, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("point %s must fail", pt)
			}
			if n := b2b2ProviderJournals(t, store, acct.ID); n != 0 {
				t.Fatalf("point %s wrote %d journals, want 0", pt, n)
			}
			opKey, _ := billing.ProviderRevisionPostingOperationKey(input)
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("point %s Get pin: %v", pt, perr)
			}
			var headCount int
			if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_heads WHERE store_id = ? AND account_id = ?`, store.StoreID(), acct.ID).Scan(ctx, &headCount); err != nil {
				t.Fatal(err)
			}
			if headCount != 0 {
				t.Fatalf("point %s wrote %d heads, want 0", pt, headCount)
			}
			store.providerFaultHook = nil
			posted, err := store.ApplyProviderCostRevision(ctx, input)
			if err != nil {
				t.Fatalf("point %s retry: %v", pt, err)
			}
			if !posted.Applied {
				t.Fatalf("point %s retry must apply (first never committed)", pt)
			}
			if n := b2b2ProviderJournals(t, store, acct.ID); n != 1 {
				t.Fatalf("point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
}
