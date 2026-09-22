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
	_ "modernc.org/sqlite"
)

// Task 17.4 RED: compatible rollback and recovery checks (Migration Strategy
// step 7, requirements 10.5, 11.6, 17.5, 18.4).
//
// Capture-only rollback is permitted only before any V2 monetary posting is
// durable. After V2 postings, a stale (V1-only, non-epoch-aware) binary must
// fail startup (or quiesce strict admissions) with zero new effects, while a
// compatible current binary restarts on the same database and drains real
// pending customer/provider/economic work exactly once. Pending provider
// evidence survives and never authorizes an old monetary writer; expiring
// processed operational queue rows through the supported retention path
// preserves canonical financial/audit linkage and recovery.
//
// These tests must FAIL before accounting_recovery_store.go exists and PASS
// after. No manual SQL operator magic: all postings flow through production
// admission/terminal/claim/worker APIs.

func rec174StaleCapability() billing.AccountingBinaryCapability {
	return billing.AccountingBinaryCapability{SupportsV1Reader: true}
}

func rec174SnapshotMustVerify(t *testing.T, snapshot billing.AccountingRecoverySnapshot, capability billing.AccountingBinaryCapability, wantStale bool) {
	t.Helper()
	err := billing.CheckAccountingStartup(snapshot, capability)
	if wantStale && !errors.Is(err, billing.ErrAccountingStaleBinary) {
		t.Fatalf("want stale-binary rejection, got %v", err)
	}
	if !wantStale && err != nil {
		t.Fatalf("compatible startup must pass: %v", err)
	}
	if billing.AccountingRequiresStrictQuiesce(snapshot, capability) != wantStale {
		t.Fatalf("quiesce predicate must agree with startup verdict (wantStale=%v)", wantStale)
	}
}

func TestRecovery174FreshStoreHasNoV2Postings(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "rec174-fresh")
	ctx := context.Background()
	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatalf("HasV2MonetaryPostings fresh: %v", err)
	}
	if hasV2 {
		t.Fatalf("fresh store must report no V2 monetary postings")
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("GetAccountingRecoverySnapshot fresh: %v", err)
	}
	if snapshot.MarkerFound {
		t.Fatalf("fresh store must report no marker")
	}
	if snapshot.HasV2MonetaryPosting {
		t.Fatalf("fresh snapshot must report no V2 postings")
	}
	if err := billing.CheckCaptureRollbackAllowed(snapshot); err != nil {
		t.Fatalf("fresh store must allow capture rollback: %v", err)
	}
	rec174SnapshotMustVerify(t, snapshot, rec174StaleCapability(), false)
	rec174SnapshotMustVerify(t, snapshot, billing.CurrentAccountingBinaryCapability(), false)
	verified, err := store.VerifyAccountingRecovery(ctx, rec174StaleCapability())
	if err != nil {
		t.Fatalf("fresh store must verify stale binary: %v", err)
	}
	if verified.HasV2MonetaryPosting || verified.MarkerFound {
		t.Fatalf("fresh verify mismatch: %#v", verified)
	}
}

func TestRecovery174V1PostingsAreNotV2Postings(t *testing.T) {
	t.Parallel()
	store := f2aNewStore(t, "rec174-v1only")
	ctx := context.Background()
	f2aSetupShadowAccount(t, store, "acct-rec174-v1")
	// Full V1 monetary pipeline: legacy admission, terminal evidence, and
	// production claim workers posting exactly once.
	callID := f2aMustCallID(t)
	stub := testIndependentCallUsageFor(callID, []string{"b-v1"})
	stub.AccountID = "acct-rec174-v1"
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-v1", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: stub.CustomerPricingRef, ChargePolicyRef: stub.ChargePolicyRef,
	}); err != nil {
		t.Fatal(err)
	}
	closure := testIndependentCallUsageFor(callID, []string{"b-v1"})
	closure.AccountID = "acct-rec174-v1"
	if err := store.AppendCallUsage(ctx, closure); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-v1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithClaim(store, store, f2aRatingStub{charge: 120, fp: "rec174-v1-fp"}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithClaim(store, store, f2aProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("V1 provider worker: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("V1 customer worker: %v", err)
	}
	if n := f2aCustomerJournals(t, store, "acct-rec174-v1"); n != 1 {
		t.Fatalf("V1 customer journals = %d, want 1", n)
	}
	// V1 money moved, but no V2 financial authority exists: rollback stays
	// allowed and the stale binary still serves.
	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasV2 {
		t.Fatalf("V1-only postings must not report V2 monetary postings")
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HasV2MonetaryPosting {
		t.Fatalf("V1-only snapshot must report no V2 postings")
	}
	if err := billing.CheckCaptureRollbackAllowed(snapshot); err != nil {
		t.Fatalf("V1-only store must allow capture rollback: %v", err)
	}
	rec174SnapshotMustVerify(t, snapshot, rec174StaleCapability(), false)
	if _, err := store.VerifyAccountingRecovery(ctx, rec174StaleCapability()); err != nil {
		t.Fatalf("V1-only store must verify stale binary: %v", err)
	}
}

func TestRecovery174V2PostingsBlockStaleBinary(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "rec174-v2posted")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-rec174-v2", 100000)
	f3ActivateEmpty(t, store)
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-v2"})
	closure.AccountID = "acct-rec174-v2"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-v2", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-v2"), billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 120}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f3JournalCount(t, store, "acct-rec174-v2"); n != 2 {
		t.Fatalf("V2 journals = %d, want 2 (customer+provider)", n)
	}
	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasV2 {
		t.Fatalf("posted V2 pipeline must report V2 monetary postings")
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.MarkerFound || snapshot.Marker.State != billing.AccountingCutoverV2Active {
		t.Fatalf("snapshot must carry the v2_active marker, got %#v", snapshot)
	}
	if !snapshot.HasV2MonetaryPosting {
		t.Fatalf("snapshot must report V2 postings")
	}
	if err := billing.CheckCaptureRollbackAllowed(snapshot); !errors.Is(err, billing.ErrAccountingRollbackBlocked) {
		t.Fatalf("V2 postings must block capture rollback, got %v", err)
	}
	rec174SnapshotMustVerify(t, snapshot, rec174StaleCapability(), true)
	rec174SnapshotMustVerify(t, snapshot, billing.CurrentAccountingBinaryCapability(), false)
	// Stale verification fails with zero new effects: marker, journals and
	// balance are unchanged by the rejected startup.
	markerBefore, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	balanceBefore := f3Balance(t, store, "acct-rec174-v2")
	journalsBefore := f3JournalCount(t, store, "acct-rec174-v2")
	if _, err := store.VerifyAccountingRecovery(ctx, rec174StaleCapability()); !errors.Is(err, billing.ErrAccountingStaleBinary) {
		t.Fatalf("stale verify must fail with ErrAccountingStaleBinary, got %v", err)
	}
	markerAfter, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if markerAfter != markerBefore {
		t.Fatalf("rejected startup mutated marker: %#v -> %#v", markerBefore, markerAfter)
	}
	if got := f3Balance(t, store, "acct-rec174-v2"); got != balanceBefore {
		t.Fatalf("rejected startup mutated balance %d -> %d", balanceBefore, got)
	}
	if n := f3JournalCount(t, store, "acct-rec174-v2"); n != journalsBefore {
		t.Fatalf("rejected startup mutated journals %d -> %d", journalsBefore, n)
	}
	if _, err := store.VerifyAccountingRecovery(ctx, billing.CurrentAccountingBinaryCapability()); err != nil {
		t.Fatalf("compatible verify must pass: %v", err)
	}
}

func TestRecovery174PendingProviderEvidencePreserved(t *testing.T) {
	t.Parallel()
	store := f2aNewStore(t, "rec174-pending")
	ctx := context.Background()
	f2aSetupShadowAccount(t, store, "acct-rec174-pend")
	// Pending V1 provider evidence: terminal V2-shadow capture with provider
	// leg materialized but no worker run. This is evidence, not a posting.
	callID := f2aMustCallID(t)
	stub := testIndependentCallUsageFor(callID, []string{"b-pend"})
	stub.AccountID = "acct-rec174-pend"
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-pend", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: stub.CustomerPricingRef, ChargePolicyRef: stub.ChargePolicyRef,
	}); err != nil {
		t.Fatal(err)
	}
	closure := testIndependentCallUsageFor(callID, []string{"b-pend"})
	closure.AccountID = "acct-rec174-pend"
	if err := store.AppendCallUsage(ctx, closure); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-pend")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.GetProviderCostWorkState(ctx, sealedLeg.Key)
	if err != nil {
		t.Fatalf("pending provider evidence must exist: %v", err)
	}
	if state.Status != "pending" {
		t.Fatalf("provider work status = %q, want pending", state.Status)
	}
	// Pending evidence is not a V2 posting and does not block rollback.
	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasV2 {
		t.Fatalf("pending provider evidence must not report V2 monetary postings")
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := billing.CheckCaptureRollbackAllowed(snapshot); err != nil {
		t.Fatalf("pending evidence must not block capture rollback: %v", err)
	}
	// Read-only verification preserves the pending evidence.
	if _, err := store.VerifyAccountingRecovery(ctx, rec174StaleCapability()); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetProviderCostWorkState(ctx, sealedLeg.Key)
	if err != nil {
		t.Fatalf("pending provider evidence must survive verification: %v", err)
	}
	if again.Status != "pending" {
		t.Fatalf("verification consumed pending evidence: %q", again.Status)
	}
	// The pending evidence still drains exactly once through the legitimate
	// V1 worker (pre-cutover ownership is intact).
	provWorker, err := billing.NewCallProviderCostWorkerWithClaim(store, store, f2aProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("pending provider drain: %v", err)
	}
	done, err := store.GetProviderCostWorkState(ctx, sealedLeg.Key)
	if err != nil {
		t.Fatalf("processed provider row must remain queryable: %v", err)
	}
	if done.Status != "processed" {
		t.Fatalf("provider work status = %q, want processed", done.Status)
	}
}

func TestRecovery174V2PendingIsNotV1Claimable(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "rec174-v2pend")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-rec174-v2p", 100000)
	f3ActivateEmpty(t, store)
	// One posted V2 call so V2 financial authority is durable.
	postedID := f3MustCallID(t)
	posted := testIndependentCallUsageFor(postedID, []string{"b-posted"})
	posted.AccountID = "acct-rec174-v2p"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-v2p", CallID: postedID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: posted.CustomerPricingRef, ChargePolicyRef: posted.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, posted, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(postedID, "b-posted"), billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 120}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// A second V2 call stays pending: provider evidence exists but no money
	// moved for it yet.
	pendingID := f3MustCallID(t)
	pending := testIndependentCallUsageFor(pendingID, []string{"b-pend"})
	pending.AccountID = "acct-rec174-v2p"
	pendingExp, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-v2p", CallID: pendingID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: pending.CustomerPricingRef, ChargePolicyRef: pending.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, pending, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	pendingLeg := testIndependentCallLegFor(pendingID, "b-pend")
	if err := store.AppendCallLegUsageWithOwner(ctx, pendingLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	sealedPending, err := pendingLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	pendingState, err := store.GetProviderCostWorkState(ctx, sealedPending.Key)
	if err != nil {
		t.Fatalf("V2 pending provider evidence must exist: %v", err)
	}
	if pendingState.Status != "pending" {
		t.Fatalf("V2 provider work status = %q, want pending", pendingState.Status)
	}
	// The pending V2 evidence is not permission for an old monetary writer:
	// a V1-owner post against the V2-admitted call fences, and stale startup
	// stays rejected.
	durableCall, err := store.GetCallUsage(ctx, pendingID)
	if err != nil {
		t.Fatal(err)
	}
	v1Res := billing.CallRatingResult{CallID: pendingID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"}, Fingerprint: "rec174-v2p-v1x"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: durableCall, Exposure: pendingExp, Result: v1Res, PostingOwner: billing.PostingOwnerV1,
	}); !f3IsFenceErr(err) {
		t.Fatalf("V1 post on V2-admitted pending call err = %v, want fence", err)
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec174SnapshotMustVerify(t, snapshot, rec174StaleCapability(), true)
	rec174SnapshotMustVerify(t, snapshot, billing.CurrentAccountingBinaryCapability(), false)
	// Compatible recovery drains the pending V2 work exactly once.
	balanceBefore := f3Balance(t, store, "acct-rec174-v2p")
	journalsBefore := f3JournalCount(t, store, "acct-rec174-v2p")
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("compatible provider drain: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("compatible customer drain: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-v2p"); got != balanceBefore-120 {
		t.Fatalf("compatible drain balance = %d, want %d", got, balanceBefore-120)
	}
	if n := f3JournalCount(t, store, "acct-rec174-v2p"); n != journalsBefore+2 {
		t.Fatalf("compatible drain journals = %d, want %d", n, journalsBefore+2)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider replay: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer replay: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-v2p"); got != balanceBefore-120 {
		t.Fatalf("replay moved balance to %d", got)
	}
	if n := f3JournalCount(t, store, "acct-rec174-v2p"); n != journalsBefore+2 {
		t.Fatalf("replay moved journals to %d", n)
	}
}

func rec174OpenFileStore(t *testing.T, path, storeID string) (*DurableStore, func()) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	s, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	return s, func() { _ = s.Close() }
}

func TestRecovery174ForwardRecoveryDrainsExactlyOnceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec174-forward.db")
	store, closeFn := rec174OpenFileStore(t, path, "rec174-forward")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-rec174-fwd", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "rec174-fwd-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "rec174-fwd-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "rec174-fwd-activate"); err != nil {
		t.Fatal(err)
	}
	// Pending V2 customer + provider work: admitted and terminal, no worker
	// run before the simulated crash.
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-fwd"})
	closure.AccountID = "acct-rec174-fwd"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-fwd", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	fwdLeg := testIndependentCallLegFor(callID, "b-fwd")
	if err := store.AppendCallLegUsageWithOwner(ctx, fwdLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	// Pending V2 monetary economic work for a second call.
	econID := f2bMustCallID(t)
	econWork := f2bProviderWork(t, store, "acct-rec174-fwd", econID, "b-econ", "rec174-head-econ", 1, true)
	econWork.PostingOwner = billing.PostingOwnerV2
	econWork, err = econWork.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendProviderPostingEconomicRevisionWork(ctx, econWork, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	// Crash: close without running any worker.
	closeFn()
	// Compatible binary restarts on the same database file.
	reopened, close2 := rec174OpenFileStore(t, path, "rec174-forward")
	defer close2()
	snapshot, err := reopened.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	if !snapshot.MarkerFound || snapshot.Marker.State != billing.AccountingCutoverV2Active {
		t.Fatalf("reopen marker must be v2_active, got %#v", snapshot)
	}
	rec174SnapshotMustVerify(t, snapshot, rec174StaleCapability(), true)
	rec174SnapshotMustVerify(t, snapshot, billing.CurrentAccountingBinaryCapability(), false)
	if _, err := reopened.VerifyAccountingRecovery(ctx, billing.CurrentAccountingBinaryCapability()); err != nil {
		t.Fatalf("compatible restart must verify: %v", err)
	}
	// Pending provider evidence survived the restart.
	sealedFwd, err := fwdLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	pendingState, err := reopened.GetProviderCostWorkState(ctx, sealedFwd.Key)
	if err != nil {
		t.Fatalf("pending provider evidence must survive restart: %v", err)
	}
	if pendingState.Status != "pending" {
		t.Fatalf("restarted provider work status = %q, want pending", pendingState.Status)
	}
	// Drain customer + provider + economic work exactly once.
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(reopened, reopened, f3ProviderStub{}, reopened, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("restarted provider drain: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(reopened, reopened, f3RatingStub{t: t, charge: 120}, reopened, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("restarted customer drain: %v", err)
	}
	econWorker := f2bProviderWorker(t, reopened)
	if err := econWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("restarted economic drain: %v", err)
	}
	acctAfter, err := reopened.GetAccount(ctx, "acct-rec174-fwd")
	if err != nil {
		t.Fatal(err)
	}
	if acctAfter.BalanceNano != 100000-120 {
		t.Fatalf("restarted drain balance = %d, want %d", acctAfter.BalanceNano, 100000-120)
	}
	econJournals := f2bProviderJournals(t, reopened, "acct-rec174-fwd")
	if econJournals != 2 {
		t.Fatalf("restarted provider journals = %d, want 2 (call COGS + economic revision)", econJournals)
	}
	// Replay after recovery posts nothing.
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider replay after recovery: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer replay after recovery: %v", err)
	}
	if err := econWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("economic replay after recovery: %v", err)
	}
	acctReplay, err := reopened.GetAccount(ctx, "acct-rec174-fwd")
	if err != nil {
		t.Fatal(err)
	}
	if acctReplay.BalanceNano != acctAfter.BalanceNano {
		t.Fatalf("replay moved balance %d -> %d", acctAfter.BalanceNano, acctReplay.BalanceNano)
	}
	if n := f2bProviderJournals(t, reopened, "acct-rec174-fwd"); n != econJournals {
		t.Fatalf("replay moved provider journals %d -> %d", econJournals, n)
	}
}

func TestRecovery174RetentionPrunePreservesLinkageAndRecovery(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "rec174-retain")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-rec174-ret", 100000)
	f3ActivateEmpty(t, store)
	// Posted V2 call: provider work reaches processed.
	postedID := f3MustCallID(t)
	posted := testIndependentCallUsageFor(postedID, []string{"b-ret"})
	posted.AccountID = "acct-rec174-ret"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-ret", CallID: postedID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: posted.CustomerPricingRef, ChargePolicyRef: posted.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, posted, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	postedLeg := testIndependentCallLegFor(postedID, "b-ret")
	if err := store.AppendCallLegUsageWithOwner(ctx, postedLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 120}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	sealedPosted, err := postedLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	processedState, err := store.GetProviderCostWorkState(ctx, sealedPosted.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processedState.Status != "processed" {
		t.Fatalf("posted provider work status = %q, want processed", processedState.Status)
	}
	// Pending V2 call: provider work must survive retention expiry.
	pendingID := f3MustCallID(t)
	pending := testIndependentCallUsageFor(pendingID, []string{"b-retp"})
	pending.AccountID = "acct-rec174-ret"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-ret", CallID: pendingID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: pending.CustomerPricingRef, ChargePolicyRef: pending.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, pending, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	pendingLeg := testIndependentCallLegFor(pendingID, "b-retp")
	if err := store.AppendCallLegUsageWithOwner(ctx, pendingLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	sealedPending, err := pendingLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	journalsBefore := f3JournalCount(t, store, "acct-rec174-ret")
	balanceBefore := f3Balance(t, store, "acct-rec174-ret")
	// Expire processed operational queue rows through the supported
	// retention path (the same prune production runs after terminal appends,
	// here with an explicit operator cutoff). Sealed financial facts, pins,
	// journals and heads are never touched by this path.
	if err := store.pruneProcessedProviderCostWork(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("supported retention prune: %v", err)
	}
	if _, err := store.GetProviderCostWorkState(ctx, sealedPosted.Key); !errors.Is(err, ErrUsageRecordNotFound) {
		t.Fatalf("processed queue row must expire, got %v", err)
	}
	pendingAfter, err := store.GetProviderCostWorkState(ctx, sealedPending.Key)
	if err != nil {
		t.Fatalf("pending provider evidence must survive retention expiry: %v", err)
	}
	if pendingAfter.Status != "pending" {
		t.Fatalf("pending status after prune = %q, want pending", pendingAfter.Status)
	}
	// Minimum canonical financial/audit linkage survives expiry.
	if n := f3JournalCount(t, store, "acct-rec174-ret"); n != journalsBefore {
		t.Fatalf("journals after prune = %d, want %d", n, journalsBefore)
	}
	if got := f3Balance(t, store, "acct-rec174-ret"); got != balanceBefore {
		t.Fatalf("balance after prune = %d, want %d", got, balanceBefore)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-rec174-ret", postedID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("completed V2 pin must survive prune: %v", err)
	}
	if !pin.IsCompleted() || pin.Owner != billing.PostingOwnerV2 {
		t.Fatalf("pin must stay V2 completed, got %#v", pin)
	}
	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasV2 {
		t.Fatalf("V2 postings must still be detected after retention expiry")
	}
	if _, err := store.GetCallUsage(ctx, postedID); err != nil {
		t.Fatalf("sealed posted call must survive prune: %v", err)
	}
	// Compatible recovery after expiry: remaining pending drains once,
	// posted work replays with zero effects.
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider drain after prune: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer drain after prune: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-ret"); got != balanceBefore-120 {
		t.Fatalf("drain after prune balance = %d, want %d", got, balanceBefore-120)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider replay after prune: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer replay after prune: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-ret"); got != balanceBefore-120 {
		t.Fatalf("replay after prune moved balance to %d", got)
	}
}
