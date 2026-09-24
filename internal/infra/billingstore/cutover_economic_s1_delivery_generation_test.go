package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 17.3 S1 RED: evidence->monetary delivery-generation transition.
//
// A. Sequential: shadow evidence-only completes no-post; equivalent live
// monetary upgrade must requeue; worker posts payable once; queue completes;
// pin completes; drain ready.
// B. Barrier: evidence lease acquired, upgrade, old lease cannot retire new
// generation; fresh claim posts once.
// C. Barrier: pending-list returns evidence but claim after upgrade must use
// authoritative monetary envelope/token; same worker posts once, no stranded pin.
// D. Repeated equivalent live upgrades idempotent, no reopen/double-post.

func s1CutoverWorker(t *testing.T, store *DurableStore) *billing.EconomicRevisionWorker {
	t.Helper()
	w, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(
		store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatalf("s1 cutover worker compose: %v", err)
	}
	return w
}

func s1PureWorker(t *testing.T, store *DurableStore) *billing.EconomicRevisionWorker {
	t.Helper()
	return f2bPureProviderWorker(t, store)
}

func s1ShadowAndLive(t *testing.T, store *DurableStore, accountID string, headKey string, revision uint64) (shadow billing.EconomicRevisionWork, live billing.EconomicRevisionWork) {
	t.Helper()
	callID := f2bMustCallID(t)
	base := f2bProviderWork(t, store, accountID, callID, "b-s1-"+headKey, headKey, revision, true)
	shadow = base
	shadow.EvidenceOnly = true
	shadow.PostingOwner = ""
	var err error
	shadow, err = shadow.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	live = base
	live.EvidenceOnly = false
	live.PostingOwner = ""
	live, err = live.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := shadow.Identity()
	if err != nil {
		t.Fatal(err)
	}
	lid, err := live.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if sid.Key() != lid.Key() {
		t.Fatalf("s1 evidence identity must be immutable: shadow %q != live %q", sid.Key(), lid.Key())
	}
	return shadow, live
}

func TestS1ASequentialUpgradeAfterEvidenceCompletePostsOnce(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "s1a-seq")
	ctx := f2bSetupShadowAccount(t, store, "acct-s1a-seq")
	shadow, live := s1ShadowAndLive(t, store, "acct-s1a-seq", "s1a-head", 1)
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatalf("append shadow evidence: %v", err)
	}
	// Evidence-only worker completes valuation with no payable.
	pure := s1PureWorker(t, store)
	if err := pure.ProcessOnce(ctx); err != nil {
		t.Fatalf("evidence worker: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1a-seq"); n != 0 {
		t.Fatalf("S1A RED: evidence journals = %d, want 0 (no-post shadow)", n)
	}
	// Equivalent authorized live monetary replay upgrades delivery.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("live monetary upgrade: %v", err)
	}
	worker := s1CutoverWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("monetary worker after upgrade: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1a-seq"); n != 1 {
		t.Fatalf("S1A RED: journals = %d, want 1 (upgraded payable posted once, not dropped)", n)
	}
	// Queue completed: no pending for this head.
	pending, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	liveID, _ := live.Identity()
	for i := range pending {
		id, _ := pending[i].Identity()
		if id.Key() == liveID.Key() {
			t.Fatalf("S1A RED: upgraded work still pending after worker completion")
		}
	}
	// Pin completed.
	_, _, opKey, err := billing.MonetaryEconomicPostingKey(store.StoreID(), live)
	if err != nil {
		// Live payload is legacy-empty owner; derive via authoritative overlay.
		monetary := billing.OverlayAuthoritativeEconomicWork(live, true, billing.PostingOwnerV1)
		_, _, opKey, err = billing.MonetaryEconomicPostingKey(store.StoreID(), monetary)
		if err != nil {
			t.Fatalf("posting key: %v", err)
		}
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("S1A RED: monetary pin missing after upgrade+worker: %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("S1A RED: pin must be completed after worker posting, got %#v", pin)
	}
	// Exactly once: second pass posts nothing.
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1a-seq"); n != 1 {
		t.Fatalf("S1A RED: second pass journals = %d, want 1 (exactly once)", n)
	}
	// Drain ready: no pending monetary blocks activation.
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.EconomicProviderPending != 0 {
		t.Fatalf("S1A RED: drain still counts %d pending monetary after completion", status.Counts.EconomicProviderPending)
	}
}

func TestS1BOldEvidenceLeaseCannotRetireUpgradedGeneration(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "s1b-lease")
	ctx := f2bSetupShadowAccount(t, store, "acct-s1b-lease")
	shadow, live := s1ShadowAndLive(t, store, "acct-s1b-lease", "s1b-head", 1)
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatalf("append shadow: %v", err)
	}
	// Pause evidence worker after lease acquired.
	oldClaim, oldCutover, oldClaimed, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, shadow, "s1b-old-worker", time.Minute)
	if err != nil || !oldClaimed {
		t.Fatalf("evidence lease acquire: claimed=%v err=%v", oldClaimed, err)
	}
	if oldCutover != nil {
		t.Fatalf("evidence lease must carry nil cutover (no monetary authority), got %+v", oldCutover)
	}
	// Upgrade to monetary while old lease held.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("live upgrade during lease: %v", err)
	}
	// Resume old worker: it must NOT retire the new monetary generation.
	// Old lease completed with its stale fence must fail closed.
	if err := store.CompleteEconomicRevisionWork(ctx, shadow, oldClaim); !errors.Is(err, billing.ErrEconomicRevisionClaimLost) {
		t.Fatalf("S1B RED: old evidence lease complete err = %v, want ClaimLost (fenced, payable must not be lost)", err)
	}
	// Old lease retry must also fail closed.
	if err := store.RetryEconomicRevisionWork(ctx, shadow, oldClaim, "s1b-stale-retry", time.Now().UTC()); !errors.Is(err, billing.ErrEconomicRevisionClaimLost) {
		t.Fatalf("S1B RED: old evidence lease retry err = %v, want ClaimLost", err)
	}
	// Fresh claim posts once and completes.
	worker := s1CutoverWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("fresh worker after fenced lease: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1b-lease"); n != 1 {
		t.Fatalf("S1B RED: journals = %d, want 1 (fresh monetary posts once)", n)
	}
	_ = oldClaim
}

// s1StaleListReader returns a stale pre-upgrade evidence envelope from List
// while delegating all lease/complete state to the real store. It models a
// worker that listed before upgrade and claims after.
type s1StaleListReader struct {
	*DurableStore
	stale []billing.EconomicRevisionWork
}

func (r *s1StaleListReader) ListPendingEconomicRevisionWork(_ context.Context, queue billing.EconomicQueue, limit int) ([]billing.EconomicRevisionWork, error) {
	out := make([]billing.EconomicRevisionWork, 0, len(r.stale))
	for _, w := range r.stale {
		if w.Queue == queue {
			out = append(out, w)
		}
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func TestS1CStaleListClaimUsesAuthoritativeMonetary(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "s1c-stale")
	ctx := f2bSetupShadowAccount(t, store, "acct-s1c-stale")
	shadow, live := s1ShadowAndLive(t, store, "acct-s1c-stale", "s1c-head", 1)
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatalf("append shadow: %v", err)
	}
	// Worker lists before upgrade: evidence-only envelope.
	staleList, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(staleList) == 0 {
		t.Fatalf("stale list must return evidence work")
	}
	liveID, _ := live.Identity()
	var stale billing.EconomicRevisionWork
	found := false
	for i := range staleList {
		id, _ := staleList[i].Identity()
		if id.Key() == liveID.Key() {
			stale = staleList[i]
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stale evidence work missing from list")
	}
	if !stale.EvidenceOnly && billing.IsMonetaryEconomicRevisionWork(stale) {
		t.Fatalf("stale list must be evidence-only before upgrade")
	}
	// Upgrade before atomic claim.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("live upgrade: %v", err)
	}
	// Same worker claims with its stale envelope object: atomic claim must
	// observe current monetary intent and issue a monetary pin/token even
	// though the input envelope is evidence-only.
	claim, cutover, claimed, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, stale, "s1c-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("S1C RED: post-upgrade claim with stale envelope: claimed=%v err=%v (must claim monetary)", claimed, err)
	}
	if cutover == nil {
		t.Fatalf("S1C RED: post-upgrade claim must return monetary token, got nil (stale envelope must not withhold money)")
	}
	if err := cutover.Validate(); err != nil {
		t.Fatalf("S1C token must validate: %v", err)
	}
	// Release the probe claim so the stale-list worker below can reclaim.
	if err := store.RetryEconomicRevisionWork(ctx, stale, claim, "s1c-probe-release", time.Now().UTC()); err != nil {
		t.Fatalf("probe release: %v", err)
	}
	// Same worker (stale list) must post once via authoritative intent rather
	// than skipping money and stranding the new pin.
	staleReader := &s1StaleListReader{DurableStore: store, stale: []billing.EconomicRevisionWork{stale}}
	worker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(
		staleReader, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	_ = context.Background()
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("S1C RED: stale-list worker must post via authoritative claim, got: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1c-stale"); n != 1 {
		t.Fatalf("S1C RED: journals = %d, want 1 (stale worker posts once, no stranded pin)", n)
	}
	// No stranded pin: monetary pin completed, drain not blocked.
	monetary := billing.OverlayAuthoritativeEconomicWork(live, true, billing.PostingOwnerV1)
	_, _, opKey, err := billing.MonetaryEconomicPostingKey(store.StoreID(), monetary)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("S1C RED: monetary pin missing (stranded?): %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("S1C RED: pin must be completed, got %#v (stranded pin blocks drain)", pin)
	}
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.EconomicProviderPending != 0 {
		t.Fatalf("S1C RED: drain pending = %d, want 0 (no stranded work)", status.Counts.EconomicProviderPending)
	}
}

func TestS1DRepeatedUpgradesIdempotent(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "s1d-idem")
	ctx := f2bSetupShadowAccount(t, store, "acct-s1d-idem")
	shadow, live := s1ShadowAndLive(t, store, "acct-s1d-idem", "s1d-head", 1)
	if err := store.AppendEvidenceEconomicRevisionWork(ctx, shadow); err != nil {
		t.Fatalf("append shadow: %v", err)
	}
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	// Repeated equivalent live delivery must be idempotent.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("S1D RED: second equivalent upgrade err = %v, want nil idempotent", err)
	}
	if err := store.AppendProviderPostingEconomicRevisionWork(ctx, live, billing.PostingOwnerV1); err != nil {
		t.Fatalf("S1D RED: explicit repeat upgrade err = %v, want nil idempotent", err)
	}
	worker := s1CutoverWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1d-idem"); n != 1 {
		t.Fatalf("S1D RED: journals = %d, want 1 (exactly once despite repeats)", n)
	}
	// Repeat after monetary completion must not reopen or double-post.
	if err := store.AppendEconomicRevisionWork(ctx, live); err != nil {
		t.Fatalf("S1D RED: post-completion repeat err = %v, want nil (no reopen)", err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-s1d-idem"); n != 1 {
		t.Fatalf("S1D RED: second pass journals = %d, want 1 (no double-post on repeat)", n)
	}
}
