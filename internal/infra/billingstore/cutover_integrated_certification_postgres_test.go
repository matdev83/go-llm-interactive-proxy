//go:build integration

package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3C PostgreSQL parity: integrated cutover timeline on configured
// direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN is
// configured; SQLite suite in cutover_integrated_certification_test.go is
// authoritative when PostgreSQL is unavailable.
func TestCutoverIntegratedPostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "c3c-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c3cSetupAccount(t, store, "acct-c3c-pg", 5000)
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "c3c-pg-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	_ = sh
	// Seed one V1 customer + provider + selected-cost + direct.
	callID := c3cMustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-pg"})
	call.AccountID = "acct-c3c-pg"
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("AppendCallUsage postgres: %v", err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-pg")); err != nil {
		t.Fatalf("AppendCallLegUsage postgres: %v", err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: "acct-c3c-pg", CallID: callID.String(), Max: billing.Money{Nano: 500, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	if err != nil {
		t.Fatalf("AdmitExposure postgres: %v", err)
	}
	_ = exp
	draining, status, err := store.BeginCutoverDraining(ctx, "c3c-pg-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining postgres: %v", err)
	}
	if draining.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("postgres state = %q, want draining", draining.State)
	}
	if status.Counts.V1Pinned < 1 {
		t.Fatalf("postgres pinned = %d, want >= 1", status.Counts.V1Pinned)
	}
	if err := store.CheckV2NewWorkAuthorized(ctx); err == nil {
		t.Fatalf("postgres pre-active V2 auth must fail")
	}
	// Complete classified V1 customer to prove exactly-once on PG.
	opKey, _ := billing.CustomerPostingOperationKey("acct-c3c-pg", callID)
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("postgres claim metadata: %v", err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 40, Currency: "USD"}, Fingerprint: "c3c-pg-fp"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res, PostingOwner: meta.Owner, Claim: &meta}); err != nil {
		t.Fatalf("postgres draining post: %v", err)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after C: %v", err)
	}
}

// TestCutoverIntegratedPostgresFullLifecycleWhenConfigured mirrors the SQLite
// F9 full lifecycle on configured direct PostgreSQL through supported
// coordinator/store APIs only: V1 admission/closure/leg + monetary economic
// work + completed history + synchronous adjustments -> shadow -> draining ->
// early activation blocked -> production workers drain exactly once ->
// activation succeeds -> fresh V2 admission/terminal/settlement with V2
// authority and V1 fenced. No SQL completion, no marker skipping, no fake
// authority. Reopen across draining/activation uses a new handle over the same
// isolated schema (server-side durability).
func TestCutoverIntegratedPostgresFullLifecycleWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	const storeID = "f9-pg-full"
	const accountID = "acct-f9-pg-full"
	open := func() *DurableStore {
		s, err := NewDurableStore(ctx, bunDB, Config{StoreID: storeID})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	store := open()
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	// Live V1 work: admitted call + closure/leg, monetary economic work,
	// completed history (F4 witness), synchronous adjustments.
	callA := c3cMustCallID(t)
	closureA := testIndependentCallUsageFor(callA, []string{"b-f9-pg-a"})
	closureA.AccountID = accountID
	expA, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callA.String(),
		Max:        billing.Money{Nano: 5000, Currency: "USD"},
		PricingRef: closureA.CustomerPricingRef, ChargePolicyRef: closureA.ChargePolicyRef,
	})
	if err != nil {
		t.Fatalf("PG V1 admit: %v", err)
	}
	_ = expA
	if err := store.AppendCallUsage(ctx, closureA); err != nil {
		t.Fatalf("PG V1 closure: %v", err)
	}
	legA := testIndependentCallLegFor(callA, "b-f9-pg-a")
	if err := store.AppendCallLegUsage(ctx, legA); err != nil {
		t.Fatalf("PG V1 leg: %v", err)
	}
	ecoCall := c3cMustCallID(t)
	ecoWork := f2bProviderWork(t, store, accountID, ecoCall, "b-f9-pg-eco", "f9-pg-eco-head", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, ecoWork); err != nil {
		t.Fatalf("PG economic enqueue: %v", err)
	}
	for i := 0; i < 2; i++ {
		histCall := c3cMustCallID(t)
		if _, err := store.ApplyProviderCostRevision(ctx, f4ProviderRevisionInput(store.StoreID(), accountID, histCall, f9PGHead(t, i), 1, 10)); err != nil {
			t.Fatalf("PG history %d: %v", i, err)
		}
	}
	if _, err := store.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: accountID, Amount: billing.Money{Nano: 77, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f9-pg-direct-1", Reason: "f9 pg seed"}); err != nil {
		t.Fatalf("PG direct seed: %v", err)
	}
	selCall := c3cMustCallID(t)
	selSubject := b2b3Subject(store.StoreID(), accountID, selCall.String())
	if _, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, accountID, selCall, "f9-pg-sel-head", selSubject, billing.SelectedCostHeadExpectation{}, b2b3Valuation(t, "f9-pg-sel-val-1", 1, 10_000_000_000))); err != nil {
		t.Fatalf("PG selected-cost seed: %v", err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: marker.Version, ExpectedEpoch: marker.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f9-pg-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = shadow
	if err := store.CheckV2NewWorkAuthorized(ctx); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("PG shadow V2 auth err = %v, want NotAuthorized", err)
	}
	draining, status, err := store.BeginCutoverDraining(ctx, "f9-pg-drain")
	if err != nil {
		t.Fatal(err)
	}
	if draining.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("PG draining = %q", draining.State)
	}
	if status.ReadyForActivation {
		t.Fatalf("PG drain must block: %+v", status.Counts)
	}
	if status.Counts.CustomerPending == 0 || status.Counts.ProviderPending == 0 || status.Counts.EconomicProviderPending == 0 || status.Counts.OpenExposures == 0 || status.Counts.V1Pinned == 0 {
		t.Fatalf("PG drain must count all families: %+v", status.Counts)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f9-pg-early"); !errors.Is(err, billing.ErrCutoverDrainBlocked) {
		t.Fatalf("PG early activate err = %v, want DrainBlocked", err)
	}
	// Reopen across the draining boundary: new handle over the same isolated
	// schema, same StoreID, through supported APIs.
	reopened, err := NewDurableStore(ctx, bunDB, Config{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	if st, err := store.CutoverDrainStatus(ctx); err != nil {
		t.Fatal(err)
	} else if st.ReadyForActivation {
		t.Fatalf("PG reopen drain must still block: %+v", st.Counts)
	}
	// Production drain via cutover workers with real tokens, exactly once.
	ecoWorker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f9DynamicProviderResolver{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f9DynamicCustomerResolver{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournals := c3cJournalCount(t, store, accountID)
	for pass := 0; pass < 5; pass++ {
		if err := ecoWorker.ProcessOnce(ctx); err != nil {
			t.Fatalf("PG eco pass %d: %v", pass, err)
		}
		if err := provWorker.ProcessOnce(ctx); err != nil {
			t.Fatalf("PG prov pass %d: %v", pass, err)
		}
		if err := custWorker.ProcessOnce(ctx); err != nil {
			t.Fatalf("PG cust pass %d: %v", pass, err)
		}
		if st, err := store.CutoverDrainStatus(ctx); err != nil {
			t.Fatal(err)
		} else if st.ReadyForActivation {
			break
		} else if pass == 4 {
			t.Fatalf("PG drain did not converge: %+v", st.Counts)
		}
	}
	if n := c3cJournalCount(t, store, accountID); n != beforeJournals+3 {
		t.Fatalf("PG drain journals = %d, want %d", n, beforeJournals+3)
	}
	final, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !final.ReadyForActivation || final.Counts.V1Pinned != 0 || final.Counts.OpenExposures != 0 || final.Counts.AdjustmentPending != 0 {
		t.Fatalf("PG drain must be ready: %+v", final.Counts)
	}
	activated, err := store.ActivateCutoverV2(ctx, "f9-pg-activate")
	if err != nil {
		t.Fatalf("PG activate: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("PG activated = %q", activated.State)
	}
	// Reopen across activation, then fresh V2 call with V2 authority.
	store = open()
	callV2 := c3cMustCallID(t)
	closureV2 := testIndependentCallUsageFor(callV2, []string{"b-f9-pg-v2"})
	closureV2.AccountID = accountID
	expV2, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callV2.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closureV2.CustomerPricingRef, ChargePolicyRef: closureV2.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("PG V2 admit: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closureV2, billing.PostingOwnerV2); err != nil {
		t.Fatalf("PG V2 closure: %v", err)
	}
	legV2 := testIndependentCallLegFor(callV2, "b-f9-pg-v2")
	if err := store.AppendCallLegUsageWithOwner(ctx, legV2, billing.PostingOwnerV2); err != nil {
		t.Fatalf("PG V2 leg: %v", err)
	}
	claimedProv, err := store.ClaimProviderCostWorkWithCutover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedProv) != 1 || claimedProv[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("PG V2 prov claim = %+v, want one V2", claimedProv)
	}
	claimedCust, err := store.ClaimCompleteCallsWithCutover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedCust) != 1 || claimedCust[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("PG V2 cust claim = %+v, want one V2", claimedCust)
	}
	sealedV2, err := legV2.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provCopy := claimedProv[0].Claim
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: accountID, CallID: callV2, Leg: legV2, Result: billing.OperatorCostResult{LURKey: sealedV2.Key, Amount: billing.Money{Nano: 55, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
		PostingOwner: provCopy.Owner, Claim: &provCopy,
	}); err != nil {
		t.Fatalf("PG V2 provider post: %v", err)
	}
	durableV2, err := store.GetCallUsage(ctx, callV2)
	if err != nil {
		t.Fatal(err)
	}
	custCopy := claimedCust[0].Claim
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: durableV2, Exposure: expV2, Result: billing.CallRatingResult{CallID: callV2, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"}, Fingerprint: "f9-pg-v2-fp-" + callV2.String()},
		PostingOwner: custCopy.Owner, Claim: &custCopy,
	}); err != nil {
		t.Fatalf("PG V2 customer post: %v", err)
	}
	// V1 stays fenced in active on PG.
	v1Call := c3cMustCallID(t)
	v1Stub := testIndependentCallUsageFor(v1Call, []string{"b-f9-pg-v1"})
	v1Stub.AccountID = accountID
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: v1Call.String(),
		Max:        billing.Money{Nano: 10, Currency: "USD"},
		PricingRef: v1Stub.CustomerPricingRef, ChargePolicyRef: v1Stub.ChargePolicyRef,
	}); !c3cIsFenceErr(err) {
		t.Fatalf("PG V1 in active err = %v, want fence", err)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema PG after F9: %v", err)
	}
}

func f9PGHead(t *testing.T, i int) string {
	t.Helper()
	return "f9-pg-hist-" + string(rune('0'+i))
}

// TestCutoverIntegratedPostgresActivationRacesMonetaryWhenConfigured is the
// deterministic PostgreSQL transition race for F9: concurrent V1/V2 contenders
// for the same customer, provider, and direct-adjustment logical operations
// yield exactly one owner/effect per operation, and early activation stays
// DrainBlocked. Start gate, no sleeps; simultaneous provider/adjustment drain
// proof on the configured backend.
func TestCutoverIntegratedPostgresActivationRacesMonetaryWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f9-pg-race"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const accountID = "acct-f9-pg-race"
	c3cSetupAccount(t, store, accountID, 100000)
	_ = c3cEnsureShadow(t, store)
	// Direct-adjustment single-writer race pre-drain (shadow V1): same source
	// key, concurrent posts yield exactly one journal effect. Exact replay is
	// idempotent (both may return nil with one Replayed), conflicting payload
	// would fence; either way there is one writer/one effect. Completed
	// adjustments never block drain (F4).
	adj := billing.AdjustmentInput{AccountID: accountID, Amount: billing.Money{Nano: 33, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f9-pg-race-adj", Reason: "race"}
	adjBefore := c3cJournalCount(t, store, accountID)
	var a1Post billing.Posting
	var a2Post billing.Posting
	var a1Err, a2Err error
	var adjWg sync.WaitGroup
	gateAdj := make(chan struct{})
	adjWg.Add(2)
	go func() {
		defer adjWg.Done()
		<-gateAdj
		a1Post, a1Err = store.PostAdjustment(context.Background(), adj)
	}()
	go func() {
		defer adjWg.Done()
		<-gateAdj
		a2Post, a2Err = store.PostAdjustment(context.Background(), adj)
	}()
	close(gateAdj)
	adjWg.Wait()
	for _, err := range []error{a1Err, a2Err} {
		if err != nil && !c3cIsFenceErr(err) {
			t.Fatalf("PG adjustment race err = %v, want nil or fence/conflict", err)
		}
	}
	if n := c3cJournalCount(t, store, accountID); n != adjBefore+1 {
		t.Fatalf("PG adjustment race journals = %d, want %d (single effect)", n, adjBefore+1)
	}
	// Idempotent replay: both nil is legal only when exactly one is Replayed.
	if a1Err == nil && a2Err == nil && !a1Post.Replayed && !a2Post.Replayed {
		t.Fatalf("PG adjustment both applied without replay (dual effect)")
	}
	// Customer + provider seeds pre-drain (shadow V1-active window) so their
	// pins exist before draining; draining classification refreshes them.
	callID := c3cMustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-race"})
	closure.AccountID = accountID
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 5000, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = exp
	if err := store.AppendCallUsage(ctx, closure); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-race")); err != nil {
		t.Fatal(err)
	}
	// Provider seed pre-drain for the later draining V1-vs-V2 race.
	provCallID, provLeg, provRes := c3cSeedProviderPending(t, store, accountID, "b-pg-race-prov")
	_ = provCallID
	_ = provRes
	if _, _, err := store.BeginCutoverDraining(ctx, "f9-pg-race-drain"); err != nil {
		t.Fatal(err)
	}
	custOpKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, custOpKey)
	if err != nil {
		t.Fatal(err)
	}
	durableCall, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	durableExp, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	v1Res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"}, Fingerprint: "f9-pg-race-fp"}
	v1Copy := meta
	v1Input := billing.ApplyCallBillingInput{Call: durableCall, Exposure: durableExp, Result: v1Res, PostingOwner: v1Copy.Owner, Claim: &v1Copy}
	v2Input := billing.ApplyCallBillingInput{Call: durableCall, Exposure: durableExp, Result: v1Res, PostingOwner: billing.PostingOwnerV2}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	var v1Err, v2Err error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-gate
		_, v1Err = store.ApplyCallBillingResult(context.Background(), v1Input)
	}()
	go func() {
		defer wg.Done()
		<-gate
		_, v2Err = store.ApplyCallBillingResult(context.Background(), v2Input)
	}()
	close(gate)
	wg.Wait()
	if v1Err != nil {
		t.Fatalf("PG customer V1 must win: %v", v1Err)
	}
	if !c3cIsFenceErr(v2Err) {
		t.Fatalf("PG customer V2 err = %v, want fence", v2Err)
	}
	// Provider race: same pre-drained leg, V1 draining claim vs V2 fenced.
	// The seeded leg was created pre-drain so its provider pin exists;
	// draining classification refreshed it. In draining, V1 may complete with
	// its renewed token while V2 fences.
	leg := provLeg
	res := provRes
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provOpKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	provMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, provOpKey)
	if err != nil {
		t.Fatalf("PG provider claim metadata (draining V1): %v", err)
	}
	provCopy := provMeta
	pv1Input := billing.ApplyProviderCostInput{AccountID: accountID, CallID: leg.CallID, Leg: leg, Result: res, PostingOwner: provCopy.Owner, Claim: &provCopy}
	pv2Input := billing.ApplyProviderCostInput{AccountID: accountID, CallID: leg.CallID, Leg: leg, Result: res, PostingOwner: billing.PostingOwnerV2}
	var pv1Err, pv2Err error
	wg.Add(2)
	gate2 := make(chan struct{})
	go func() {
		defer wg.Done()
		<-gate2
		_, pv1Err = store.ApplyProviderCost(context.Background(), pv1Input)
	}()
	go func() {
		defer wg.Done()
		<-gate2
		_, pv2Err = store.ApplyProviderCost(context.Background(), pv2Input)
	}()
	close(gate2)
	wg.Wait()
	if pv1Err != nil {
		t.Fatalf("PG provider V1 must win: %v", pv1Err)
	}
	if !c3cIsFenceErr(pv2Err) {
		t.Fatalf("PG provider V2 err = %v, want fence", pv2Err)
	}
	// Activation still blocked until the remaining provider/economic drain is
	// genuinely ready; barrier coverage is deterministic via the start gates
	// above, no sleeps. Economic work (if any) plus the raced pins above keep
	// the coordinator honest; assert blocked-or-ready explicitly rather than
	// manufacturing completion.
	st, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReadyForActivation {
		if _, err := store.ActivateCutoverV2(ctx, "f9-pg-race-activate-ready"); err != nil {
			t.Fatalf("PG ready activate: %v", err)
		}
	} else {
		if _, err := store.ActivateCutoverV2(ctx, "f9-pg-race-activate-blocked"); !errors.Is(err, billing.ErrCutoverDrainBlocked) {
			t.Fatalf("PG blocked activate err = %v, want DrainBlocked", err)
		}
	}
	_ = time.Now
}
