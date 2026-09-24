package billingstore

// Phase 19.2 database, restart and lifecycle-race certification.
//
// Gap closed by this file: every 19.2 lifecycle/race had per-surface
// coverage (cutover crash points, revision restart/concurrency, settlement
// fences, adjustment races, same-A-leg resume, late evidence), but no single
// integrated test exercised same-file/process restart with pending/leased
// customer/provider/economic/adjustment work, late evidence, terminal/DONE
// then same-A-leg resume, cancellation, loser callbacks, duplicate/reordered
// callbacks, adjustment races and cutover crash points while asserting
// journals/balances/pins/heads/exposure/queue together and exactly once.
//
// Both tests are SQLite file-backed so close/reopen is a real same-file
// process-restart analogue. All concurrency uses the deterministic
// ready/start-gate barrier (no sleeps as synchronization). PostgreSQL
// behavior for each surface is certified by the existing per-surface direct
// tests plus the billing dbparity component (see phase19-2 evidence);
// pooler topology is covered by the repository pooled contracts where
// tooling supports them (billing has no pooler-gated test surface).
//
// Requirements: 10.1, 10.3, 10.4, 10.5, 10.6, 11.4, 13.3, 14.4, 17.4, 17.5,
// 18.4, 18.6. Contracts: C6; Migration Strategy; Testing Strategy.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

const (
	ph192StoreIDA        = "ph192"
	ph192StoreIDB        = "ph192-resume"
	ph192AccountA        = "acct-ph192"
	ph192AccountB        = "acct-ph192-resume"
	ph192ALeg            = "a-192"
	ph192ALegResume      = "a-192-resume"
	ph192SelHeadKey      = "ph192-sel-head"
	ph192InitialBalance  = int64(1_000_000)
	ph192Call1Charge     = int64(300)
	ph192ProviderCharge  = int64(55)
	ph192SelInitialNanos = int64(10_000_000_000)
)

func ph192MustCallID(t *testing.T) corebilling.BillingCallID {
	t.Helper()
	id, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ph192CallUsage(callID corebilling.BillingCallID, aLeg, accountID string, bLegs []string) corebilling.CallUsageRecord {
	return corebilling.CallUsageRecord{
		SchemaVersion: corebilling.CurrentRecordSchemaVersion,
		CallID:        callID,
		AccountID:     accountID,
		ALegID:        aLeg,
		SessionID:     "sess-ph192",
		StartedAt:     time.Unix(100, 0).UTC(),
		FinishedAt:    time.Unix(101, 0).UTC(),
		Outcome:       corebilling.TurnOutcomeCompleted,
		CustomerPricingRef: corebilling.VersionRef{
			ID:      "prices",
			Version: "v1",
		},
		ChargePolicyRef: corebilling.VersionRef{
			ID:      "policy",
			Version: "v2",
		},
		ExpectedBLegIDs: bLegs,
	}
}

func ph192Leg(callID corebilling.BillingCallID, aLeg, bLeg, dedupe string, attemptSeq int) corebilling.CallLegUsageRecord {
	return corebilling.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     aLeg,
		BLegID:     bLeg,
		AttemptSeq: attemptSeq,
		BackendID:  "backend-a",
		ProviderID: "provider-a",
		ModelID:    "model-a",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(100, 500000000).UTC(),
		Outcome:    corebilling.LegOutcomeWinner,
		Surfaced:   corebilling.SurfacedYes,
		Evidence: corebilling.FinalBillingEvidence{
			InputTokens:  corebilling.Quantity{Value: 7, Present: true},
			OutputTokens: corebilling.Quantity{Value: 3, Present: true},
			Cost:         corebilling.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       corebilling.EvidenceSourceProviderReported,
			Authority:    corebilling.EvidenceAuthorityAuthoritative,
			DedupeKey:    dedupe,
		},
		OperatorRateRef: corebilling.VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

func ph192SetupAccount(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	acct := corebilling.Account{
		ID: accountID, Currency: "USD", Mode: corebilling.AccountPrepaid,
		BalanceNano: ph192InitialBalance, State: corebilling.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
}

func ph192Admit(t *testing.T, store *DurableStore, accountID string, callID corebilling.BillingCallID, maxNano int64) corebilling.CallExposure {
	t.Helper()
	call := ph192CallUsage(callID, ph192ALeg, accountID, []string{"b-ph192"})
	exp, err := store.AdmitExposure(context.Background(), corebilling.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        corebilling.Money{Nano: maxNano, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatalf("admit %s: %v", callID.String(), err)
	}
	return exp
}

func ph192Settle(t *testing.T, store *DurableStore, call corebilling.CallUsageRecord, exp corebilling.CallExposure, chargeNano int64, fingerprint string) corebilling.CallSettlement {
	t.Helper()
	settled, err := store.ApplyCallBillingResult(context.Background(), corebilling.ApplyCallBillingInput{
		Call:     call,
		Exposure: exp,
		Result: corebilling.CallRatingResult{
			CallID: call.CallID, CustomerCharge: corebilling.Money{Nano: chargeNano, Currency: "USD"}, Fingerprint: fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("settle %s: %v", call.CallID.String(), err)
	}
	return settled
}

// ph192Snapshot captures every financial surface together: journals,
// balance, selected-cost head pin, economic valuation head, worker queue,
// sealed legs and the cutover marker.
type ph192Snapshot struct {
	journals      int
	balance       int64
	selVersion    uint64
	selAmountNano int64
	valVersion    uint64
	valFp         string
	backlog       corebilling.EconomicRevisionBacklog
	legs          int
	pinsPinned    int
	pinsCompleted int
	markerVersion uint64
	markerEpoch   uint64
}

func ph192SnapshotOf(t *testing.T, store *DurableStore, accountID string, callID corebilling.BillingCallID, headKey, selHeadKey string, selCallID corebilling.BillingCallID) ph192Snapshot {
	t.Helper()
	ctx := context.Background()
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	selHead, err := store.GetSelectedCostHead(ctx, accountID, selCallID, selHeadKey)
	if err != nil {
		t.Fatal(err)
	}
	var selAmount int64
	if selHead.Selected == nil || selHead.Selected.Amount == nil || selHead.Selected.Amount.Decimal == nil {
		t.Fatalf("selected head %q has no amount", selHeadKey)
	} else {
		nanos, err := selHead.Selected.Amount.Decimal.ToNanoUnits()
		if err != nil {
			t.Fatal(err)
		}
		selAmount = nanos
	}
	valHead, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, headKey)
	if err != nil {
		t.Fatal(err)
	}
	backlog, err := store.EconomicRevisionQueueBacklog(ctx, corebilling.EconomicQueueProvider)
	if err != nil {
		t.Fatal(err)
	}
	// OldestPendingAge is now-derived at read time; zero it so snapshots
	// compare durable state rather than wall-clock read instants.
	backlog.OldestPendingAge = 0
	legs, err := store.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ph192Snapshot{
		journals:      len(journals),
		balance:       acct.BalanceNano,
		selVersion:    selHead.Version,
		selAmountNano: selAmount,
		valVersion:    valHead.HeadVersion,
		valFp:         valHead.Fingerprint,
		backlog:       backlog,
		legs:          len(legs),
		pinsPinned:    c3cPinnedCount(t, store),
		pinsCompleted: c3cCompletedCount(t, store),
		markerVersion: marker.Version,
		markerEpoch:   marker.Epoch,
	}
}

func ph192RequireSnapshotEqual(t *testing.T, what string, want, got ph192Snapshot) {
	t.Helper()
	if want != got {
		t.Fatalf("%s changed across restart/replay: want=%+v got=%+v", what, want, got)
	}
}

// TestPhase192RestartLifecycleRaceExactlyOnce certifies the 19.2 matrix on
// one file-backed store: pending/leased customer/provider/economic/
// adjustment work, loser-callback races, duplicate/reordered callbacks,
// late evidence, cancellation, adjustment races and cutover crash points,
// then same-file restart and same-A-leg resume, with all financial
// surfaces asserted together and exactly once.
func TestPhase192RestartLifecycleRaceExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ph192-billing.sqlite")
	store, closeStore := openRefinement82FileBillingStore(t, path, ph192StoreIDA)
	restarted := false
	defer func() {
		if !restarted {
			closeStore()
		}
	}()
	ph192SetupAccount(t, store, ph192AccountA)

	// Cutover marker exists before any money moves (Migration Strategy).
	marker0, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Terminal/DONE for call-1 on A-leg a-192: usage + winner leg sealed.
	call1 := ph192MustCallID(t)
	usage1 := ph192CallUsage(call1, ph192ALeg, ph192AccountA, []string{"b-1"})
	if err := store.AppendCallUsage(ctx, usage1); err != nil {
		t.Fatal(err)
	}
	leg1 := ph192Leg(call1, ph192ALeg, "b-1", "ph192-charge-1", 1)
	if err := store.AppendCallLegUsage(ctx, leg1); err != nil {
		t.Fatal(err)
	}
	exp1 := ph192Admit(t, store, ph192AccountA, call1, 1000)

	// Pending economic revision work (provider queue) via the production
	// builder, plus pending provider cost on a second leg of call-1.
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	obs := refinement52ProviderObservation(ph192StoreIDA, call1, 1, "ph192-econ", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	work := refinement82ProviderWork(t, ctx, builder, obs)
	if err := store.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("enqueue economic work: %v", err)
	}
	provLeg := ph192Leg(call1, ph192ALeg, "b-1-prov", "ph192-charge-prov", 2)
	if err := store.AppendCallLegUsage(ctx, provLeg); err != nil {
		t.Fatal(err)
	}
	sealedProv, err := provLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provInput := corebilling.ApplyProviderCostInput{
		AccountID: ph192AccountA, CallID: provLeg.CallID, Leg: provLeg,
		Result: corebilling.OperatorCostResult{
			LURKey: sealedProv.Key, Amount: corebilling.Money{Nano: ph192ProviderCharge, Currency: "USD"},
			AmountPresent: true, Reconciled: true, Authoritative: true,
		},
	}

	// Pending selected-cost head: initial 10 USD posting (late-evidence and
	// adjustment races build on this pin).
	selSubject := b2b3Subject(ph192StoreIDA, ph192AccountA, call1.String())
	selInitial := b2b3Valuation(t, "ph192-sel-v1", 1, ph192SelInitialNanos)
	selInput := corebilling.SelectedCostAdjustmentInput{
		AccountID: ph192AccountA, CallID: call1, HeadKey: ph192SelHeadKey,
		Subject: selSubject, Expected: corebilling.SelectedCostHeadExpectation{}, Selected: selInitial,
	}
	initialAdj, err := store.ApplySelectedCostAdjustment(ctx, selInput)
	if err != nil {
		t.Fatalf("initial selected-cost: %v", err)
	}
	if initialAdj.Status != corebilling.SelectedCostTransitionApplied {
		t.Fatalf("initial selected-cost status = %q, want applied", initialAdj.Status)
	}

	// Loser-callback race 1: four concurrent customer settlements for the
	// same terminal call through a deterministic start gate. Exactly one
	// applies; losers replay without new effects (Req 10.1, 14.4).
	settleInput := corebilling.ApplyCallBillingInput{
		Call: usage1, Exposure: exp1,
		Result: corebilling.CallRatingResult{
			CallID: call1, CustomerCharge: corebilling.Money{Nano: ph192Call1Charge, Currency: "USD"}, Fingerprint: "ph192-call1",
		},
	}
	journalsBeforeSettle, err := store.JournalTransactions(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	const settlers = 4
	settleOut := make([]corebilling.CallSettlement, settlers)
	settleErrs := refinement82ReleaseBarrier(t, settlers, func(i int) error {
		out, err := store.ApplyCallBillingResult(ctx, settleInput)
		if err != nil {
			return err
		}
		settleOut[i] = out
		return nil
	})
	for i, err := range settleErrs {
		if err != nil {
			t.Fatalf("settlement racer %d: %v", i, err)
		}
	}
	applied, replayed := 0, 0
	for _, out := range settleOut {
		if out.Replayed {
			replayed++
		} else {
			applied++
		}
	}
	if applied != 1 || replayed != settlers-1 {
		t.Fatalf("settlement race applied=%d replayed=%d, want 1/%d", applied, replayed, settlers-1)
	}
	journalsAfterSettle, err := store.JournalTransactions(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	if len(journalsAfterSettle) != len(journalsBeforeSettle)+1 {
		t.Fatalf("settlement race journals = %d, want %d", len(journalsAfterSettle), len(journalsBeforeSettle)+1)
	}
	acctAfterSettle, err := store.GetAccount(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterSettle.BalanceNano != ph192InitialBalance-ph192Call1Charge {
		t.Fatalf("balance after settlement race = %d, want %d", acctAfterSettle.BalanceNano, ph192InitialBalance-ph192Call1Charge)
	}

	// Loser-callback race 2: four concurrent provider-cost callbacks for
	// the same leg. Exactly one posts; losers replay (Req 10.1, 14.4).
	provOut := make([]corebilling.Posting, settlers)
	provErrs := refinement82ReleaseBarrier(t, settlers, func(i int) error {
		out, err := store.ApplyProviderCost(ctx, provInput)
		if err != nil {
			return err
		}
		provOut[i] = out
		return nil
	})
	for i, err := range provErrs {
		if err != nil {
			t.Fatalf("provider racer %d: %v", i, err)
		}
	}
	provApplied, provReplayed := 0, 0
	for _, out := range provOut {
		if out.Replayed {
			provReplayed++
		} else {
			provApplied++
		}
	}
	if provApplied != 1 || provReplayed != settlers-1 {
		t.Fatalf("provider race applied=%d replayed=%d, want 1/%d", provApplied, provReplayed, settlers-1)
	}

	// Worker-claim race: four concurrent lease claims on the pending
	// economic work elect exactly one winner; a foreign-owner completion
	// fails closed and the winner completes (Req 10.6, C6).
	type claimOutcome struct {
		acquired bool
		fence    uint64
		owner    string
	}
	claimOut := make([]claimOutcome, settlers)
	claimErrs := refinement82ReleaseBarrier(t, settlers, func(i int) error {
		claim, acquired, err := store.ClaimEconomicRevisionWork(ctx, work, fmt.Sprintf("ph192-worker-%d", i), time.Minute)
		if err != nil {
			return err
		}
		claimOut[i] = claimOutcome{acquired: acquired, fence: claim.Fence, owner: fmt.Sprintf("ph192-worker-%d", i)}
		return nil
	})
	for i, err := range claimErrs {
		if err != nil {
			t.Fatalf("claim racer %d: %v", i, err)
		}
	}
	winners := 0
	var winner claimOutcome
	for _, out := range claimOut {
		if out.acquired {
			winners++
			winner = out
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want exactly one", winners)
	}
	loserOwner := "ph192-worker-loser"
	if winner.owner == loserOwner {
		loserOwner = "ph192-worker-loser-2"
	}
	if err := store.CompleteEconomicRevisionWork(ctx, work, corebilling.EconomicRevisionWorkClaim{Owner: loserOwner, Fence: winner.fence}); !errors.Is(err, corebilling.ErrEconomicRevisionClaimLost) {
		t.Fatalf("foreign-owner complete = %v, want ErrEconomicRevisionClaimLost", err)
	}

	// Adjustment race: two distinct corrections (8 vs 7 USD) against the
	// same head expectation through a start gate. Exactly one wins; the
	// loser fences without effects. Delivery order must not matter
	// (Req 13.3, 14.4).
	corrA := b2b3Valuation(t, "ph192-sel-v2a", 2, 8_000_000_000)
	corrB := b2b3Valuation(t, "ph192-sel-v2b", 2, 7_000_000_000)
	expectV1 := corebilling.SelectedCostHeadExpectation{Version: 1, Previous: &selInitial}
	adjOut := make([]corebilling.SelectedCostAdjustmentResult, 2)
	adjErrs := refinement82ReleaseBarrier(t, 2, func(i int) error {
		val := corrA
		if i == 1 {
			val = corrB
		}
		out, err := store.ApplySelectedCostAdjustment(ctx, corebilling.SelectedCostAdjustmentInput{
			AccountID: ph192AccountA, CallID: call1, HeadKey: ph192SelHeadKey,
			Subject: selSubject, Expected: expectV1, Selected: val,
		})
		if err != nil {
			return err
		}
		adjOut[i] = out
		return nil
	})
	adjApplied := 0
	for i, err := range adjErrs {
		if err == nil && adjOut[i].Status == corebilling.SelectedCostTransitionApplied {
			adjApplied++
		}
	}
	if adjApplied != 1 {
		t.Fatalf("adjustment race applied=%d errs=%v, want exactly one winner", adjApplied, adjErrs)
	}
	adjJournalsBefore, err := store.JournalTransactions(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	adjJournalCount := 0
	for _, j := range adjJournalsBefore {
		if j.OperationKind == "provider_call_cogs" && j.CorrectionGroupID == ph192SelHeadKey {
			adjJournalCount++
		}
	}
	if adjJournalCount != 2 {
		t.Fatalf("adjustment journals = %d, want initial + one winning correction", adjJournalCount)
	}

	// Winner completes the leased economic work and its valuation result
	// advances the provider head exactly once (Req 10.6, 11.4).
	if err := store.CompleteEconomicRevisionWork(ctx, work, corebilling.EconomicRevisionWorkClaim{Owner: winner.owner, Fence: winner.fence}); err != nil {
		t.Fatalf("winner complete: %v", err)
	}
	if err := store.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work)}); err != nil {
		t.Fatalf("economic result: %v", err)
	}
	head1, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head1.HeadVersion != 1 {
		t.Fatalf("valuation head version = %d, want 1", head1.HeadVersion)
	}
	valsAfterResult := refinement82ValuationCount(t, ctx, store)

	// Cutover crash-point race: two concurrent shadow transitions against
	// the same marker version elect one winner (Req 17.4).
	shadowErrs := refinement82ReleaseBarrier(t, 2, func(i int) error {
		_, err := store.TransitionAccountingCutover(ctx, corebilling.AccountingCutoverTransition{
			ExpectedVersion: marker0.Version, ExpectedEpoch: marker0.Epoch,
			NextState:    corebilling.AccountingCutoverV2Shadow,
			TransitionID: fmt.Sprintf("ph192-shadow-%d", i),
		})
		return err
	})
	shadowWon := 0
	for _, err := range shadowErrs {
		if err == nil {
			shadowWon++
		}
	}
	if shadowWon != 1 {
		t.Fatalf("cutover race winners = %d errs=%v, want exactly one", shadowWon, shadowErrs)
	}
	markerPost, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if markerPost.Version != marker0.Version+1 {
		t.Fatalf("marker version = %d, want %d", markerPost.Version, marker0.Version+1)
	}

	before := ph192SnapshotOf(t, store, ph192AccountA, call1, work.HeadKey, ph192SelHeadKey, call1)

	// Same-file/process restart: close and reopen the identical file with
	// the same store identity. All surfaces must be byte-stable
	// (Req 10.6, 11.4, 17.4).
	closeStore()
	restarted = true
	store, closeStore = openRefinement82FileBillingStore(t, path, ph192StoreIDA)
	defer closeStore()
	afterReopen := ph192SnapshotOf(t, store, ph192AccountA, call1, work.HeadKey, ph192SelHeadKey, call1)
	ph192RequireSnapshotEqual(t, "restart", before, afterReopen)

	// Post-restart duplicate callbacks replay without effects (Req 10.1).
	dupSettle, err := store.ApplyCallBillingResult(ctx, settleInput)
	if err != nil {
		t.Fatalf("duplicate settlement after restart: %v", err)
	}
	if !dupSettle.Replayed {
		t.Fatalf("duplicate settlement after restart applied, want replay")
	}
	dupProv, err := store.ApplyProviderCost(ctx, provInput)
	if err != nil {
		t.Fatalf("duplicate provider cost after restart: %v", err)
	}
	if !dupProv.Replayed {
		t.Fatalf("duplicate provider cost after restart applied, want replay")
	}
	if err := store.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work)}); err != nil {
		t.Fatalf("duplicate economic result after restart: %v", err)
	}
	headReplay, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if headReplay.Fingerprint != head1.Fingerprint || headReplay.HeadVersion != head1.HeadVersion {
		t.Fatalf("economic replay mutated head: before=%+v after=%+v", head1, headReplay)
	}
	if got := refinement82ValuationCount(t, ctx, store); got != valsAfterResult {
		t.Fatalf("economic replay added valuations: before=%d after=%d", valsAfterResult, got)
	}

	// Reordered callback: a stale correction that expects the superseded
	// head version is fenced as a typed zero-effect stale result; the
	// snapshot proves no journal/head/balance moved (Req 10.3, 13.3).
	staleVal := b2b3Valuation(t, "ph192-sel-v2-stale", 2, 6_000_000_000)
	staleRes, err := store.ApplySelectedCostAdjustment(ctx, corebilling.SelectedCostAdjustmentInput{
		AccountID: ph192AccountA, CallID: call1, HeadKey: ph192SelHeadKey,
		Subject: selSubject, Expected: expectV1, Selected: staleVal,
	})
	if err != nil {
		t.Fatalf("stale correction: %v", err)
	}
	if staleRes.Status != corebilling.SelectedCostTransitionStale {
		t.Fatalf("stale correction status = %q, want stale", staleRes.Status)
	}
	mid := ph192SnapshotOf(t, store, ph192AccountA, call1, work.HeadKey, ph192SelHeadKey, call1)
	ph192RequireSnapshotEqual(t, "stale correction", before, mid)

	// Late evidence after closure: a revision-3 correction referencing the
	// current head posts exactly one balanced delta (10/8/7 -> 9 USD or
	// 10/7 -> 9 depending on the race winner is wrong: the winner is
	// either 8 or 7, so the delta is 9-winner). Replay is idempotent
	// (Req 10.3, 10.4, 13.3).
	currentHead, err := store.GetSelectedCostHead(ctx, ph192AccountA, call1, ph192SelHeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if currentHead.Selected == nil {
		t.Fatalf("current selected head has no valuation")
	}
	lateVal := b2b3Valuation(t, "ph192-sel-v3", 3, 9_000_000_000)
	lateInput := corebilling.SelectedCostAdjustmentInput{
		AccountID: ph192AccountA, CallID: call1, HeadKey: ph192SelHeadKey,
		Subject:  selSubject,
		Expected: corebilling.SelectedCostHeadExpectation{Version: currentHead.Version, Previous: currentHead.Selected},
		Selected: lateVal,
	}
	lateAdj, err := store.ApplySelectedCostAdjustment(ctx, lateInput)
	if err != nil {
		t.Fatalf("late correction: %v", err)
	}
	if lateAdj.Status != corebilling.SelectedCostTransitionApplied {
		t.Fatalf("late correction status = %q, want applied", lateAdj.Status)
	}
	lateReplay, err := store.ApplySelectedCostAdjustment(ctx, lateInput)
	if err != nil {
		t.Fatalf("late correction replay: %v", err)
	}
	if lateReplay.Status != corebilling.SelectedCostTransitionReplay {
		t.Fatalf("late correction replay status = %q, want replay", lateReplay.Status)
	}

	// Cancellation fails closed with zero new effects (Req 10.5, 14.3).
	cancelCall := ph192MustCallID(t)
	cancelUsage := ph192CallUsage(cancelCall, ph192ALeg, ph192AccountA, []string{"b-cancel"})
	if err := store.AppendCallUsage(ctx, cancelUsage); err != nil {
		t.Fatal(err)
	}
	cancelLeg := ph192Leg(cancelCall, ph192ALeg, "b-cancel", "ph192-charge-cancel", 1)
	if err := store.AppendCallLegUsage(ctx, cancelLeg); err != nil {
		t.Fatal(err)
	}
	cancelExp := ph192Admit(t, store, ph192AccountA, cancelCall, 1000)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	preCancel := ph192SnapshotOf(t, store, ph192AccountA, call1, work.HeadKey, ph192SelHeadKey, call1)
	if _, err := store.ApplyCallBillingResult(cancelled, corebilling.ApplyCallBillingInput{
		Call: cancelUsage, Exposure: cancelExp,
		Result: corebilling.CallRatingResult{
			CallID: cancelCall, CustomerCharge: corebilling.Money{Nano: 100, Currency: "USD"}, Fingerprint: "ph192-cancel",
		},
	}); err == nil {
		t.Fatalf("canceled settlement must fail")
	}
	postCancel := ph192SnapshotOf(t, store, ph192AccountA, call1, work.HeadKey, ph192SelHeadKey, call1)
	// The canceled call added usage+leg+exposure rows outside the money
	// snapshot; the money surfaces (journals/balance/heads) must not move.
	if preCancel.journals != postCancel.journals || preCancel.balance != postCancel.balance ||
		preCancel.selVersion != postCancel.selVersion || preCancel.valVersion != postCancel.valVersion ||
		preCancel.valFp != postCancel.valFp {
		t.Fatalf("canceled settlement moved money: before=%+v after=%+v", preCancel, postCancel)
	}

	// Terminal/DONE then same-A-leg resume: call-2 on a-192 allocates a
	// fresh BillingCallID and settles independently without reopening
	// call-1 records (Req 10.4).
	sealed1, err := store.GetCallUsage(ctx, call1)
	if err != nil {
		t.Fatal(err)
	}
	sealed1FP, err := sealed1.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	call2 := ph192MustCallID(t)
	usage2 := ph192CallUsage(call2, ph192ALeg, ph192AccountA, []string{"b-2"})
	usage2.StartedAt = time.Unix(300, 0).UTC()
	usage2.FinishedAt = time.Unix(301, 0).UTC()
	if err := store.AppendCallUsage(ctx, usage2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, ph192Leg(call2, ph192ALeg, "b-2", "ph192-charge-2", 1)); err != nil {
		t.Fatal(err)
	}
	exp2 := ph192Admit(t, store, ph192AccountA, call2, 1000)
	settled2 := ph192Settle(t, store, usage2, exp2, 150, "ph192-call2")
	if settled2.Replayed {
		t.Fatalf("resumed call settlement replayed, want apply")
	}
	sealed1After, err := store.GetCallUsage(ctx, call1)
	if err != nil {
		t.Fatal(err)
	}
	sealed1AfterFP, err := sealed1After.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if sealed1AfterFP != sealed1FP {
		t.Fatalf("resume mutated sealed call-1 record")
	}
	legs1, err := store.ListCallLegUsage(ctx, call1)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs1) != 2 {
		t.Fatalf("call-1 legs = %d, want winner + provider legs only", len(legs1))
	}

	// Final exactly-once accounting: initial balance minus the two
	// customer charges only; provider COGS and adjustments never touch
	// the customer balance; every replay above added nothing.
	final, err := store.GetAccount(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	if want := ph192InitialBalance - ph192Call1Charge - 150; final.BalanceNano != want {
		t.Fatalf("final balance = %d, want %d", final.BalanceNano, want)
	}
	finalSel, err := store.GetSelectedCostHead(ctx, ph192AccountA, call1, ph192SelHeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if finalSel.Version != 3 {
		t.Fatalf("final selected head version = %d, want 3 (initial + race winner + late)", finalSel.Version)
	}
	finalJournals, err := store.JournalTransactions(ctx, ph192AccountA)
	if err != nil {
		t.Fatal(err)
	}
	// 1 customer settlement (call-1 race) + 1 provider posting + 3
	// selected-cost postings (initial + race winner + late) + 1 customer
	// settlement (call-2 resume) = 6. Cancel admission holds no journal;
	// every replay/duplicate/stale/canceled callback added zero.
	if len(finalJournals) != 6 {
		ops := make([]string, 0, len(finalJournals))
		for _, j := range finalJournals {
			ops = append(ops, j.OperationKind)
		}
		t.Fatalf("final journals = %d %v, want 6", len(finalJournals), ops)
	}
}

// TestPhase192RepeatedTerminalDoneSameALegResume certifies repeated
// terminal/DONE followed by same-A-leg resume across a restart: six
// generations on one A-leg, each with a fresh BillingCallID, each settled
// exactly once, with duplicate terminal callbacks replaying idempotently
// (Req 10.4, 10.6, 14.4).
func TestPhase192RepeatedTerminalDoneSameALegResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ph192-resume-billing.sqlite")
	store, closeStore := openRefinement82FileBillingStore(t, path, ph192StoreIDB)
	restarted := false
	defer func() {
		if !restarted {
			closeStore()
		}
	}()
	ph192SetupAccount(t, store, ph192AccountB)
	if _, err := store.EnsureAccountingCutover(ctx); err != nil {
		t.Fatal(err)
	}

	const generations = 6
	const charge = int64(100)
	var firstCallID corebilling.BillingCallID
	var firstFP string
	for gen := range generations {
		if gen == 3 {
			// Restart mid-sequence: close and reopen the same file.
			closeStore()
			restarted = true
			var reopenedClose func()
			store, reopenedClose = openRefinement82FileBillingStore(t, path, ph192StoreIDB)
			closeStore = reopenedClose
			restarted = false
		}
		callID := ph192MustCallID(t)
		bLeg := fmt.Sprintf("b-r%d", gen)
		usage := ph192CallUsage(callID, ph192ALegResume, ph192AccountB, []string{bLeg})
		usage.StartedAt = time.Unix(int64(1000+gen*10), 0).UTC()
		usage.FinishedAt = time.Unix(int64(1000+gen*10+1), 0).UTC()
		if err := store.AppendCallUsage(ctx, usage); err != nil {
			t.Fatalf("gen %d append usage: %v", gen, err)
		}
		if err := store.AppendCallLegUsage(ctx, ph192Leg(callID, ph192ALegResume, bLeg, fmt.Sprintf("ph192-r-charge-%d", gen), 1)); err != nil {
			t.Fatalf("gen %d append leg: %v", gen, err)
		}
		exp := ph192Admit(t, store, ph192AccountB, callID, 5000)
		settled := ph192Settle(t, store, usage, exp, charge, fmt.Sprintf("ph192-resume-%d", gen))
		if settled.Replayed {
			t.Fatalf("gen %d settlement replayed, want apply", gen)
		}
		// Duplicate terminal callbacks (DONE redelivery + settlement
		// retry) replay without effects.
		if err := store.AppendCallUsage(ctx, usage); err != nil {
			t.Fatalf("gen %d duplicate usage: %v", gen, err)
		}
		dup, err := store.ApplyCallBillingResult(ctx, corebilling.ApplyCallBillingInput{
			Call: usage, Exposure: exp,
			Result: corebilling.CallRatingResult{
				CallID: callID, CustomerCharge: corebilling.Money{Nano: charge, Currency: "USD"}, Fingerprint: fmt.Sprintf("ph192-resume-%d", gen),
			},
		})
		if err != nil {
			t.Fatalf("gen %d duplicate settlement: %v", gen, err)
		}
		if !dup.Replayed {
			t.Fatalf("gen %d duplicate settlement applied, want replay", gen)
		}
		sealed, err := store.GetCallUsage(ctx, callID)
		if err != nil {
			t.Fatalf("gen %d sealed read: %v", gen, err)
		}
		fp, err := sealed.SemanticFingerprint()
		if err != nil {
			t.Fatalf("gen %d fingerprint: %v", gen, err)
		}
		if gen == 0 {
			firstCallID = callID
			firstFP = fp
		}
		legs, err := store.ListCallLegUsage(ctx, callID)
		if err != nil {
			t.Fatalf("gen %d legs: %v", gen, err)
		}
		if len(legs) != 1 {
			t.Fatalf("gen %d legs = %d, want exactly one sealed leg", gen, len(legs))
		}
	}

	// The first sealed record survived five later resumes on the same
	// A-leg plus a restart: exactly-once per generation, nothing reopened.
	final, err := store.GetAccount(ctx, ph192AccountB)
	if err != nil {
		t.Fatal(err)
	}
	if want := ph192InitialBalance - generations*charge; final.BalanceNano != want {
		t.Fatalf("final balance = %d, want %d", final.BalanceNano, want)
	}
	journals, err := store.JournalTransactions(ctx, ph192AccountB)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != generations {
		t.Fatalf("journals = %d, want one settlement per generation (%d)", len(journals), generations)
	}
	firstSealed, err := store.GetCallUsage(ctx, firstCallID)
	if err != nil {
		t.Fatal(err)
	}
	firstAfterFP, err := firstSealed.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if firstAfterFP != firstFP {
		t.Fatalf("later resumes mutated the first sealed record")
	}
}
