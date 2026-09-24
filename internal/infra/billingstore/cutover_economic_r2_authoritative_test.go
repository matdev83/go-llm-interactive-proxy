package billingstore

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 17.3 R2 RED: authoritative delivery-intent contract.
//
// B: explicit V2 enqueue with legacy-compatible empty payload owner must be
// consumed as V2 through list/claim/worker and post once.
// C: shadow evidence-only capture upgraded by equivalent live replay must
// become monetary (same immutable identity) and post once.
// D: stale V1 monetary upgrade after v2_active must fence with zero effects.

func TestR2ExplicitV2EmptyPayloadPostsAsV2(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r2-explicit-v2")
	ctx := f2bSetupShadowAccount(t, store, "acct-r2-explicit-v2")
	seedID := f2bMustCallID(t)
	seed := f2bProviderWork(t, store, "acct-r2-explicit-v2", seedID, "b-r2-seed", "r2-head-seed", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "r2-explicit-drain"); err != nil {
		t.Fatal(err)
	}
	worker := f2bProviderWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("seed worker: %v", err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "r2-explicit-activate"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	v2ID := f2bMustCallID(t)
	v2Work := f2bProviderWork(t, store, "acct-r2-explicit-v2", v2ID, "b-r2-v2", "r2-head-v2", 1, true)
	// Ordinary normalized work carries empty PostingOwner (legacy-compatible).
	if v2Work.PostingOwner != "" {
		t.Fatalf("fixture must carry empty payload owner, got %q", v2Work.PostingOwner)
	}
	if v2Work.EvidenceOnly {
		t.Fatalf("fixture must not be evidence-only")
	}
	if err := store.AppendProviderPostingEconomicRevisionWork(ctx, v2Work, billing.PostingOwnerV2); err != nil {
		t.Fatalf("explicit V2 enqueue: %v", err)
	}
	// Authoritative list must overlay V2 (not empty/V1).
	pending, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found *billing.EconomicRevisionWork
	for i := range pending {
		id, _ := pending[i].Identity()
		want, _ := v2Work.Identity()
		if id.Key() == want.Key() {
			found = &pending[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("explicit V2 work missing from list (pending=%d)", len(pending))
	}
	if found.PostingOwner != billing.PostingOwnerV2 {
		t.Fatalf("RED R2-B: list owner = %q, want v2 (split payload/state)", found.PostingOwner)
	}
	if found.EvidenceOnly {
		t.Fatalf("RED R2-B: authoritative work must not be evidence-only")
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("V2 worker: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-r2-explicit-v2"); n < 2 {
		t.Fatalf("RED R2-B: journals = %d, want >=2 (seed V1 + V2 once)", n)
	}
	_, _, opKey, err := billing.MonetaryEconomicPostingKey(store.StoreID(), *found)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("V2 pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 {
		t.Fatalf("pin owner = %q, want v2", pin.Owner)
	}
}

func TestR2ShadowUpgradeBecomesMonetaryAndPostsOnce(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r2-shadow-upgrade")
	ctx := f2bSetupShadowAccount(t, store, "acct-r2-shadow")
	callID := f2bMustCallID(t)
	base := f2bProviderWork(t, store, "acct-r2-shadow", callID, "b-r2-shadow", "r2-head-shadow", 1, true)
	shadow := base
	shadow.EvidenceOnly = true
	shadow.PostingOwner = ""
	shadow, err := shadow.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatal(err)
	}
	shadowID, err := shadow.Identity()
	if err != nil {
		t.Fatal(err)
	}
	// Equivalent live delivery: same immutable evidence, monetary intent.
	live := base
	live.EvidenceOnly = false
	live.PostingOwner = ""
	live, err = live.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	liveID, err := live.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if liveID.Key() != shadowID.Key() {
		t.Fatalf("evidence identity must be immutable: shadow %q != live %q", shadowID.Key(), liveID.Key())
	}
	// Live replay in same pre-active window upgrades evidence->monetary.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("live replay upgrade: %v", err)
	}
	pending, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found *billing.EconomicRevisionWork
	for i := range pending {
		id, _ := pending[i].Identity()
		if id.Key() == liveID.Key() {
			found = &pending[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("upgraded work missing from list")
	}
	if !billing.IsMonetaryEconomicRevisionWork(*found) {
		t.Fatalf("RED R2-C: authoritative list must be monetary after upgrade (EvidenceOnly=%v owner=%q)", found.EvidenceOnly, found.PostingOwner)
	}
	worker := f2bProviderWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker after upgrade: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-r2-shadow"); n != 1 {
		t.Fatalf("RED R2-C: journals = %d, want 1 (upgraded payable posted once)", n)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-r2-shadow"); n != 1 {
		t.Fatalf("second pass journals = %d, want 1 (exactly once)", n)
	}
}

func TestR2StaleV1UpgradeAfterActiveFenced(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r2-stale-v1")
	ctx := f2bSetupShadowAccount(t, store, "acct-r2-stale-v1")
	callID := f2bMustCallID(t)
	base := f2bProviderWork(t, store, "acct-r2-stale-v1", callID, "b-r2-stale", "r2-head-stale", 1, true)
	shadow := base
	shadow.EvidenceOnly = true
	shadow.PostingOwner = ""
	shadow, err := shadow.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatal(err)
	}
	// Evidence-only never blocks drain.
	if _, _, err := store.BeginCutoverDraining(ctx, "r2-stale-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "r2-stale-activate"); err != nil {
		t.Fatalf("activate with only evidence: %v", err)
	}
	// Stale V1 monetary replay after v2_active: first tx conflicts (evidence
	// exists, rolls back), retry tx reacquires F1 lock and must fence V1.
	live := base
	live.EvidenceOnly = false
	live.PostingOwner = ""
	live, err = live.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	err = store.AppendProviderPostingEconomicRevisionWork(ctx, live, billing.PostingOwnerV1)
	if !f2bIsFenceErr(err) {
		t.Fatalf("RED R2-D: stale V1 upgrade after active err = %v, want fence", err)
	}
	// Deterministic retry-transaction revalidation: the second (upgrade) tx
	// reacquires the F1 marker lock after the first tx rolled back on
	// identity conflict. A concurrent shadow->active transition between those
	// txs must not create V1 monetary state. Directly exercising the upgrade
	// tx after activation must fence V1 with zero effects.
	retryID, _ := live.Identity()
	utx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure rollback on failure; commit only on unexpected success path.
	committed := false
	defer func() {
		if !committed {
			_ = utx.Rollback()
		}
	}()
	upgradeMarker, err := store.ensureAndLockAccountingCutoverTx(ctx, utx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.upgradeEconomicStateToMonetaryTx(ctx, utx, upgradeMarker, retryID, billing.PostingOwnerV1); !f2bIsFenceErr(err) {
		t.Fatalf("RED R2-D: retry-tx V1 upgrade after active err = %v, want fence (missing revalidation)", err)
	}
	_ = utx.Rollback()
	committed = true
	// Zero monetary effects: no V1 pin, no journal, state remains evidence.
	liveID, _ := live.Identity()
	_, _, pinKey, kerr := billing.MonetaryEconomicPostingKey(store.StoreID(), live)
	if kerr == nil {
		if _, perr := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, pinKey); !errors.Is(perr, billing.ErrPostingOwnershipNotFound) {
			t.Fatalf("RED R2-D: stale V1 must create no pin (pin err = %v)", perr)
		}
	}
	_ = liveID
	worker := f2bProviderWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker after fenced upgrade: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-r2-stale-v1"); n != 0 {
		t.Fatalf("RED R2-D: journals = %d, want 0 (fenced, no money)", n)
	}
}
