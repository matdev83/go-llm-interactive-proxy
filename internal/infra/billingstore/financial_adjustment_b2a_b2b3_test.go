package billingstore

// Phase 17.3 B2b3 B2a coverage (F4): adjustment inventory is explicitly empty.
// Synchronous adjustments have no durable pending queue (F1 serializes them),
// so historical heads are never classified into phantom pins. Activation
// counts only real incomplete financial_adjustment pins (B2b4 orphan proof);
// this test proves ordinary completed adjustment history with a legacy
// head-without-pin leaves no phantom and activates.

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestB2b3B2aClassifiesAdjustmentHeadsBoundedAndActivationCountsPins(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-b2a")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	b2b3SetupAccount(t, store, "acct-b2b3-b2a")
	callID, _ := billing.ParseBillingCallID("bc_00000000000000000000000000000c12")
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-b2a", callID.String())
	headKey := "b2b3-b2a-head"
	val := b2b3Valuation(t, "b2b3-b2a-val-1", 1, 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-b2a", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val))
	if err != nil {
		t.Fatal(err)
	}
	_ = applied
	// Simulate pre-B2b3 legacy head without pin: F4 empty inventory means
	// draining must NOT invent a phantom pinned pin for this historical head.
	// Only real incomplete pins (B2b4 orphan proof) block activation.
	opKey, _ := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-b2a", callID, headKey, subject)
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), opKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	marker, status, err := store.BeginCutoverDraining(ctx, "b2b3-b2a-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	if marker.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("marker = %q, want draining", marker.State)
	}
	// F4: no phantom pin for historical head.
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); err == nil {
		t.Fatalf("F4: historical head must not invent a phantom adjustment pin")
	}
	drainStatus, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if drainStatus.Counts.AdjustmentPending != 0 {
		t.Fatalf("F4: AdjustmentPending = %d, want 0 (empty inventory)", drainStatus.Counts.AdjustmentPending)
	}
	if drainStatus.Counts.V1Pinned != 0 {
		t.Fatalf("F4: V1Pinned = %d, want 0 (no real incomplete pins)", drainStatus.Counts.V1Pinned)
	}
	if !drainStatus.ReadyForActivation {
		t.Fatalf("F4: drain must be ready with only historical heads: %+v", drainStatus.Counts)
	}
	// Exact replay of the historical adjustment backfills the real completed
	// pin with its durable outcome (stored operation/journal IDs), then
	// activation succeeds without fabricated completion.
	replayed, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, "acct-b2b3-b2a", callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val))
	if err != nil {
		t.Fatalf("exact replay must backfill real pin: %v", err)
	}
	if replayed.Status != billing.SelectedCostTransitionReplay {
		t.Fatalf("replay status = %q, want replay", replayed.Status)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("replay must backfill completed pin with real outcome: %v", err)
	}
	if !pin.IsCompleted() || pin.Owner != billing.PostingOwnerV1 {
		t.Fatalf("backfilled pin must be V1 completed, got %#v", pin)
	}
	if pin.CompletionOperationKey != replayed.OperationKey || pin.CompletionTransactionID != replayed.TransactionID {
		t.Fatalf("backfill must reference real durable outcome: pin %#v replay %#v", pin, replayed)
	}
	if _, err := store.ActivateCutoverV2(ctx, "b2b3-b2a-activate"); err != nil {
		t.Fatalf("F4: activation after ordinary history must succeed: %v", err)
	}
	_ = status
}
