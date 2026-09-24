package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// Phase 17.3 F6+F8 RED: renewable mandatory claim authority.
// Must FAIL before fix, PASS after.
//  1. epoch1 pin then shadow epoch2 correction (fresh token must be current)
//  2. pre-drain incomplete pin then draining reclaim (fresh token current, owner stable)
//  3. wrapper returning error/malformed token currently posts (must fail closed, zero effects)
//  4. canceled lookup swallowed (must fail closed, zero effects)
//  5. genuine stale lease after active (active V1 cannot renew)

func f6f8NewStore(t *testing.T, storeID string) *DurableStore {
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

func f6f8MustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func f6f8SetupCustomerPinned(t *testing.T, store *DurableStore, accountID string) (billing.BillingCallID, billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult) {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := f6f8MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = accountID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 60000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 12000, Currency: "USD"}, Fingerprint: "f6f8-fp-" + callID.String()}
	return callID, call, exp, res
}

func TestF6F8RedEpoch1PinShadowCorrection(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-shadow-renew")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Epoch != 1 {
		t.Fatalf("initial epoch = %d, want 1", m.Epoch)
	}
	callID, call, exp, res := f6f8SetupCustomerPinned(t, store, "acct-f6f8-shadow")
	// Acquire incomplete V1 pin in epoch1 via direct Acquire (pinned, not completed).
	pin, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{
		Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-f6f8-shadow", CallID: callID,
		Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: m.Version, ExpectedMarkerEpoch: m.Epoch,
	})
	if err != nil {
		t.Fatalf("acquire epoch1 pin: %v", err)
	}
	if pin.MarkerEpoch != 1 {
		t.Fatalf("pin epoch = %d, want 1", pin.MarkerEpoch)
	}
	// Advance to shadow epoch2.
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if shadow.Epoch != 2 {
		t.Fatalf("shadow epoch = %d, want 2", shadow.Epoch)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-f6f8-shadow", callID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("renewed claim must be issuable in shadow for V1 pinned work, got err: %v", err)
	}
	// F6: renewable authority must be current-marker, owner stable.
	if meta.MarkerEpoch != shadow.Epoch || meta.MarkerVersion != shadow.Version {
		t.Fatalf("RED F6: renewed claim epoch %d/%d != current %d/%d (pin epoch %d); acquisition epoch is not a forever lease",
			meta.MarkerVersion, meta.MarkerEpoch, shadow.Version, shadow.Epoch, pin.MarkerEpoch)
	}
	if meta.Owner != billing.PostingOwnerV1 {
		t.Fatalf("renewed owner = %q, want V1 stable", meta.Owner)
	}
	// Posting with fresh renewed token must succeed in shadow without rewriting owner.
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exp, Result: res, PostingOwner: meta.Owner, Claim: &meta,
	})
	if err != nil {
		t.Fatalf("RED F6: shadow correction with renewed token must post, got: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first shadow posting must not be replayed")
	}
}

func TestF6F8RedPredrainIncompleteDrainingReclaim(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-drain-renew")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-to-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	callID, call, exp, res := f6f8SetupCustomerPinned(t, store, "acct-f6f8-drain")
	pin, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{
		Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-f6f8-drain", CallID: callID,
		Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: shadow.Version, ExpectedMarkerEpoch: shadow.Epoch,
	})
	if err != nil {
		t.Fatalf("acquire shadow pin: %v", err)
	}
	_ = pin
	// Direct transition to draining epoch3 WITHOUT classification refresh (pin stays epoch2).
	draining, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: shadow.Version, ExpectedEpoch: shadow.Epoch,
		NextState: billing.AccountingCutoverV1Draining, TransitionID: "f6f8-to-drain",
	})
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-f6f8-drain", callID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("draining reclaim must be issuable for classified/pre-pinned V1, got: %v", err)
	}
	if meta.MarkerEpoch != draining.Epoch {
		t.Fatalf("RED F6: draining renewed claim epoch %d != current %d (pin preserved %d); must renew without rewriting owner",
			meta.MarkerEpoch, draining.Epoch, pin.MarkerEpoch)
	}
	if meta.Owner != billing.PostingOwnerV1 {
		t.Fatalf("reclaim owner = %q, want stable V1", meta.Owner)
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exp, Result: res, PostingOwner: meta.Owner, Claim: &meta,
	})
	if err != nil {
		t.Fatalf("RED F6: draining reclaim with renewed token must post, got: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first draining posting must not be replayed")
	}
	// Owner must remain stable V1.
	gotPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if gotPin.Owner != billing.PostingOwnerV1 {
		t.Fatalf("pin owner after reclaim = %q, want stable V1", gotPin.Owner)
	}
}

// errClaimProvider simulates a durable decorator that implements the claim port
// but returns an operational error. Production workers must fail closed.
type f6f8ErrClaimProvider struct{ err error }

func (f f6f8ErrClaimProvider) GetCutoverClaimMetadata(context.Context, billing.PostingOperationKind, string) (billing.CutoverClaimMetadata, error) {
	return billing.CutoverClaimMetadata{}, f.err
}

type f6f8BadClaimProvider struct{ meta billing.CutoverClaimMetadata }

func (f f6f8BadClaimProvider) GetCutoverClaimMetadata(context.Context, billing.PostingOperationKind, string) (billing.CutoverClaimMetadata, error) {
	return f.meta, nil
}

type f6f8RatingStub struct{ callID billing.BillingCallID }

func (s f6f8RatingStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	// Dynamic: always rate the claimed call so multi-call tests do not mismatch.
	callID := complete.Closure.CallID
	if callID.String() == "" {
		callID = s.callID
	}
	return billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 5000, Currency: "USD"}, Fingerprint: "f6f8-worker-fp-" + callID.String()}, nil
}

func f6f8JournalCount(t *testing.T, store *DurableStore, accountID string) int {
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

func TestF6F8RedWrapperErrorMalformedPosts(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-wrapper")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-wrap-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-f6f8-wrap")
	// Error wrapper must fail closed with zero effects.
	errWorker, err := billing.NewCallPostUsageWorkerWithClaim(store, store, f6f8RatingStub{callID: callID}, f6f8ErrClaimProvider{err: errors.New("durable claim store unavailable")}, 8)
	if err != nil {
		t.Fatalf("worker compose with err provider: %v", err)
	}
	if err := errWorker.ProcessOnce(ctx); err == nil {
		t.Fatalf("RED F8: worker with erroring claim port must fail closed, got nil error")
	}
	if n := f6f8JournalCount(t, store, "acct-f6f8-wrap"); n != 0 {
		t.Fatalf("RED F8: erroring claim wrapper posted %d journals, want 0 (never swallow into nil/default owner)", n)
	}
	// Malformed token on an independent call (avoids retry backoff coupling).
	// Tampered key fails Validate; old code swallows validation failure into
	// empty and posts without a claim (false positive in shadow). New code
	// fails closed with zero effects.
	callID2, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-f6f8-wrap-mal")
	malformed := billing.CutoverClaimMetadata{Kind: billing.PostingOperationCustomerSettlement, OperationKey: "tampered-key", AccountID: "acct-f6f8-wrap-mal", CallID: callID2, Owner: billing.PostingOwnerV1, MarkerVersion: 2, MarkerEpoch: 2, MarkerState: billing.AccountingCutoverV2Shadow}
	malWorker, err := billing.NewCallPostUsageWorkerWithClaim(store, store, f6f8RatingStub{callID: callID2}, f6f8BadClaimProvider{meta: malformed}, 8)
	if err != nil {
		t.Fatalf("worker compose with malformed: %v", err)
	}
	if err := malWorker.ProcessOnce(ctx); err == nil {
		t.Fatalf("RED F8: worker with malformed token must fail closed, got nil")
	}
	if n := f6f8JournalCount(t, store, "acct-f6f8-wrap-mal"); n != 0 {
		t.Fatalf("RED F8: malformed token posted %d journals, want 0", n)
	}
	if n := f6f8JournalCount(t, store, "acct-f6f8-wrap"); n != 0 {
		t.Fatalf("RED F8: error path leaked %d journals, want 0", n)
	}
	_ = time.Now
}

func TestF6F8RedCanceledLookupSwallowed(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-canceled")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-cancel-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, _, _, _ := f6f8SetupCustomerPinned(t, store, "acct-f6f8-cancel")
	canceledProvider := f6f8ErrClaimProvider{err: context.Canceled}
	worker, err := billing.NewCallPostUsageWorkerWithClaim(store, store, f6f8RatingStub{callID: callID}, canceledProvider, 8)
	if err != nil {
		t.Fatalf("worker compose: %v", err)
	}
	if err := worker.ProcessOnce(ctx); err == nil {
		t.Fatalf("RED F8: canceled claim lookup must fail closed, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("RED F8: canceled lookup err = %v, want context.Canceled (never swallow cancellation)", err)
	}
	if n := f6f8JournalCount(t, store, "acct-f6f8-cancel"); n != 0 {
		t.Fatalf("RED F8: canceled lookup posted %d journals, want 0", n)
	}
}

func TestF6F8RedStaleAfterActive(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-stale-active")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-stale-shadow",
	})
	if err != nil {
		// Already shadow? reload.
		cur, rerr := store.GetAccountingCutover(ctx)
		if rerr != nil {
			t.Fatal(err)
		}
		shadow = cur
	}
	_ = shadow
	// Complete a real V1 customer posting in shadow so drain can activate
	// (completed pins do not block activation; pinned would).
	callID, call, exp, res := f6f8SetupCustomerPinned(t, store, "acct-f6f8-stale")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err != nil {
		t.Fatalf("shadow V1 complete: %v", err)
	}
	// Complete pending provider work so providerPending==0 and v1Pinned==0.
	// Otherwise Activate blocks (correct drain inventory).
	if pending, err := store.ListPendingProviderCostWork(ctx, 8); err != nil {
		t.Fatalf("list pending provider: %v", err)
	} else {
		for _, w := range pending {
			sealed, serr := w.Leg.Seal()
			if serr != nil {
				t.Fatalf("seal pending leg: %v", serr)
			}
			pres := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
			if _, perr := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: w.AccountID, CallID: w.CallID, Leg: w.Leg, Result: pres}); perr != nil {
				t.Fatalf("complete provider pending: %v", perr)
			}
		}
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-f6f8-stale", callID)
	if err != nil {
		t.Fatal(err)
	}
	staleMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("pre-drain claim: %v", err)
	}
	// Drain then activate (no pending/incomplete => ready).
	draining, _, err := store.BeginCutoverDraining(ctx, "f6f8-stale-drain")
	if err != nil {
		t.Fatalf("begin draining: %v", err)
	}
	_ = draining
	active, err := store.ActivateCutoverV2(ctx, "f6f8-stale-activate")
	if err != nil {
		t.Fatalf("activate (completed drain must succeed): %v", err)
	}
	_ = active
	// Genuine stale lease (shadow/draining-epoch token) for NEW money must fence in active.
	// Use a conflicting fingerprint so it is new money, not exact replay.
	conflictRes := res
	conflictRes.Fingerprint = res.Fingerprint + ":stale-conflict"
	conflictRes.CustomerCharge.Nano++
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exp, Result: conflictRes,
		PostingOwner: staleMeta.Owner, Claim: &staleMeta,
	}); err == nil {
		t.Fatalf("RED: stale draining token in active must fence new money, got nil")
	} else if !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrPostingOwnershipConflict) && !errors.Is(err, billing.ErrCutoverV1Fenced) && !errors.Is(err, billing.ErrAccountingCutoverFence) && !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("stale active err = %v, want fence/conflict", err)
	}
	// Active V1 must not receive renewal.
	if _, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey); err == nil {
		t.Fatalf("RED F6+F8: active V1 must not receive renewal (V1 receives none), got token")
	}
}

func TestF6F8ProviderStaleFailsFreshSucceedsViaWorker(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-prov-renew")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		m, err = store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-prov-shadow",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	callID, leg, result := b2b2SetupLeg(t, store, "acct-f6f8-prov", "b-1")
	if _, _, err := store.BeginCutoverDraining(ctx, "f6f8-prov-drain"); err != nil {
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
	fresh, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("draining provider renewal must succeed, got: %v", err)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.MarkerEpoch != marker.Epoch || fresh.Owner != billing.PostingOwnerV1 {
		t.Fatalf("provider fresh token epoch %d owner %q, want current %d V1", fresh.MarkerEpoch, fresh.Owner, marker.Epoch)
	}
	// Stale (rewound epoch) must fence with zero effects.
	stale := fresh
	if stale.MarkerEpoch > 1 {
		stale.MarkerEpoch--
		stale.MarkerVersion--
	}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-f6f8-prov", CallID: callID, Leg: leg, Result: result, PostingOwner: stale.Owner, Claim: &stale}); err == nil {
		t.Fatalf("stale provider token must fence, got nil")
	}
	if n := b2b2ProviderJournals(t, store, "acct-f6f8-prov"); n != 0 {
		t.Fatalf("stale provider posted %d journals, want 0", n)
	}
	// Fresh via actual worker with cutover claimer must succeed in draining.
	// Use a resolver matching the setup amount (11) so the worker posts the
	// same fingerprint the direct proof below expects; f2aProviderStub uses 7
	// and would conflict.
	worker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f6f8ProviderResolverStub{}, store, 8)
	if err != nil {
		t.Fatalf("provider cutover worker compose: %v", err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider cutover worker must succeed in draining, got: %v", err)
	}
	if n := b2b2ProviderJournals(t, store, "acct-f6f8-prov"); n != 1 {
		t.Fatalf("provider worker journals = %d, want 1", n)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("provider pin after renewal = %#v, want V1 completed stable owner", pin)
	}
}

func TestF6F8EconomicMonetaryRenewalViaCutoverWorker(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "f6f8-eco-renew")
	ctx := f2bSetupShadowAccount(t, store, "acct-f6f8-eco")
	callID := f2bMustCallID(t)
	work := f2bProviderWork(t, store, "acct-f6f8-eco", callID, "b-eco", "f6f8-eco-head", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("append economic work: %v", err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f6f8-eco-drain"); err != nil {
		t.Fatal(err)
	}
	// Atomic lease+token must carry current-epoch renewal.
	_, cutover, claimed, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, work, "f6f8-eco-owner", 30*time.Second)
	if err != nil {
		t.Fatalf("economic cutover claim must succeed, got: %v", err)
	}
	if !claimed {
		t.Fatalf("economic monetary work must be claimable in draining")
	}
	if cutover == nil {
		t.Fatalf("monetary economic lease must carry a cutover token")
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cutover.MarkerEpoch != marker.Epoch || cutover.Owner != billing.PostingOwnerV1 {
		t.Fatalf("economic cutover epoch %d owner %q, want current %d V1", cutover.MarkerEpoch, cutover.Owner, marker.Epoch)
	}
	// Release the probe lease so the production worker can reclaim and post.
	// Use the work claim fence from the probe above via retry (no money yet).
	// The worker below reclaims atomically with its own owner.
	// Production worker with cutover must complete without direct SQL completion.
	worker := f2bProviderWorker(t, store)
	// f2bProviderWorker uses old WithClaim; for cutover path use new constructor.
	workerCut, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(
		store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatalf("economic cutover worker compose: %v", err)
	}
	_ = worker
	if err := workerCut.ProcessOnce(ctx); err != nil {
		t.Fatalf("economic cutover worker must complete draining monetary work, got: %v", err)
	}
	// Evidence-only must carry nil cutover and remain operable.
	custWork := f2bCustomerWork(t, store, "acct-f6f8-eco", f2bMustCallID(t), "b-ev", "f6f8-ev-head", 1)
	if err := store.AppendEconomicRevisionWork(ctx, custWork); err != nil {
		t.Fatalf("append evidence work: %v", err)
	}
	_, evCut, evClaimed, err := store.ClaimEconomicRevisionWorkWithCutover(ctx, custWork, "f6f8-ev-owner", 30*time.Second)
	if err != nil {
		t.Fatalf("evidence claim: %v", err)
	}
	if !evClaimed || evCut != nil {
		t.Fatalf("evidence-only must claim without cutover, got claimed=%v cutover=%v", evClaimed, evCut)
	}
}

func TestF6F8MissingTokenFailsDraining(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-missing")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-miss-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, call, exp, res := f6f8SetupCustomerPinned(t, store, "acct-f6f8-miss")
	if _, _, err := store.BeginCutoverDraining(ctx, "f6f8-miss-drain"); err != nil {
		t.Fatal(err)
	}
	// Pinned draining posting without the required token must fence, zero effects.
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res}); err == nil {
		t.Fatalf("missing token in draining must fence, got nil")
	}
	if n := f6f8JournalCount(t, store, "acct-f6f8-miss"); n != 0 {
		t.Fatalf("missing token posted %d journals, want 0", n)
	}
}

func TestF6F8TestOnlyFakesRemainUsable(t *testing.T) {
	t.Parallel()
	// Legacy test-only constructors with pure in-memory doubles (no cutover
	// ports) must remain usable; they are explicitly not production.
	callID := f6f8MustCallID(t)
	closure := billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-fake", ALegID: "a-1", SessionID: "s-1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "p", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "c", Version: "v1"}, ExpectedBLegIDs: []string{"b-1"}}
	sealed, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}
	usage := &fakeF6F8Usage{claims: []billing.CompleteCall{{Closure: sealed}}, exposure: billing.CallExposure{AccountID: sealed.AccountID, CallID: sealed.CallID.String(), Max: billing.Money{Nano: 60, Currency: "USD"}, Status: billing.ExposureOpen}}
	settlement := &fakeF6F8Settlement{}
	worker, err := billing.NewCallPostUsageWorker(usage, settlement, f6f8RatingStub{callID: callID}, 8)
	if err != nil {
		t.Fatalf("legacy test-only worker compose: %v", err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("legacy fake worker must remain usable, got: %v", err)
	}
	if len(settlement.got) != 1 || settlement.got[0].Claim != nil {
		t.Fatalf("legacy fake must post without claim, got %#v", settlement.got)
	}
}

type fakeF6F8Usage struct {
	billing.CallUsageStore
	claims   []billing.CompleteCall
	exposure billing.CallExposure
}

func (f *fakeF6F8Usage) ClaimCompleteCalls(context.Context, int) ([]billing.CompleteCall, error) {
	return f.claims, nil
}

func (f *fakeF6F8Usage) GetCallExposure(context.Context, billing.BillingCallID) (billing.CallExposure, error) {
	return f.exposure, nil
}

func (f *fakeF6F8Usage) RetryCompleteCall(context.Context, billing.BillingCallID, string) error {
	return nil
}

type fakeF6F8Settlement struct {
	got []billing.ApplyCallBillingInput
}

func (f *fakeF6F8Settlement) ApplyCallBillingResult(_ context.Context, in billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	f.got = append(f.got, in)
	return billing.CallSettlement{CallID: in.Call.CallID}, nil
}

type f6f8ProviderResolverStub struct{}

func (f6f8ProviderResolverStub) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

func TestF6F8ReopenPreservesRenewal(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "f6f8-reopen.db")) + "?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
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
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "f6f8-reopen"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-ro-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = shadow
	acct := billing.Account{ID: "acct-f6f8-ro", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := f6f8MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f6f8-ro-drain"); err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("pre-reopen renewal: %v", err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 12000, Currency: "USD"}, Fingerprint: "f6f8-ro-fp-" + callID.String()}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: fresh.Owner, Claim: &fresh}); err != nil {
		t.Fatalf("pre-reopen posting: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Same-file reopen.
	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB2.SetMaxOpenConns(4)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		_ = sqlDB2.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB2)
	reopened, err := NewDurableStore(ctx, bunDB2, Config{StoreID: "f6f8-reopen"})
	if err != nil {
		_ = bunDB2.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	pin, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("reopen pin: %v", err)
	}
	if !pin.IsCompleted() || pin.Owner != billing.PostingOwnerV1 {
		t.Fatalf("reopen pin = %#v, want V1 completed", pin)
	}
	replayed, err := reopened.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: pin.Owner})
	if err != nil {
		t.Fatalf("reopen exact replay without live lease must succeed (immutable outcome proves replay), got: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("reopen replay must be marked replayed")
	}
}

func TestF6F8SyncAdjustmentUsesMarkerLockNoLease(t *testing.T) {
	t.Parallel()
	store := f6f8NewStore(t, "f6f8-sync")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-sync-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	acct := billing.Account{ID: "acct-f6f8-sync", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	// Synchronous direct adjustment uses the current marker lock directly and
	// needs no lease token.
	posted, err := store.PostAdjustment(ctx, billing.AdjustmentInput{
		AccountID: acct.ID, Amount: billing.Money{Nano: 1000, Currency: "USD"},
		Direction: billing.AdjustmentCredit, SourceKey: "f6f8-sync-src", Reason: "f6f8 sync no lease",
	})
	if err != nil {
		t.Fatalf("sync adjustment without lease token must succeed in shadow, got: %v", err)
	}
	if posted.Replayed {
		t.Fatalf("first sync adjustment must not be replayed")
	}
	replayed, err := store.PostAdjustment(ctx, billing.AdjustmentInput{
		AccountID: acct.ID, Amount: billing.Money{Nano: 1000, Currency: "USD"},
		Direction: billing.AdjustmentCredit, SourceKey: "f6f8-sync-src", Reason: "f6f8 sync no lease",
	})
	if err != nil {
		t.Fatalf("sync exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("sync replay must be marked replayed")
	}
}
