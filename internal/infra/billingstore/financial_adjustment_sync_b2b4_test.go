package billingstore

// Phase 17.3 B2b4 GREEN: atomic ownership pins for the remaining synchronous
// monetary adjustment writers (Task 17.3 one posting version per logical
// adjustment).
//
// Scope: financial_adjustment only.
//   - cost pass-through revisions: per-head pin
//     CostPassThroughFinancialAdjustmentPostingOperationKey(store, account,
//     call); one authority for the replacement chain.
//   - direct adjustments: per-source pin
//     DirectFinancialAdjustmentPostingOperationKey(store, account, source);
//     head_key carries source so the hash stays recomputable without fake
//     call/head/B-leg lineage. Selected-cost head identity unchanged.
//
// V1 active/shadow default V1; draining new blocked but exact completed V1
// replay allowed; v2_active V1 blocked/V2 allowed. Stale claim/epoch fenced.
// Pin + balance/journal/head commit atomically; exact replay
// backfills/completes same owner; conflict owner/epoch/amount/currency/source
// fails before effects. Funding/payment/policy and unit ledger are not
// cutover adjustment writers (provisioning/policy/evidence, no pin).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func b2b4IsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, ErrOperationConflict)
}

func b2b4CostPassJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == billing.CostPassThroughAdjustmentOperationKind {
			n++
		}
	}
	return n
}

func b2b4DirectJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "adjustment" {
			n++
		}
	}
	return n
}

func b2b4CostPassPin(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) billing.PostingPin {
	t.Helper()
	opKey, err := billing.CostPassThroughFinancialAdjustmentPostingOperationKey(store.StoreID(), accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(context.Background(), billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("cost pass-through pin missing for %q: %v", opKey, err)
	}
	return pin
}

func b2b4DirectPin(t *testing.T, store *DurableStore, accountID, sourceKey string) billing.PostingPin {
	t.Helper()
	opKey, err := billing.DirectFinancialAdjustmentPostingOperationKey(store.StoreID(), accountID, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(context.Background(), billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("direct pin missing for %q: %v", opKey, err)
	}
	return pin
}

func b2b4SetupCostPassThrough(t *testing.T, store *DurableStore, accountID string) (context.Context, billing.CallUsageRecord, billing.CostPassThroughProviderCost) {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	call, provider := b2b4SetupCostPassThroughHead(t, store, ctx, acct)
	return ctx, call, provider
}

func b2b4SetupDirectAccount(t *testing.T, store *DurableStore, accountID string) context.Context {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestB2b4CostPassThroughV1DefaultBindsPinReplayConflict(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-cost-default")
	ctx, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-cost-default")
	applied, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-cost-default", CallID: call.CallID, ProviderCost: provider})
	if err != nil {
		t.Fatalf("default post: %v", err)
	}
	if !applied.Applied {
		t.Fatalf("first must apply, got %+v", applied)
	}
	if n := b2b4CostPassJournals(t, store, "acct-b2b4-cost-default"); n != 1 {
		t.Fatalf("journals = %d, want 1", n)
	}
	sourceKey, _ := billing.CostPassThroughAdjustmentSourceKey("acct-b2b4-cost-default", call.CallID, provider)
	pin := b2b4CostPassPin(t, store, "acct-b2b4-cost-default", call.CallID)
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("default pin must be V1 completed, got %#v", pin)
	}
	if pin.CompletionOperationKey != sourceKey || pin.CompletionTransactionID != sourceKey {
		t.Fatalf("pin completion must match revision outcome: pin %#v source %q", pin, sourceKey)
	}
	wantHead := billing.CostPassThroughHeadKey("acct-b2b4-cost-default", call.CallID)
	if pin.HeadKey != wantHead {
		t.Fatalf("pin head %q must equal canonical %q (no fake lineage)", pin.HeadKey, wantHead)
	}
	if pin.Subject.Kind != "" {
		t.Fatalf("cost pass-through pin must carry no subject, got %#v", pin.Subject)
	}
	replayed, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-cost-default", CallID: call.CallID, ProviderCost: provider})
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("replay must be replayed, got %+v", replayed)
	}
	if n := b2b4CostPassJournals(t, store, "acct-b2b4-cost-default"); n != 1 {
		t.Fatalf("replay journals = %d, want 1", n)
	}
	// Conflicting amount under same revision fails before effects (within bound
	// so it reaches fingerprint conflict, not bound check).
	conflictCost := provider
	conflictCost.Amount = billing.Money{Nano: 90, Currency: "USD"}
	before := b2b4CostPassJournals(t, store, "acct-b2b4-cost-default")
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-cost-default", CallID: call.CallID, ProviderCost: conflictCost}); err == nil {
		t.Fatalf("conflicting amount must fail")
	} else if !b2b4IsFenceErr(err) && !errors.Is(err, billing.ErrCostPassThroughRevisionConflict) {
		t.Fatalf("conflict err = %v, want fence/conflict", err)
	}
	if n := b2b4CostPassJournals(t, store, "acct-b2b4-cost-default"); n != before {
		t.Fatalf("conflict mutated journals %d -> %d", before, n)
	}
	// Conflicting currency fails before effects.
	currencyCost := provider
	currencyCost.Amount = billing.Money{Nano: 10, Currency: "EUR"}
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-cost-default", CallID: call.CallID, ProviderCost: currencyCost}); err == nil {
		t.Fatalf("currency mismatch must fail")
	}
	if n := b2b4CostPassJournals(t, store, "acct-b2b4-cost-default"); n != before {
		t.Fatalf("currency conflict mutated journals")
	}
}

func TestB2b4DirectV1DefaultBindsPinReplayConflict(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-direct-default")
	ctx := b2b4SetupDirectAccount(t, store, "acct-b2b4-direct-default")
	adj := billing.AdjustmentInput{AccountID: "acct-b2b4-direct-default", Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-direct-1", Reason: "correction"}
	posted, err := store.PostAdjustment(ctx, adj)
	if err != nil {
		t.Fatalf("default post: %v", err)
	}
	if posted.Replayed {
		t.Fatalf("first must not be replayed")
	}
	if n := b2b4DirectJournals(t, store, "acct-b2b4-direct-default"); n != 1 {
		t.Fatalf("journals = %d, want 1", n)
	}
	pin := b2b4DirectPin(t, store, "acct-b2b4-direct-default", "b2b4-direct-1")
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("default pin must be V1 completed, got %#v", pin)
	}
	wantOp := billing.ScopedOperationKey("adjustment", "acct-b2b4-direct-default", "b2b4-direct-1")
	if pin.CompletionOperationKey != wantOp || pin.CompletionTransactionID != wantOp {
		t.Fatalf("pin completion must match adjustment outcome: pin %#v want %q", pin, wantOp)
	}
	if strings.TrimSpace(pin.CallID.String()) != "" {
		t.Fatalf("direct pin must carry no call lineage, got %q", pin.CallID.String())
	}
	if pin.HeadKey != "b2b4-direct-1" {
		t.Fatalf("direct pin head_key must carry source %q, got %q", "b2b4-direct-1", pin.HeadKey)
	}
	replayed, err := store.PostAdjustment(ctx, adj)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("second identical must be replayed")
	}
	if n := b2b4DirectJournals(t, store, "acct-b2b4-direct-default"); n != 1 {
		t.Fatalf("replay journals = %d, want 1", n)
	}
	// Conflicting amount with same source fails before effects.
	conflict := adj
	conflict.Amount = billing.Money{Nano: 999, Currency: "USD"}
	before := b2b4DirectJournals(t, store, "acct-b2b4-direct-default")
	if _, err := store.PostAdjustment(ctx, conflict); err == nil {
		t.Fatalf("conflicting amount must fail")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("conflict err = %v, want fence/conflict", err)
	}
	if n := b2b4DirectJournals(t, store, "acct-b2b4-direct-default"); n != before {
		t.Fatalf("conflict mutated journals")
	}
	// Conflicting owner fails before effects.
	ownerConflict := adj
	ownerConflict.PostingOwner = billing.PostingOwnerV2
	if _, err := store.PostAdjustment(ctx, ownerConflict); err == nil {
		t.Fatalf("conflicting owner must fail")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("owner conflict err = %v, want fence", err)
	}
	if n := b2b4DirectJournals(t, store, "acct-b2b4-direct-default"); n != before {
		t.Fatalf("owner conflict mutated journals")
	}
}

func TestB2b4DrainingNewBlockedReplayAllowed(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-drain")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	// Cost pass-through leg.
	costAcct := "acct-b2b4-drain-cost"
	costCtx, costCall, costProvider := b2b4SetupCostPassThrough(t, store, costAcct)
	if _, err := store.ApplyCostPassThroughRevision(costCtx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: costProvider}); err != nil {
		t.Fatal(err)
	}
	// Direct leg.
	directAcct := "acct-b2b4-drain-direct"
	b2b4SetupDirectAccount(t, store, directAcct)
	directAdj := billing.AdjustmentInput{AccountID: directAcct, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-drain-1", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, directAdj); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b4-drain-go"); err != nil {
		t.Fatal(err)
	}
	// Exact replays remain allowed in draining with zero new effects.
	if _, err := store.ApplyCostPassThroughRevision(costCtx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: costProvider}); err != nil {
		t.Fatalf("draining cost exact replay: %v", err)
	}
	if _, err := store.PostAdjustment(ctx, directAdj); err != nil {
		t.Fatalf("draining direct exact replay: %v", err)
	}
	// New commands blocked with zero effects.
	costBefore := b2b4CostPassJournals(t, store, costAcct)
	nextProvider := costProvider
	nextProvider.Revision = costProvider.Revision + 1
	nextProvider.ValuationID = "val-b2b4-drain-next"
	nextProvider.Amount = billing.Money{Nano: 70, Currency: "USD"}
	if _, err := store.ApplyCostPassThroughRevision(costCtx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: nextProvider}); err == nil {
		t.Fatalf("draining new cost must be fenced")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("draining cost err = %v, want fence", err)
	}
	if n := b2b4CostPassJournals(t, store, costAcct); n != costBefore {
		t.Fatalf("draining cost fence wrote journals")
	}
	directBeforeAcct, _ := store.GetAccount(ctx, directAcct)
	fresh := billing.AdjustmentInput{AccountID: directAcct, Amount: billing.Money{Nano: 50, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-drain-2", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, fresh); err == nil {
		t.Fatalf("draining new direct must be fenced")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("draining direct err = %v, want fence", err)
	}
	directAfterAcct, _ := store.GetAccount(ctx, directAcct)
	if directAfterAcct.BalanceNano != directBeforeAcct.BalanceNano {
		t.Fatalf("fenced direct mutated balance")
	}
	// Stale claim fenced.
	opKey, _ := billing.DirectFinancialAdjustmentPostingOperationKey(store.StoreID(), directAcct, "b2b4-drain-2")
	staleMeta := billing.CutoverClaimMetadata{Kind: billing.PostingOperationFinancialAdjustment, OperationKey: opKey, AccountID: directAcct, Owner: billing.PostingOwnerV1, MarkerVersion: 1, MarkerEpoch: 1, MarkerState: billing.AccountingCutoverV1Active}
	stale := fresh
	stale.PostingOwner = billing.PostingOwnerV1
	stale.Claim = &staleMeta
	if _, err := store.PostAdjustment(ctx, stale); err == nil {
		t.Fatalf("stale claim must be fenced")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("stale err = %v, want fence", err)
	}
}

func TestB2b4V2ActiveV1BlockedV2Allowed(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-v2")
	ctx := context.Background()
	costAcct := "acct-b2b4-v2-cost"
	_, costCall, costProvider := b2b4SetupCostPassThrough(t, store, costAcct)
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: costProvider}); err != nil {
		t.Fatal(err)
	}
	directAcct := "acct-b2b4-v2-direct"
	b2b4SetupDirectAccount(t, store, directAcct)
	directAdj := billing.AdjustmentInput{AccountID: directAcct, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-v2-1", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, directAdj); err != nil {
		t.Fatal(err)
	}
	// Fresh head for V2 cost, created before cutover while V1 still allowed.
	v2CostAcct := "acct-b2b4-v2-cost2"
	_, v2Call, _ := b2b4SetupCostPassThrough(t, store, v2CostAcct)
	m, _ := store.EnsureAccountingCutover(ctx)
	sh, _ := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b4-v2-shadow"})
	dr, _ := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b4-v2-drain"})
	_ = dr
	cur, _ := store.GetAccountingCutover(ctx)
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b4-v2-active"}); err != nil {
		t.Fatal(err)
	}
	// V1 new blocked.
	nextProvider := costProvider
	nextProvider.Revision = costProvider.Revision + 1
	nextProvider.ValuationID = "val-b2b4-v2-next"
	nextProvider.Amount = billing.Money{Nano: 70, Currency: "USD"}
	costBefore := b2b4CostPassJournals(t, store, costAcct)
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: nextProvider}); err == nil {
		t.Fatalf("v2_active V1 cost new must fail closed")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("v2 V1 cost err = %v, want fence", err)
	}
	if n := b2b4CostPassJournals(t, store, costAcct); n != costBefore {
		t.Fatalf("v2 V1 cost fence wrote journals")
	}
	freshV1 := billing.AdjustmentInput{AccountID: directAcct, Amount: billing.Money{Nano: 50, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-v2-2", Reason: "correction"}
	directBefore, _ := store.GetAccount(ctx, directAcct)
	if _, err := store.PostAdjustment(ctx, freshV1); err == nil {
		t.Fatalf("v2_active V1 direct new must fail closed")
	} else if !b2b4IsFenceErr(err) {
		t.Fatalf("v2 V1 direct err = %v, want fence", err)
	}
	directAfter, _ := store.GetAccount(ctx, directAcct)
	if directAfter.BalanceNano != directBefore.BalanceNano {
		t.Fatalf("v2 V1 direct fence mutated balance")
	}
	// V1 exact replay allowed (no new money).
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: costAcct, CallID: costCall.CallID, ProviderCost: costProvider}); err != nil {
		t.Fatalf("v2_active V1 cost exact replay: %v", err)
	}
	if _, err := store.PostAdjustment(ctx, directAdj); err != nil {
		t.Fatalf("v2_active V1 direct exact replay: %v", err)
	}
	if n := b2b4CostPassJournals(t, store, costAcct); n != costBefore {
		t.Fatalf("V1 replay wrote journals")
	}
	// V2 new allowed on fresh identities (head created pre-cutover, revision V2).
	// Fresh head for V2 cost has no provider yet; post V2 revision in v2_active.
	v2ProviderFresh := billing.CostPassThroughProviderCost{
		LURKey: "lur-b2b4", ValuationID: "val-b2b4-v2-fresh", Revision: 2,
		InputHash:     strings.Repeat("c", 64),
		Amount:        billing.Money{Nano: 80, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
	v2Input := billing.CostPassThroughRevisionInput{AccountID: v2CostAcct, CallID: v2Call.CallID, ProviderCost: v2ProviderFresh, PostingOwner: billing.PostingOwnerV2}
	postedV2, err := store.ApplyCostPassThroughRevision(ctx, v2Input)
	if err != nil {
		t.Fatalf("v2_active V2 cost post: %v", err)
	}
	if !postedV2.Applied {
		t.Fatalf("V2 cost must apply, got %+v", postedV2)
	}
	pinV2 := b2b4CostPassPin(t, store, v2CostAcct, v2Call.CallID)
	if pinV2.Owner != billing.PostingOwnerV2 || !pinV2.IsCompleted() || pinV2.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("V2 cost pin must be completed in v2_active, got %#v", pinV2)
	}
	v2Direct := billing.AdjustmentInput{AccountID: directAcct, Amount: billing.Money{Nano: 25, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-v2-3", Reason: "correction", PostingOwner: billing.PostingOwnerV2}
	postedDirect, err := store.PostAdjustment(ctx, v2Direct)
	if err != nil {
		t.Fatalf("v2_active V2 direct post: %v", err)
	}
	if postedDirect.Replayed {
		t.Fatalf("V2 direct must not be replayed")
	}
	pinDirectV2 := b2b4DirectPin(t, store, directAcct, "b2b4-v2-3")
	if pinDirectV2.Owner != billing.PostingOwnerV2 || !pinDirectV2.IsCompleted() {
		t.Fatalf("V2 direct pin must be completed, got %#v", pinDirectV2)
	}
}

func TestB2b4CrashAtBoundariesIsAtomic(t *testing.T) {
	t.Parallel()
	costPoints := []string{"b2b4-enter", "b2b4-pin-acquire", "b2b4-before-effects", "b2b4-before-journal", "b2b4-before-head", "b2b4-before-pin-complete", "b2b4-before-commit"}
	for _, point := range costPoints {
		func(pt string) {
			store := b2b3NewStore(t, "b2b4-crash-cost-"+pt)
			_, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-crash-cost-"+pt)
			ctx := context.Background()
			input := billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-crash-cost-" + pt, CallID: call.CallID, ProviderCost: provider}
			failed := false
			store.SetAdjustmentFaultHook(func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b4 injected crash at %s", p)
				}
				return nil
			})
			if _, err := store.ApplyCostPassThroughRevision(ctx, input); err == nil {
				t.Fatalf("cost point %s must fail", pt)
			}
			if n := b2b4CostPassJournals(t, store, "acct-b2b4-crash-cost-"+pt); n != 0 {
				t.Fatalf("cost point %s wrote %d journals, want 0", pt, n)
			}
			opKey, _ := billing.CostPassThroughFinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b4-crash-cost-"+pt, call.CallID)
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("cost point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("cost point %s Get pin: %v", pt, perr)
			}
			store.SetAdjustmentFaultHook(nil)
			posted, err := store.ApplyCostPassThroughRevision(ctx, input)
			if err != nil {
				t.Fatalf("cost point %s retry: %v", pt, err)
			}
			if !posted.Applied {
				t.Fatalf("cost point %s retry must apply", pt)
			}
			if n := b2b4CostPassJournals(t, store, "acct-b2b4-crash-cost-"+pt); n != 1 {
				t.Fatalf("cost point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
	directPoints := []string{"b2b4-enter", "b2b4-pin-acquire", "b2b4-before-effects", "b2b4-before-journal", "b2b4-before-pin-complete", "b2b4-before-commit"}
	for _, point := range directPoints {
		func(pt string) {
			store := b2b3NewStore(t, "b2b4-crash-direct-"+pt)
			ctx := b2b4SetupDirectAccount(t, store, "acct-b2b4-crash-direct-"+pt)
			adj := billing.AdjustmentInput{AccountID: "acct-b2b4-crash-direct-" + pt, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-crash-1", Reason: "correction"}
			failed := false
			store.SetAdjustmentFaultHook(func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b4 injected crash at %s", p)
				}
				return nil
			})
			if _, err := store.PostAdjustment(ctx, adj); err == nil {
				t.Fatalf("direct point %s must fail", pt)
			}
			if n := b2b4DirectJournals(t, store, "acct-b2b4-crash-direct-"+pt); n != 0 {
				t.Fatalf("direct point %s wrote %d journals, want 0", pt, n)
			}
			opKey, _ := billing.DirectFinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b4-crash-direct-"+pt, "b2b4-crash-1")
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("direct point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("direct point %s Get pin: %v", pt, perr)
			}
			store.SetAdjustmentFaultHook(nil)
			posted, err := store.PostAdjustment(ctx, adj)
			if err != nil {
				t.Fatalf("direct point %s retry: %v", pt, err)
			}
			if posted.Replayed {
				t.Fatalf("direct point %s retry must apply, not replay", pt)
			}
			if n := b2b4DirectJournals(t, store, "acct-b2b4-crash-direct-"+pt); n != 1 {
				t.Fatalf("direct point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
}

func TestB2b4ConcurrentSingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-concurrent-cost")
	_, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-concurrent-cost")
	input := billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-concurrent-cost", CallID: call.CallID, ProviderCost: provider}
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]billing.CostPassThroughRevisionResult, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = store.ApplyCostPassThroughRevision(context.Background(), input)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent cost identical err = %v, want all succeed as replay", err)
		}
	}
	applied, replayed := 0, 0
	for _, r := range results {
		if r.Applied {
			applied++
		}
		if r.Replayed {
			replayed++
		}
	}
	if applied != 1 || replayed != n-1 {
		t.Fatalf("cost applied=%d replayed=%d, want 1/%d", applied, replayed, n-1)
	}
	if c := b2b4CostPassJournals(t, store, "acct-b2b4-concurrent-cost"); c != 1 {
		t.Fatalf("concurrent cost journals = %d, want 1", c)
	}
	// Direct concurrent.
	dstore := b2b3NewStore(t, "b2b4-concurrent-direct")
	dctx := b2b4SetupDirectAccount(t, dstore, "acct-b2b4-concurrent-direct")
	dadj := billing.AdjustmentInput{AccountID: "acct-b2b4-concurrent-direct", Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-conc-1", Reason: "correction"}
	var dwg sync.WaitGroup
	derrs := make([]error, n)
	dresults := make([]billing.Posting, n)
	for i := range n {
		dwg.Add(1)
		go func(idx int) {
			defer dwg.Done()
			dresults[idx], derrs[idx] = dstore.PostAdjustment(dctx, dadj)
		}(i)
	}
	dwg.Wait()
	for _, err := range derrs {
		if err != nil {
			t.Fatalf("concurrent direct identical err = %v", err)
		}
	}
	dapplied, dreplayed := 0, 0
	for _, r := range dresults {
		if r.Replayed {
			dreplayed++
		} else {
			dapplied++
		}
	}
	if dapplied != 1 || dreplayed != n-1 {
		t.Fatalf("direct applied=%d replayed=%d, want 1/%d", dapplied, dreplayed, n-1)
	}
	if c := b2b4DirectJournals(t, dstore, "acct-b2b4-concurrent-direct"); c != 1 {
		t.Fatalf("concurrent direct journals = %d, want 1", c)
	}
}

func TestB2b4ConcurrentV1VsV2SingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-v1v2-cost")
	_, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-v1v2-cost")
	v1Input := billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-v1v2-cost", CallID: call.CallID, ProviderCost: provider, PostingOwner: billing.PostingOwnerV1}
	v2Input := v1Input
	v2Input.PostingOwner = billing.PostingOwnerV2
	var wg sync.WaitGroup
	var v1Err, v2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, v1Err = store.ApplyCostPassThroughRevision(context.Background(), v1Input)
	}()
	go func() {
		defer wg.Done()
		_, v2Err = store.ApplyCostPassThroughRevision(context.Background(), v2Input)
	}()
	wg.Wait()
	if v1Err != nil {
		t.Fatalf("v1_active V1 contender must win: %v", v1Err)
	}
	if !b2b4IsFenceErr(v2Err) {
		t.Fatalf("v1_active V2 contender err = %v, want fence", v2Err)
	}
	if c := b2b4CostPassJournals(t, store, "acct-b2b4-v1v2-cost"); c != 1 {
		t.Fatalf("V1-wins cost journals = %d, want 1", c)
	}
	// Direct V1 vs V2.
	dstore := b2b3NewStore(t, "b2b4-v1v2-direct")
	dctx := b2b4SetupDirectAccount(t, dstore, "acct-b2b4-v1v2-direct")
	v1Adj := billing.AdjustmentInput{AccountID: "acct-b2b4-v1v2-direct", Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-v1v2-1", Reason: "correction", PostingOwner: billing.PostingOwnerV1}
	v2Adj := v1Adj
	v2Adj.PostingOwner = billing.PostingOwnerV2
	var dwg sync.WaitGroup
	var dv1Err, dv2Err error
	dwg.Add(2)
	go func() {
		defer dwg.Done()
		_, dv1Err = dstore.PostAdjustment(dctx, v1Adj)
	}()
	go func() {
		defer dwg.Done()
		_, dv2Err = dstore.PostAdjustment(dctx, v2Adj)
	}()
	dwg.Wait()
	if dv1Err != nil {
		t.Fatalf("v1_active V1 direct must win: %v", dv1Err)
	}
	if !b2b4IsFenceErr(dv2Err) {
		t.Fatalf("v1_active V2 direct err = %v, want fence", dv2Err)
	}
	if c := b2b4DirectJournals(t, dstore, "acct-b2b4-v1v2-direct"); c != 1 {
		t.Fatalf("V1-wins direct journals = %d, want 1", c)
	}
}

func TestB2b4ReopenPreservesPinAndOutcome(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-reopen-cost")
	ctx, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-reopen-cost")
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-reopen-cost", CallID: call.CallID, ProviderCost: provider}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableStore(ctx, store.DB(), Config{StoreID: "b2b4-reopen-cost"})
	if err != nil {
		t.Fatal(err)
	}
	pin := b2b4CostPassPin(t, reopened, "acct-b2b4-reopen-cost", call.CallID)
	if !pin.IsCompleted() {
		t.Fatalf("reopen cost pin must be completed")
	}
	if c := b2b4CostPassJournals(t, reopened, "acct-b2b4-reopen-cost"); c != 1 {
		t.Fatalf("reopen cost journals = %d, want 1", c)
	}
	replayed, err := reopened.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-reopen-cost", CallID: call.CallID, ProviderCost: provider})
	if err != nil {
		t.Fatalf("reopen cost replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("reopen cost replay must be replay")
	}
	// Direct reopen.
	dstore := b2b3NewStore(t, "b2b4-reopen-direct")
	dctx := b2b4SetupDirectAccount(t, dstore, "acct-b2b4-reopen-direct")
	dadj := billing.AdjustmentInput{AccountID: "acct-b2b4-reopen-direct", Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-reopen-1", Reason: "correction"}
	if _, err := dstore.PostAdjustment(dctx, dadj); err != nil {
		t.Fatal(err)
	}
	reopenedDirect, err := NewDurableStore(dctx, dstore.DB(), Config{StoreID: "b2b4-reopen-direct"})
	if err != nil {
		t.Fatal(err)
	}
	dpin := b2b4DirectPin(t, reopenedDirect, "acct-b2b4-reopen-direct", "b2b4-reopen-1")
	if !dpin.IsCompleted() {
		t.Fatalf("reopen direct pin must be completed")
	}
	if c := b2b4DirectJournals(t, reopenedDirect, "acct-b2b4-reopen-direct"); c != 1 {
		t.Fatalf("reopen direct journals = %d, want 1", c)
	}
}

func TestB2b4B2aCountsIncompletePinsBlocksActivation(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-b2a")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	_, call, provider := b2b4SetupCostPassThrough(t, store, "acct-b2b4-b2a-cost")
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: "acct-b2b4-b2a-cost", CallID: call.CallID, ProviderCost: provider}); err != nil {
		t.Fatal(err)
	}
	// Simulate pre-B2b4 legacy money without pin so draining must observe the
	// incomplete pin after we delete it and re-pin as pinned (crash orphan).
	opKey, _ := billing.CostPassThroughFinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b4-b2a-cost", call.CallID)
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-create a pinned (incomplete) pin to prove activation counts it.
	// Use coordinator-style insert via direct SQL to simulate crash orphan.
	headKey := billing.CostPassThroughHeadKey("acct-b2b4-b2a-cost", call.CallID)
	marker, _ := store.EnsureAccountingCutover(ctx)
	nowUnix := int64(1234567890000000000)
	if _, err := store.DB().NewRaw(`INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), opKey, "acct-b2b4-b2a-cost", call.CallID.String(), "", "", headKey, "", "", billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b4-b2a-drain"); err != nil {
		t.Fatal(err)
	}
	drainStatus, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if drainStatus.Counts.V1Pinned == 0 {
		t.Fatalf("drain status must count incomplete V1 sync pins, got %#v", drainStatus.Counts)
	}
	if drainStatus.ReadyForActivation {
		t.Fatalf("drain status must not be ready with pinned sync work")
	}
	if _, err := store.ActivateCutoverV2(ctx, "b2b4-b2a-activate"); err == nil {
		t.Fatalf("activation with incomplete V1 sync pin must block")
	}
}

func TestB2b4FundingPaymentPolicyNotFenced(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-nofence")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b4-nofence", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	b2b3EnsureShadow(t, store)
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b4-nofence-drain"); err != nil {
		t.Fatal(err)
	}
	// Funding/payment/policy are provisioning, not cutover adjustment writers:
	// they keep existing behavior in draining (no financial_adjustment pin).
	funding := billing.FundingInput{AccountID: acct.ID, Amount: billing.Money{Nano: 100, Currency: "USD"}, SourceKey: "b2b4-fund-1", Reason: "topup"}
	if _, err := store.PostFunding(ctx, funding); err != nil {
		t.Fatalf("funding in draining must not be fenced (provisioning, not adjustment): %v", err)
	}
	payment := billing.PaymentInput{AccountID: acct.ID, Amount: billing.Money{Nano: 50, Currency: "USD"}, SourceKey: "b2b4-pay-1", Reason: "payout"}
	if _, err := store.PostPayment(ctx, payment); err != nil {
		t.Fatalf("payment in draining must not be fenced: %v", err)
	}
	// No financial_adjustment pins for funding/payment.
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ?`, store.StoreID(), string(billing.PostingOperationFinancialAdjustment)).Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("funding/payment must not create financial_adjustment pins, got %d", n)
	}
}
