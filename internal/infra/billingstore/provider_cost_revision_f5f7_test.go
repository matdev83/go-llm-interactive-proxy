package billingstore

// Phase 17.3 F5+F7 GREEN: immutable provider revision ownership + atomic legacy handoff.
// Each revision outcome is its own canonical pin derived from
// ProviderCostRevisionSourceKey (revision-specific, not merely lineage).
// Base legacy keeps lineage pin; higher/replacement revisions are distinct
// pins while heads/fences order lineage. Completed pins immutable: exact
// same operation+fingerprint+tx replay only. Handoff/promotion branches
// acquire revision pin, post delta, complete pin atomically in same tx.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestF5F7_ExactReplayAfterActiveSucceeds(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-replay")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-green-head", 1, 20, true)
	posted, err := store.ApplyProviderCostRevision(ctx, base)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("seed must apply")
	}
	f5f7Activate(t, store)
	replay, err := store.ApplyProviderCostRevision(ctx, base)
	if err != nil {
		t.Fatalf("exact V1 replay after active: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("exact replay must be replayed, got %+v", replay)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("replay journals=%d want 1 (no new money)", n)
	}
	pinKey, err := billing.ProviderRevisionPostingOperationKey(base)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, pinKey)
	if err != nil {
		t.Fatal(err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("replay pin must stay completed")
	}
}

func TestF5F7_V2HigherRevisionPostsAndAdvancesHead(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-v2")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-v2", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	v1 := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-v2-head", 1, 20, true)
	if _, err := store.ApplyProviderCostRevision(ctx, v1); err != nil {
		t.Fatalf("V1 base: %v", err)
	}
	f5f7Activate(t, store)
	v2 := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-v2-head", 2, 35, true)
	v2.PostingOwner = billing.PostingOwnerV2
	posted, err := store.ApplyProviderCostRevision(ctx, v2)
	if err != nil {
		t.Fatalf("V2 higher after active: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("V2 must apply")
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 2 {
		t.Fatalf("journals=%d want 2 (V1+V2 delta)", n)
	}
	head, err := store.GetProviderCostHead(ctx, acct.ID, callID, v2.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.EvidenceRevision != 2 || head.CurrentAmount.Nano != 35 {
		t.Fatalf("head must advance to V2 rev2 amount 35, got rev=%d amount=%d", head.EvidenceRevision, head.CurrentAmount.Nano)
	}
	v2Key, err := billing.ProviderRevisionPostingOperationKey(v2)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, v2Key)
	if err != nil {
		t.Fatalf("V2 pin missing: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() {
		t.Fatalf("V2 pin must be V2 completed, got %#v", pin)
	}
	v1Key, err := billing.ProviderRevisionPostingOperationKey(v1)
	if err != nil {
		t.Fatal(err)
	}
	if v1Key == v2Key {
		t.Fatalf("V1/V2 must be distinct pins")
	}
	v1Pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, v1Key)
	if err != nil {
		t.Fatal(err)
	}
	if !v1Pin.IsCompleted() || v1Pin.Owner != billing.PostingOwnerV1 {
		t.Fatalf("V1 history must stay V1 completed, got %#v", v1Pin)
	}
}

func TestF5F7_ConcurrentV1V2AfterActive(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-conc")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-conc", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-conc-head", 1, 20, true)
	if _, err := store.ApplyProviderCostRevision(ctx, base); err != nil {
		t.Fatal(err)
	}
	f5f7Activate(t, store)
	v1Higher := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-conc-head", 2, 99, true)
	v2Higher := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-conc-head", 2, 35, true)
	v2Higher.PostingOwner = billing.PostingOwnerV2
	// Use different evidence hashes to avoid same-pin collision; V1 and V2 with
	// same rev but different amounts would share sourceKey only if hash same.
	// Force distinct hashes via distinct InputSetHash to get distinct pins.
	v1Higher.InputSetHash = fmt.Sprintf("%064x", 1001)
	v1Higher.ValuationID = "f5f7-conc-v1-val"
	v2Higher.InputSetHash = fmt.Sprintf("%064x", 2002)
	v2Higher.ValuationID = "f5f7-conc-v2-val"
	var wg sync.WaitGroup
	var v1Err, v2Err error
	var v2Res billing.ProviderCostRevisionResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, v1Err = store.ApplyProviderCostRevision(context.Background(), v1Higher)
	}()
	go func() {
		defer wg.Done()
		v2Res, v2Err = store.ApplyProviderCostRevision(context.Background(), v2Higher)
	}()
	wg.Wait()
	if !f5f7IsFenceErr(v1Err) {
		t.Fatalf("concurrent V1 higher must fence, err=%v", v1Err)
	}
	if v2Err != nil {
		t.Fatalf("concurrent V2 higher: %v", v2Err)
	}
	if !v2Res.Applied {
		t.Fatalf("V2 must apply")
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 2 {
		t.Fatalf("journals=%d want 2 (base+V2, V1 fenced)", n)
	}
}

func TestF5F7_NonpayableExclusionRevision(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-excl")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-excl", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	excl := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-excl-head", 1, 0, false)
	res, err := store.ApplyProviderCostRevision(ctx, excl)
	if err != nil {
		t.Fatalf("exclusion: %v", err)
	}
	if !res.Ignored {
		t.Fatalf("must be ignored, got %+v", res)
	}
	exclKey, err := billing.ProviderRevisionPostingOperationKey(excl)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, exclKey)
	if err != nil {
		t.Fatal(err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("exclusion pin must complete")
	}
	// Replacement payable for same head lineage but higher revision is distinct pin.
	payable := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-excl-head", 2, 25, true)
	pres, err := store.ApplyProviderCostRevision(ctx, payable)
	if err != nil {
		t.Fatalf("payable after exclusion: %v", err)
	}
	if !pres.Applied {
		t.Fatalf("payable must apply")
	}
	payKey, err := billing.ProviderRevisionPostingOperationKey(payable)
	if err != nil {
		t.Fatal(err)
	}
	if payKey == exclKey {
		t.Fatalf("exclusion/payable must be distinct pins")
	}
	// New V1 exclusion after active fenced; exact replay of prior exclusion succeeds.
	f5f7Activate(t, store)
	newExcl := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-excl-head", 3, 0, false)
	if _, err := store.ApplyProviderCostRevision(ctx, newExcl); !f5f7IsFenceErr(err) {
		t.Fatalf("new V1 exclusion after active must fence, err=%v", err)
	}
	journalsBefore := f5f7ProviderJournals(t, store, acct.ID)
	replay, err := store.ApplyProviderCostRevision(ctx, excl)
	if err != nil {
		t.Fatalf("exact exclusion replay after active: %v", err)
	}
	// Prior exclusion rev1 is now stale vs head rev2 (payable advanced); stale
	// is idempotent without new money, same as ignored replay before advancement.
	if !replay.Ignored && !replay.Stale {
		t.Fatalf("exclusion replay must stay ignored/stale, got %+v", replay)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != journalsBefore {
		t.Fatalf("replay journals=%d want %d (no new money)", n, journalsBefore)
	}
}

func TestF5F7_ReplacementSequenceDistinctPinsImmutableHistory(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-seq")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-seq", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	amounts := []int64{10, 8, 12}
	var keys []string
	for i, amt := range amounts {
		in := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-seq-head", uint64(i+1), amt, true)
		res, err := store.ApplyProviderCostRevision(ctx, in)
		if err != nil {
			t.Fatalf("rev %d: %v", i+1, err)
		}
		if !res.Applied {
			t.Fatalf("rev %d must apply", i+1)
		}
		k, err := billing.ProviderRevisionPostingOperationKey(in)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if keys[0] == keys[1] || keys[1] == keys[2] || keys[0] == keys[2] {
		t.Fatalf("replacement sequence must be distinct pins: %v", keys)
	}
	for i, k := range keys {
		pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, k)
		if err != nil {
			t.Fatalf("pin %d missing: %v", i+1, err)
		}
		if !pin.IsCompleted() {
			t.Fatalf("pin %d must stay completed", i+1)
		}
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 3 {
		t.Fatalf("journals=%d want 3", n)
	}
	head, err := store.GetProviderCostHead(ctx, acct.ID, callID, "f5f7-seq-head")
	if err != nil {
		t.Fatal(err)
	}
	if head.EvidenceRevision != 3 || head.CurrentAmount.Nano != 12 {
		t.Fatalf("head must be rev3 amount 12, got rev=%d amount=%d", head.EvidenceRevision, head.CurrentAmount.Nano)
	}
	// Same-pin different outcome never updates: same revision/hash but changed amount conflicts.
	dup := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-seq-head", 3, 99, true)
	// Force same sourceKey as rev3 by reusing its hash/valuation? rev3 hash is 000...3.
	// dup uses same rev3 hash (000...3) but amount 99 -> same pin, different fingerprint.
	if _, err := store.ApplyProviderCostRevision(ctx, dup); err == nil {
		t.Fatalf("same-pin changed amount must conflict (immutable outcome)")
	} else if !errors.Is(err, ErrOperationConflict) && !errors.Is(err, billing.ErrProviderCostRevisionConflict) && !f5f7IsFenceErr(err) {
		t.Fatalf("same-pin conflict err=%v, want conflict/fence", err)
	}
}

func TestF5F7_PrePinLegacyUpgradeBackfillsFromDurableOutcome(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-prepin")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-prepin", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-f5f7"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-f5f7")
	leg.ALegID = "a-f5f7"
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	legacyResult := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 30, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: legacyResult}); err != nil {
		t.Fatalf("legacy: %v", err)
	}
	// Simulate pre-pin legacy row: delete legacy lineage pin, keep fence/journal.
	legacyOp, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), legacyOp).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	rev := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-prepin-head", 2, 45, true)
	rev.Subject.BLegID = "b-f5f7"
	rev.Subject.ALegID = "a-f5f7"
	res, err := store.ApplyProviderCostRevision(ctx, rev)
	if err != nil {
		t.Fatalf("pre-pin handoff: %v", err)
	}
	if !res.Applied {
		t.Fatalf("must apply")
	}
	revKey, err := billing.ProviderRevisionPostingOperationKey(rev)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, revKey)
	if err != nil {
		t.Fatalf("revision pin must exist: %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("must complete atomically")
	}
	// Completion must reference actual durable tx (new delta journal), not fabricated empty.
	if pin.CompletionOperationKey != revKey || pin.CompletionTransactionID == "" {
		t.Fatalf("completion must be actual outcome op=%q tx=%q", pin.CompletionOperationKey, pin.CompletionTransactionID)
	}
}

func TestF5F7_PromotionBranchNonpayableFenceToPayable(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-promo")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-green-promo", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	excl := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-promo-head", 1, 0, false)
	if _, err := store.ApplyProviderCostRevision(ctx, excl); err != nil {
		t.Fatalf("exclusion fence: %v", err)
	}
	payable := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-promo-head", 2, 25, true)
	res, err := store.ApplyProviderCostRevision(ctx, payable)
	if err != nil {
		t.Fatalf("promotion to payable: %v", err)
	}
	if !res.Applied {
		t.Fatalf("promotion must apply")
	}
	payKey, err := billing.ProviderRevisionPostingOperationKey(payable)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, payKey)
	if err != nil {
		t.Fatal(err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("promotion pin must complete atomically")
	}
	head, err := store.GetProviderCostHead(ctx, acct.ID, callID, payable.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.CurrentAmount.Nano != 25 {
		t.Fatalf("head amount=%d want 25", head.CurrentAmount.Nano)
	}
}

func TestF5F7_F6TokenBindsRevisionPin(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-green-f6")
	ctx := context.Background()
	_ = func() billing.AccountingCutoverMarker {
		m, err := store.EnsureAccountingCutover(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f5f7-f6-shadow",
		})
		if err != nil {
			t.Fatal(err)
		}
		return sh
	}()
	acct := billing.Account{ID: "acct-f5f7-green-f6", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	callRec := testIndependentCallUsageFor(callID, []string{"b-f5f7"})
	callRec.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, callRec); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-f5f7")
	leg.ALegID = "a-f5f7"
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f5f7-f6-drain"); err != nil {
		t.Fatal(err)
	}
	rev := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-f6-head", 1, 20, true)
	revKey, err := billing.ProviderRevisionPostingOperationKey(rev)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.coordinatorInsertProviderRevisionPin(ctx, marker, acct.ID, callID, rev.Subject, revKey); err != nil {
		t.Fatalf("classify revision: %v", err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, revKey)
	if err != nil {
		t.Fatalf("revision token: %v", err)
	}
	if meta.OperationKey != revKey {
		t.Fatalf("F6 token must bind revision pin %q, got %q", revKey, meta.OperationKey)
	}
	// Lineage token must NOT validate as revision claim.
	lineageKey, err := billing.ProviderPostingOperationKey(store.StoreID(), acct.ID, callID, rev.Subject)
	if err != nil {
		t.Fatal(err)
	}
	lineageMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, lineageKey)
	if err == nil {
		// If lineage pin exists (from leg classification), its token must fail revision validation.
		if err := billing.ValidateProviderRevisionClaim(lineageMeta, rev); err == nil {
			t.Fatalf("lineage token must not validate as revision claim")
		}
	}
	// Current-marker authority required: stale token fences.
	stale := meta
	stale.MarkerEpoch--
	if _, err := store.ApplyProviderCostRevision(ctx, func() billing.ProviderCostRevisionInput {
		c := rev
		c.PostingOwner = stale.Owner
		c.Claim = &stale
		return c
	}()); !f5f7IsFenceErr(err) {
		t.Fatalf("stale revision token must fence, err=%v", err)
	}
	// Fresh token posts.
	fresh := rev
	fresh.PostingOwner = meta.Owner
	fresh.Claim = &meta
	if _, err := store.ApplyProviderCostRevision(ctx, fresh); err != nil {
		t.Fatalf("fresh revision token must post: %v", err)
	}
}
