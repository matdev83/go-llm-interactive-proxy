package billingstore

// Phase 17.3 B2b3 GREEN: financial adjustment posting-time ownership fence.
// Scope: financial_adjustment only (selected-cost corrections). Customer/
// provider already fenced B2b1/B2b2. Reuses RED helpers (b2b3NewStore etc).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func b2b3IsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, ErrOperationConflict)
}

func b2b3Pin(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey string, subject metering.SubjectRef) billing.PostingPin {
	t.Helper()
	opKey, err := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), accountID, callID, headKey, subject)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(context.Background(), billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("adjustment pin missing for %q: %v (no atomic ownership fence)", opKey, err)
	}
	return pin
}

func b2b3AdjustmentInput(store *DurableStore, accountID string, callID billing.BillingCallID, headKey string, subject metering.SubjectRef, expected billing.SelectedCostHeadExpectation, selected billing.SelectedCostValuation) billing.SelectedCostAdjustmentInput {
	return billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: callID, HeadKey: headKey,
		Subject: subject, Expected: expected, Selected: selected,
	}
}

func TestB2b3V1DefaultBindsPinCompletionReplayConflict(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-default-full")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-default-full")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c01")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-default-full", callID.String())
	headKey := "b2b3-default-head"
	first := b2b3Valuation(t, "b2b3-default-val-1", 1, 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-default-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, first))
	if err != nil {
		t.Fatalf("default post: %v", err)
	}
	if applied.Status != billing.SelectedCostTransitionApplied {
		t.Fatalf("first status = %q, want applied", applied.Status)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-default-full"); n != 1 {
		t.Fatalf("journals = %d, want 1", n)
	}
	pin := b2b3Pin(t, store, "acct-b2b3-default-full", callID, headKey, subject)
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("default pin must be V1 completed, got %#v", pin)
	}
	if pin.CompletionOperationKey != applied.OperationKey || pin.CompletionTransactionID != applied.TransactionID {
		t.Fatalf("pin completion must match adjustment outcome: pin %#v applied %#v", pin, applied)
	}
	replayed, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-default-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, first))
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replayed.Status != billing.SelectedCostTransitionReplay {
		t.Fatalf("replay status = %q, want replay", replayed.Status)
	}
	if replayed.OperationKey != applied.OperationKey || replayed.TransactionID != applied.TransactionID {
		t.Fatalf("replay identity must be stable")
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-default-full"); n != 1 {
		t.Fatalf("replay journals = %d, want 1", n)
	}
	conflictVal := b2b3Valuation(t, "b2b3-default-val-1", 1, 11_000_000_000)
	conflictInput := b2b3AdjustmentInput(store, "acct-b2b3-default-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, conflictVal)
	// Same revision different amount under same head: planner returns conflict
	// (zero-effect) or store conflict; either way no new money.
	before := b2b3AdjustmentJournals(t, store, "acct-b2b3-default-full")
	if _, err := store.ApplySelectedCostAdjustment(ctx, conflictInput); err != nil {
		if !b2b3IsFenceErr(err) && !errors.Is(err, billing.ErrSelectedCostAdjustmentConflict) && !errors.Is(err, billing.ErrProviderCostRevisionConflict) {
			// Planner conflict returns nil error with Conflict status, not an
			// error; any error here must be fence/conflict.
			t.Fatalf("conflicting amount err = %v, want fence/conflict or planner conflict status", err)
		}
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-default-full"); n != before {
		t.Fatalf("conflict mutated journals %d -> %d", before, n)
	}
}

func TestB2b3DrainingPinnedWithClaimPostsStaleWithoutClaimFenced(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-drain-full")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	b2b3SetupAccount(t, store, "acct-b2b3-drain-full")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c02")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-drain-full", callID.String())
	headKey := "b2b3-drain-head"
	// Seed head before draining. To simulate pre-B2b3 legacy work (head
	// without pin), delete the seed pin. F4 empty inventory means draining
	// never invents a phantom pinned pin; exact replay backfills the real
	// completed pin with its durable outcome, then a new revision with claim
	// posts.
	seedVal := b2b3Valuation(t, "b2b3-drain-seed-1", 1, 10_000_000_000)
	seeded, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-drain-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, seedVal))
	if err != nil {
		t.Fatal(err)
	}
	_ = seeded
	seedOpKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-drain-full", callID, headKey, subject)
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), seedOpKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b3-drain-full-go"); err != nil {
		t.Fatal(err)
	}
	opKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-drain-full", callID, headKey, subject)
	// F4: no phantom pin for historical head.
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); err == nil {
		t.Fatalf("F4: historical head must not invent a phantom adjustment pin")
	}
	// Exact replay of the historical adjustment backfills the real completed
	// pin with its durable journal/link outcome (never a fabricated tx).
	replayedSeed, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-drain-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, seedVal))
	if err != nil {
		t.Fatalf("draining exact replay must backfill real pin: %v", err)
	}
	if replayedSeed.Status != billing.SelectedCostTransitionReplay {
		t.Fatalf("replay status = %q, want replay", replayedSeed.Status)
	}
	pre, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("backfilled pin missing: %v", err)
	}
	if pre.Owner != billing.PostingOwnerV1 || !pre.IsCompleted() {
		t.Fatalf("backfilled pin must be V1 completed, got %#v", pre)
	}
	if pre.CompletionOperationKey != replayedSeed.OperationKey || pre.CompletionTransactionID != replayedSeed.TransactionID {
		t.Fatalf("backfill must reference real durable outcome")
	}
	// New revision with matching claim posts in draining.
	head, err := store.GetSelectedCostHead(ctx, "acct-b2b3-drain-full", callID, headKey)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("claim metadata: %v", err)
	}
	nextVal := b2b3Valuation(t, "b2b3-drain-val-2", 2, 8_000_000_000)
	withClaim := b2b3AdjustmentInput(store, "acct-b2b3-drain-full", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected}, nextVal)
	withClaim.PostingOwner = billing.PostingOwnerV1
	withClaim.Claim = &meta
	posted, err := store.ApplySelectedCostAdjustment(ctx, withClaim)
	if err != nil {
		t.Fatalf("draining classified post: %v", err)
	}
	if posted.Status != billing.SelectedCostTransitionApplied {
		t.Fatalf("draining status = %q, want applied", posted.Status)
	}
	post, _ := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if !post.IsCompleted() || post.Owner != billing.PostingOwnerV1 {
		t.Fatalf("draining pin must stay V1 completed, got %#v", post)
	}
	if post.CompletionOperationKey != posted.OperationKey {
		t.Fatalf("replacement must advance pin completion to latest outcome")
	}
	// Without claim must reject even though pin exists.
	head2, _ := store.GetSelectedCostHead(ctx, "acct-b2b3-drain-full", callID, headKey)
	thirdVal := b2b3Valuation(t, "b2b3-drain-val-3", 3, 7_000_000_000)
	noClaim := b2b3AdjustmentInput(store, "acct-b2b3-drain-full", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: head2.Version, Previous: head2.Selected}, thirdVal)
	before := b2b3AdjustmentJournals(t, store, "acct-b2b3-drain-full")
	if _, err := store.ApplySelectedCostAdjustment(ctx, noClaim); err == nil {
		t.Fatalf("draining without claim must reject")
	} else if !b2b3IsFenceErr(err) {
		t.Fatalf("no-claim err = %v, want fence", err)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-drain-full"); n != before {
		t.Fatalf("no-claim fence wrote journals")
	}
	// Stale claim epoch fenced.
	staleMeta := meta
	staleMeta.MarkerEpoch--
	staleMeta.MarkerVersion--
	stale := noClaim
	stale.PostingOwner = billing.PostingOwnerV1
	stale.Claim = &staleMeta
	if _, err := store.ApplySelectedCostAdjustment(ctx, stale); err == nil {
		t.Fatalf("stale claim must be fenced")
	} else if !b2b3IsFenceErr(err) {
		t.Fatalf("stale err = %v, want fence", err)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-drain-full"); n != before {
		t.Fatalf("stale fence wrote journals")
	}
}

func TestB2b3V2ActiveV2AllowedV1ReplayOnly(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-v2-full")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-v2-full")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c03")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-v2-full", callID.String())
	headKey := "b2b3-v2-head"
	first := b2b3Valuation(t, "b2b3-v2-val-1", 1, 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-v2-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, first))
	if err != nil {
		t.Fatal(err)
	}
	_ = applied
	m, _ := store.EnsureAccountingCutover(ctx)
	sh, _ := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b3-v2-shadow"})
	dr, _ := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b3-v2-drain"})
	_ = dr
	cur, _ := store.GetAccountingCutover(ctx)
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b3-v2-active"}); err != nil {
		t.Fatal(err)
	}
	// V1 new blocked.
	second := b2b3Valuation(t, "b2b3-v2-val-2", 2, 9_000_000_000)
	head, _ := store.GetSelectedCostHead(ctx, "acct-b2b3-v2-full", callID, headKey)
	newV1 := b2b3AdjustmentInput(store, "acct-b2b3-v2-full", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected}, second)
	before := b2b3AdjustmentJournals(t, store, "acct-b2b3-v2-full")
	if _, err := store.ApplySelectedCostAdjustment(ctx, newV1); err == nil {
		t.Fatalf("v2_active V1 new must fail closed")
	} else if !b2b3IsFenceErr(err) {
		t.Fatalf("v2 V1 err = %v, want fence", err)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-v2-full"); n != before {
		t.Fatalf("v2 V1 fence wrote journals")
	}
	// V1 exact replay allowed (no new money).
	replay, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-v2-full", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, first))
	if err != nil {
		t.Fatalf("v2_active V1 exact replay: %v", err)
	}
	if replay.Status != billing.SelectedCostTransitionReplay {
		t.Fatalf("V1 replay status = %q, want replay", replay.Status)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-v2-full"); n != before {
		t.Fatalf("V1 replay wrote journals")
	}
	// V2 new allowed on a fresh head.
	callV2, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c04")
	subjectV2 := b2b3Subject(store.StoreID(), "acct-b2b3-v2-full", callV2.String())
	headV2 := "b2b3-v2-head2"
	v2First := b2b3Valuation(t, "b2b3-v2-val2-1", 1, 5_000_000_000)
	v2Input := b2b3AdjustmentInput(store, "acct-b2b3-v2-full", callV2, headV2, subjectV2, billing.SelectedCostHeadExpectation{}, v2First)
	v2Input.PostingOwner = billing.PostingOwnerV2
	postedV2, err := store.ApplySelectedCostAdjustment(ctx, v2Input)
	if err != nil {
		t.Fatalf("v2_active V2 post: %v", err)
	}
	if postedV2.Status != billing.SelectedCostTransitionApplied {
		t.Fatalf("V2 status = %q, want applied", postedV2.Status)
	}
	opKeyV2, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-v2-full", callV2, headV2, subjectV2)
	pinV2, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKeyV2)
	if err != nil {
		t.Fatalf("V2 pin missing: %v", err)
	}
	if pinV2.Owner != billing.PostingOwnerV2 || !pinV2.IsCompleted() || pinV2.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("V2 pin must be completed in v2_active, got %#v", pinV2)
	}
}

func TestB2b3ReplacementChainRetainsLinkageOneAuthority(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-chain")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-chain")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c05")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-chain", callID.String())
	headKey := "b2b3-chain-head"
	v1 := b2b3Valuation(t, "b2b3-chain-val-1", 1, 10_000_000_000)
	a1, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-chain", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, v1))
	if err != nil {
		t.Fatal(err)
	}
	v2 := b2b3Valuation(t, "b2b3-chain-val-2", 2, 8_000_000_000)
	a2, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-chain", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: a1.HeadVersion, Previous: &v1}, v2))
	if err != nil {
		t.Fatal(err)
	}
	if a1.OperationKey == a2.OperationKey {
		t.Fatalf("replacement must get its own canonical operation key")
	}
	if a1.LinkKey == a2.LinkKey {
		t.Fatalf("replacement must get its own link key")
	}
	opKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-chain", callID, headKey, subject)
	pin, _ := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if pin.Owner != billing.PostingOwnerV1 {
		t.Fatalf("chain must retain one V1 authority, got %#v", pin)
	}
	if pin.CompletionOperationKey != a2.OperationKey || pin.CompletionTransactionID != a2.TransactionID {
		t.Fatalf("pin must advance to latest outcome: pin %#v a2 %#v", pin, a2)
	}
	// Linkage retained: two immutable rows, head points to latest.
	var adjCount int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_selected_cost_adjustments WHERE store_id = ? AND account_id = ?`, store.StoreID(), "acct-b2b3-chain").Scan(ctx, &adjCount); err != nil {
		t.Fatal(err)
	}
	if adjCount != 2 {
		t.Fatalf("adjustments = %d, want 2 (immutable linkage)", adjCount)
	}
	head, _ := store.GetSelectedCostHead(ctx, "acct-b2b3-chain", callID, headKey)
	if head.Version != 2 || head.LastOperationKey != a2.OperationKey || head.LastTransactionID != a2.TransactionID {
		t.Fatalf("head must point to latest: %#v", head)
	}
	// Journals chained via reversal linkage.
	txs, _ := store.JournalTransactions(ctx, "acct-b2b3-chain")
	var first, second *struct{ ID, Reversal string }
	_ = first
	_ = second
	foundFirst, foundSecond := false, false
	for _, tx := range txs {
		if tx.ID == a1.TransactionID {
			foundFirst = true
		}
		if tx.ID == a2.TransactionID {
			foundSecond = true
			if tx.ReversalOf != a1.TransactionID || tx.CorrectsTransactionID != a1.TransactionID {
				t.Fatalf("correction must chain to original: %#v", tx)
			}
		}
	}
	if !foundFirst || !foundSecond {
		t.Fatalf("both journals must exist")
	}
	// Conflicting owner on same head fails.
	conflict := b2b3AdjustmentInput(store, "acct-b2b3-chain", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected}, b2b3Valuation(t, "b2b3-chain-val-3", 3, 6_000_000_000))
	conflict.PostingOwner = billing.PostingOwnerV2
	before := b2b3AdjustmentJournals(t, store, "acct-b2b3-chain")
	if _, err := store.ApplySelectedCostAdjustment(ctx, conflict); err == nil {
		t.Fatalf("conflicting owner must fail")
	} else if !b2b3IsFenceErr(err) {
		t.Fatalf("owner conflict err = %v, want fence/conflict", err)
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-chain"); n != before {
		t.Fatalf("owner conflict wrote journals")
	}
}

func TestB2b3CrashAtBoundariesIsAtomic(t *testing.T) {
	t.Parallel()
	points := []string{"b2b3-enter", "b2b3-pin-acquire", "b2b3-before-effects", "b2b3-before-journal", "b2b3-before-pin-complete", "b2b3-before-commit"}
	for _, point := range points {
		func(pt string) {
			store := b2b3NewStore(t, "b2b3-crash-"+pt)
			ctx := context.Background()
			b2b3SetupAccount(t, store, "acct-b2b3-crash-"+pt)
			callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c06")
			subject := b2b3Subject(store.StoreID(), "acct-b2b3-crash-"+pt, callID.String())
			headKey := "b2b3-crash-head"
			input := b2b3AdjustmentInput(store, "acct-b2b3-crash-"+pt, callID, headKey, subject, billing.SelectedCostHeadExpectation{}, b2b3Valuation(t, "b2b3-crash-val-1", 1, 10_000_000_000))
			failed := false
			store.SetAdjustmentFaultHook(func(p string) error {
				if p == pt && !failed {
					failed = true
					return fmt.Errorf("b2b3 injected crash at %s", p)
				}
				return nil
			})
			if _, err := store.ApplySelectedCostAdjustment(ctx, input); err == nil {
				t.Fatalf("point %s must fail", pt)
			}
			if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-crash-"+pt); n != 0 {
				t.Fatalf("point %s wrote %d journals, want 0", pt, n)
			}
			opKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-crash-"+pt, callID, headKey, subject)
			if pin, perr := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); perr == nil {
				if pin.IsCompleted() {
					t.Fatalf("point %s left pin completed without money commit (window)", pt)
				}
			} else if !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
				t.Fatalf("point %s Get pin: %v", pt, perr)
			}
			var headCount int
			if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_heads WHERE store_id = ? AND account_id = ?`, store.StoreID(), "acct-b2b3-crash-"+pt).Scan(ctx, &headCount); err != nil {
				t.Fatal(err)
			}
			if headCount != 0 {
				t.Fatalf("point %s wrote %d heads, want 0", pt, headCount)
			}
			store.SetAdjustmentFaultHook(nil)
			posted, err := store.ApplySelectedCostAdjustment(ctx, input)
			if err != nil {
				t.Fatalf("point %s retry: %v", pt, err)
			}
			if posted.Status != billing.SelectedCostTransitionApplied {
				t.Fatalf("point %s retry must apply", pt)
			}
			if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-crash-"+pt); n != 1 {
				t.Fatalf("point %s retry journals = %d, want 1", pt, n)
			}
		}(point)
	}
}

func TestB2b3ConcurrentOwnersSingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-concurrent")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c07")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-concurrent", callID.String())
	b2b3SetupAccount(t, store, "acct-b2b3-concurrent")
	headKey := "b2b3-concurrent-head"
	val := b2b3Valuation(t, "b2b3-concurrent-val-1", 1, 10_000_000_000)
	input := b2b3AdjustmentInput(store, "acct-b2b3-concurrent", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]billing.SelectedCostAdjustmentResult, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = store.ApplySelectedCostAdjustment(context.Background(), input)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical err = %v, want all succeed as replay", err)
		}
	}
	applied, replayed := 0, 0
	for _, r := range results {
		switch r.Status {
		case billing.SelectedCostTransitionApplied:
			applied++
		case billing.SelectedCostTransitionReplay:
			replayed++
		default:
			t.Fatalf("concurrent status = %q", r.Status)
		}
	}
	if applied != 1 || replayed != n-1 {
		t.Fatalf("applied=%d replayed=%d, want 1/%d", applied, replayed, n-1)
	}
	if c := b2b3AdjustmentJournals(t, store, "acct-b2b3-concurrent"); c != 1 {
		t.Fatalf("concurrent journals = %d, want 1", c)
	}
}

func TestB2b3ConcurrentV1VsV2SingleWinner(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-v1v2")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c08")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-v1v2", callID.String())
	b2b3SetupAccount(t, store, "acct-b2b3-v1v2")
	headKey := "b2b3-v1v2-head"
	val := b2b3Valuation(t, "b2b3-v1v2-val-1", 1, 10_000_000_000)
	v1Input := b2b3AdjustmentInput(store, "acct-b2b3-v1v2", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val)
	v1Input.PostingOwner = billing.PostingOwnerV1
	v2Input := v1Input
	v2Input.PostingOwner = billing.PostingOwnerV2
	var wg sync.WaitGroup
	var v1Err, v2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, v1Err = store.ApplySelectedCostAdjustment(context.Background(), v1Input)
	}()
	go func() {
		defer wg.Done()
		_, v2Err = store.ApplySelectedCostAdjustment(context.Background(), v2Input)
	}()
	wg.Wait()
	if v1Err != nil {
		t.Fatalf("v1_active V1 contender must win: %v", v1Err)
	}
	if !b2b3IsFenceErr(v2Err) {
		t.Fatalf("v1_active V2 contender err = %v, want fence", v2Err)
	}
	if c := b2b3AdjustmentJournals(t, store, "acct-b2b3-v1v2"); c != 1 {
		t.Fatalf("V1-wins journals = %d, want 1", c)
	}
}

func TestB2b3ReopenPreservesPinAndOutcome(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-reopen")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-reopen")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c09")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-reopen", callID.String())
	headKey := "b2b3-reopen-head"
	val := b2b3Valuation(t, "b2b3-reopen-val-1", 1, 10_000_000_000)
	if _, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-reopen", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val)); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableStore(ctx, store.DB(), Config{StoreID: "b2b3-reopen"})
	if err != nil {
		t.Fatal(err)
	}
	pin := b2b3Pin(t, reopened, "acct-b2b3-reopen", callID, headKey, subject)
	if !pin.IsCompleted() {
		t.Fatalf("reopen pin must be completed")
	}
	if c := b2b3AdjustmentJournals(t, reopened, "acct-b2b3-reopen"); c != 1 {
		t.Fatalf("reopen journals = %d, want 1", c)
	}
	replayed, err := reopened.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(reopened, "acct-b2b3-reopen", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val))
	if err != nil {
		t.Fatalf("reopen replay: %v", err)
	}
	if replayed.Status != billing.SelectedCostTransitionReplay {
		t.Fatalf("reopen replay must be replay")
	}
}

func TestB2b3NoOpCompletesPinWithoutJournal(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-noop")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-noop")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c10")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-noop", callID.String())
	headKey := "b2b3-noop-head"
	v1 := b2b3Valuation(t, "b2b3-noop-val-1", 1, 10_000_000_000)
	a1, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-noop", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, v1))
	if err != nil {
		t.Fatal(err)
	}
	v2 := b2b3Valuation(t, "b2b3-noop-val-2", 2, 10_000_000_000)
	noop, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-noop", callID, headKey, subject,
		billing.SelectedCostHeadExpectation{Version: a1.HeadVersion, Previous: &v1}, v2))
	if err != nil {
		t.Fatal(err)
	}
	if noop.Status != billing.SelectedCostTransitionNoOp {
		t.Fatalf("zero delta must be NoOp, got %q", noop.Status)
	}
	if noop.TransactionID != "" {
		t.Fatalf("NoOp must carry no journal transaction")
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-noop"); n != 1 {
		t.Fatalf("NoOp journals = %d, want 1 (no new money)", n)
	}
	opKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-noop", callID, headKey, subject)
	pin, _ := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if !pin.IsCompleted() {
		t.Fatalf("NoOp must complete pin")
	}
	if pin.CompletionOperationKey != noop.OperationKey {
		t.Fatalf("NoOp must advance pin completion")
	}
}
