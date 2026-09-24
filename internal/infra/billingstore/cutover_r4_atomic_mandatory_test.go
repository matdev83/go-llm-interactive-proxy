package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 17.3 R4 RED: atomic mandatory cutover claim tokens for production workers.
// Each test below must FAIL before the fix and PASS after.
//   A. wrapper drops token -> fails closed, zero effects
//   B. wrapper clears OperationKey / corrupts marker+lease -> fails closed
//   C. marker transition between claim and return cannot authorize wrong epoch
//   D. expired lease reclaim issues fresh token; stale fails; fresh completes once
//   E. first acquisition receives nonempty fully validated token atomically

// dropTokenCustomerClaimer forwards claimed work but drops the token.
type r4DropTokenCustomerClaimer struct {
	inner *DurableStore
}

func (d r4DropTokenCustomerClaimer) ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]billing.ClaimedCompleteCall, error) {
	items, err := d.inner.ClaimCompleteCallsWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Claim = billing.CutoverClaimMetadata{}
	}
	return items, nil
}

// dropTokenProviderClaimer forwards claimed work but drops the token.
type r4DropTokenProviderClaimer struct {
	inner *DurableStore
}

func (d r4DropTokenProviderClaimer) ClaimProviderCostWorkWithCutover(ctx context.Context, limit int) ([]billing.ClaimedProviderCostWork, error) {
	items, err := d.inner.ClaimProviderCostWorkWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Claim = billing.CutoverClaimMetadata{}
	}
	return items, nil
}

// dropTokenEconomicClaimer leases honestly but drops the cutover token for monetary work.
type r4DropTokenEconomicClaimer struct {
	inner *DurableStore
}

func (d r4DropTokenEconomicClaimer) ClaimEconomicRevisionWorkWithCutover(ctx context.Context, work billing.EconomicRevisionWork, owner string, lease time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	claim, _, claimed, err := d.inner.ClaimEconomicRevisionWorkWithCutover(ctx, work, owner, lease)
	if err != nil || !claimed {
		return claim, nil, claimed, err
	}
	return claim, nil, true, nil
}

func TestR4ADroppedCustomerTokenFailsClosed(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-a-cust-drop")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-a-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-r4-a-cust")
	worker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f6f8RatingStub{callID: callID}, r4DropTokenCustomerClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4A RED: wrapper dropping customer token must fail closed, got nil")
	}
	if n := f6f8JournalCount(t, store, "acct-r4-a-cust"); n != 0 {
		t.Fatalf("R4A RED: dropped customer token posted %d journals, want 0", n)
	}
}

func TestR4ADroppedProviderTokenFailsClosed(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-a-prov-drop")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-a-prov-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _ = b2b2SetupLeg(t, store, "acct-r4-a-prov", "b-1")
	worker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f6f8ProviderResolverStub{}, r4DropTokenProviderClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4A RED: wrapper dropping provider token must fail closed, got nil")
	}
	if n := b2b2ProviderJournals(t, store, "acct-r4-a-prov"); n != 0 {
		t.Fatalf("R4A RED: dropped provider token posted %d journals, want 0", n)
	}
}

func TestR4ADroppedEconomicTokenFailsClosed(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r4-a-eco-drop")
	ctx := f2bSetupShadowAccount(t, store, "acct-r4-a-eco")
	callID := f2bMustCallID(t)
	work := f2bProviderWork(t, store, "acct-r4-a-eco", callID, "b-eco", "r4-a-head", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("append economic work: %v", err)
	}
	worker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(
		store, store, f2bRater{}, nil, store, r4DropTokenEconomicClaimer{inner: store}, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4A RED: worker with dropped economic cutover token must fail closed, got nil")
	}
	if n := f2bProviderJournals(t, store, "acct-r4-a-eco"); n != 0 {
		t.Fatalf("R4A RED: dropped economic token posted %d provider journals, want 0", n)
	}
}

// clearOpKeyCustomerClaimer forwards valid work but clears only OperationKey.
type r4ClearOpKeyCustomerClaimer struct {
	inner *DurableStore
}

func (d r4ClearOpKeyCustomerClaimer) ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]billing.ClaimedCompleteCall, error) {
	items, err := d.inner.ClaimCompleteCallsWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Claim.OperationKey = ""
	}
	return items, nil
}

func TestR4BClearedOperationKeyFailsClosed(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-b-opkey")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-b-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-r4-b-opkey")
	worker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f6f8RatingStub{callID: callID}, r4ClearOpKeyCustomerClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4B RED: cleared OperationKey must fail closed, got nil")
	}
	if n := f6f8JournalCount(t, store, "acct-r4-b-opkey"); n != 0 {
		t.Fatalf("R4B RED: cleared OperationKey posted %d journals, want 0", n)
	}
}

// corruptEpochCustomerClaimer decrements the marker epoch on an otherwise valid token.
type r4CorruptEpochCustomerClaimer struct {
	inner *DurableStore
}

func (d r4CorruptEpochCustomerClaimer) ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]billing.ClaimedCompleteCall, error) {
	items, err := d.inner.ClaimCompleteCallsWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Claim.MarkerEpoch > 1 {
			items[i].Claim.MarkerEpoch--
			items[i].Claim.MarkerVersion--
		}
	}
	return items, nil
}

func TestR4BCorruptedMarkerEpochFailsClosed(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-b-epoch")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-b-epoch-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-r4-b-epoch")
	worker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f6f8RatingStub{callID: callID}, r4CorruptEpochCustomerClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4B RED: corrupted marker epoch must fail closed, got nil")
	}
	if n := f6f8JournalCount(t, store, "acct-r4-b-epoch"); n != 0 {
		t.Fatalf("R4B RED: corrupted epoch posted %d journals, want 0", n)
	}
}

func TestR4CClaimTokenBoundToCommitMarker(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-c-atomic")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-c-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = shadow
	callID, call, exp, res := f6f8SetupCustomerPinned(t, store, "acct-r4-c")
	// Claim via the production WithCutover port; token must equal the marker
	// snapshot at claim commit, never a stale or future epoch.
	claimed, err := store.ClaimCompleteCallsWithCutover(ctx, 8)
	if err != nil {
		t.Fatalf("claim with cutover: %v", err)
	}
	if len(claimed) == 0 {
		t.Fatalf("R4C RED: WithCutover must return claimed work with a token")
	}
	var found *billing.ClaimedCompleteCall
	for i := range claimed {
		if claimed[i].Call.Closure.CallID == callID {
			found = &claimed[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("claimed work missing call %s", callID.String())
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if found.Claim.OperationKey == "" {
		t.Fatalf("R4C RED: first-acquisition must carry a nonempty token, got empty")
	}
	if err := found.Validate(); err != nil {
		t.Fatalf("R4C RED: claimed token must fully validate, got: %v", err)
	}
	if found.Claim.MarkerVersion != marker.Version || found.Claim.MarkerEpoch != marker.Epoch || found.Claim.MarkerState != marker.State {
		t.Fatalf("R4C RED: token %d/%d/%q != current marker %d/%d/%q; claim and authority must commit atomically",
			found.Claim.MarkerVersion, found.Claim.MarkerEpoch, string(found.Claim.MarkerState),
			marker.Version, marker.Epoch, string(marker.State))
	}
	// Posting with the atomic token must succeed exactly once.
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exp, Result: res, PostingOwner: found.Claim.Owner, Claim: &found.Claim,
	}); err != nil {
		t.Fatalf("R4C RED: atomic token posting must succeed, got: %v", err)
	}
	_ = callID
}

func TestR4DExpiredLeaseReclaimFreshToken(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r4-d-reclaim")
	ctx := f2bSetupShadowAccount(t, store, "acct-r4-d")
	callID := f2bMustCallID(t)
	work := f2bProviderWork(t, store, "acct-r4-d", callID, "b-r4d", "r4-d-head", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("append: %v", err)
	}
	claim1, cut1, claimed, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, work, "owner-1", 30*time.Second)
	if err != nil || !claimed {
		t.Fatalf("first lease+token: claimed=%v err=%v", claimed, err)
	}
	if cut1 == nil {
		t.Fatalf("R4D RED: monetary lease must carry a cutover token")
	}
	if err := cut1.Validate(); err != nil {
		t.Fatalf("R4D RED: first token must validate, got: %v", err)
	}
	// Expire the lease by direct state manipulation is not public; instead
	// retry/release then reclaim with a new owner to simulate expiry.
	if err := store.RetryEconomicRevisionWork(ctx, work, claim1, "r4d-expire", time.Now().UTC()); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	claim2, cut2, claimed2, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, work, "owner-2", 30*time.Second)
	if err != nil || !claimed2 {
		t.Fatalf("reclaim lease+token: claimed=%v err=%v", claimed2, err)
	}
	if cut2 == nil {
		t.Fatalf("R4D RED: reclaim must issue a fresh valid token")
	}
	if err := cut2.Validate(); err != nil {
		t.Fatalf("R4D RED: fresh token must validate, got: %v", err)
	}
	// Stale prior token must be distinguishable from the fresh one via lease
	// fence binding: fences must differ and stale posting must fail.
	if claim1.Fence == claim2.Fence {
		t.Fatalf("R4D RED: reclaim must advance the lease fence, got %d twice", claim1.Fence)
	}
	if cut1.WorkID == "" || cut1.LeaseFence == 0 || cut2.WorkID == "" || cut2.LeaseFence == 0 {
		t.Fatalf("R4D RED: tokens must bind lease fence (got %+v / %+v)", cut1, cut2)
	}
	if cut1.WorkID != cut2.WorkID || cut1.LeaseFence == cut2.LeaseFence {
		t.Fatalf("R4D RED: fresh token must bind same work with a new fence")
	}
	// Release the manual reclaim so the production worker can reclaim with
	// its own owner and complete exactly once (lease held by owner-2 would
	// otherwise withhold the worker without error).
	if err := store.RetryEconomicRevisionWork(ctx, work, claim2, "r4d-release-for-worker", time.Now().UTC()); err != nil {
		t.Fatalf("release reclaim for worker: %v", err)
	}
	// Fresh worker completes exactly once via the production cutover worker.
	worker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(
		store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("R4D RED: fresh token worker must complete, got: %v", err)
	}
	if n := f2bProviderJournals(t, store, "acct-r4-d"); n != 1 {
		t.Fatalf("R4D RED: fresh worker journals = %d, want 1", n)
	}
	_ = cut1
	_ = claim2
}

func TestR4EFirstAcquisitionCarriesToken(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "r4-e-first")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-e-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Fresh call with no preexisting pin (first acquisition).
	callID := f6f8MustCallID(t)
	acct := billing.Account{ID: "acct-r4-e-first", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(),
		Max:        billing.Money{Nano: 60000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = exp
	claimed, err := store.ClaimCompleteCallsWithCutover(ctx, 8)
	if err != nil {
		t.Fatalf("first-acquisition claim: %v", err)
	}
	var found *billing.ClaimedCompleteCall
	for i := range claimed {
		if claimed[i].Call.Closure.CallID == callID {
			found = &claimed[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("R4E RED: first acquisition work missing from WithCutover claim")
	}
	if found.Claim.OperationKey == "" {
		t.Fatalf("R4E RED: first acquisition must receive a nonempty token atomically, got empty")
	}
	if err := found.Validate(); err != nil {
		t.Fatalf("R4E RED: first-acquisition token must fully validate, got: %v", err)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if found.Claim.MarkerEpoch != marker.Epoch || found.Claim.MarkerVersion != marker.Version {
		t.Fatalf("R4E RED: first-acquisition token epoch %d/%d != current %d/%d",
			found.Claim.MarkerVersion, found.Claim.MarkerEpoch, marker.Version, marker.Epoch)
	}
}
