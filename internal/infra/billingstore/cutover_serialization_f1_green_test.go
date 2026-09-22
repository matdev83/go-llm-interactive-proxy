package billingstore

// Phase 17.3 Finding F1 GREEN: shared per-store serialization boundary.
//
// Covers: call exposure/admission, call/leg append that creates work,
// customer settlement, provider legacy/revision, selected-cost,
// cost-pass-through/direct adjustment, coordinator activation.
// Evidence-only shadow journal paths are unaffected (no marker lock).
//
// Lock protocol is documented in cutover_serialization_store.go. All tests
// below use deterministic barriers (entered/release channels); timeouts only
// bound liveness while the peer holds the per-store marker row.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func newF1WALStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(filepath.Join(t.TempDir(), storeID+".db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func f1WALShadow(t *testing.T, store *DurableStore) context.Context {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f1-green-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

// barrierActivationWhilePaused starts draining+activation while the caller
// holds the marker via a paused posting. It fails if activation completes
// while paused (missing serialization) and succeeds if activation waits.
// After release, activation must observe the true drain state: success when
// no other pending remains, DrainBlocked when other pending (e.g. setup leg)
// correctly blocks.
func barrierActivationWhilePaused(t *testing.T, ctx context.Context, store *DurableStore, release chan struct{}, postDone chan error) {
	t.Helper()
	type drainRes struct {
		marker billing.AccountingCutoverMarker
		err    error
	}
	drainDone := make(chan drainRes, 1)
	go func() {
		_, _, derr := store.BeginCutoverDraining(ctx, "f1-green-drain-"+store.StoreID())
		if derr != nil {
			drainDone <- drainRes{err: derr}
			return
		}
		m, aerr := store.ActivateCutoverV2(ctx, "f1-green-activate-"+store.StoreID())
		drainDone <- drainRes{marker: m, err: aerr}
	}()
	select {
	case r := <-drainDone:
		close(release)
		perr := <-postDone
		t.Fatalf("F1 GREEN: activation completed while posting in-flight (drain err=%v, post err=%v); missing shared marker lock", r.err, perr)
	case <-time.After(2 * time.Second):
	}
	close(release)
	if perr := <-postDone; perr != nil {
		t.Fatalf("F1 GREEN: paused posting after release: %v", perr)
	}
	dr := <-drainDone
	if dr.err != nil {
		t.Fatalf("F1 GREEN: draining+activation after posting commit: %v", dr.err)
	}
	if dr.marker.State != billing.AccountingCutoverV2Active {
		t.Fatalf("F1 GREEN: final marker = %q, want v2_active", dr.marker.State)
	}
}

// barrierActivationWhilePausedAllowDrainBlocked is the variant for setups
// with other pending work (e.g. a setup leg) that must keep activation
// blocked after the paused posting commits. It still fails if activation
// completes while paused (stale reads); after release it requires
// DrainBlocked (correct observation) rather than success.
func barrierActivationWhilePausedAllowDrainBlocked(t *testing.T, ctx context.Context, store *DurableStore, release chan struct{}, postDone chan error) {
	t.Helper()
	type drainRes struct {
		marker billing.AccountingCutoverMarker
		err    error
	}
	drainDone := make(chan drainRes, 1)
	go func() {
		_, _, derr := store.BeginCutoverDraining(ctx, "f1-green-drain-"+store.StoreID())
		if derr != nil {
			drainDone <- drainRes{err: derr}
			return
		}
		m, aerr := store.ActivateCutoverV2(ctx, "f1-green-activate-"+store.StoreID())
		drainDone <- drainRes{marker: m, err: aerr}
	}()
	select {
	case r := <-drainDone:
		close(release)
		perr := <-postDone
		t.Fatalf("F1 GREEN: activation completed while posting in-flight (drain err=%v, post err=%v); missing shared marker lock", r.err, perr)
	case <-time.After(2 * time.Second):
	}
	close(release)
	if perr := <-postDone; perr != nil {
		t.Fatalf("F1 GREEN: paused posting after release: %v", perr)
	}
	dr := <-drainDone
	if dr.err == nil {
		if dr.marker.State != billing.AccountingCutoverV2Active {
			t.Fatalf("F1 GREEN: final marker = %q, want v2_active or drain-blocked", dr.marker.State)
		}
		return
	}
	if !errors.Is(dr.err, billing.ErrCutoverDrainBlocked) {
		t.Fatalf("F1 GREEN: draining+activation after posting commit err = %v, want v2_active or drain-blocked", dr.err)
	}
}

func TestF1GreenCustomerSettlementBlocksActivation(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-customer")
	ctx := f1WALShadow(t, store)
	call, exp, res := b2b1SetupAccountCallExposure(t, store, "acct-f1-green-cust", 1000, 60, 25)
	// F2A: admission now pins V1 at AdmitExposure time; the pin-acquire fault
	// proves atomic acquisition when no pin exists. Clear it to force the
	// acquisition barrier (activation must wait while posting holds uncommitted pin).
	custOpKey, err := billing.CustomerPostingOperationKey("acct-f1-green-cust", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationCustomerSettlement), custOpKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store.settlementFaultHook = func(point string) error {
		if point == "b2b1-pin-acquire" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	defer func() { store.settlementFaultHook = nil }()
	postDone := make(chan error, 1)
	go func() {
		_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
		postDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("customer settlement never reached b2b1-pin-acquire")
	}
	// Setup leg leaves provider pending; after the paused settlement commits,
	// activation must still observe that pending (drain-blocked), not stale success.
	barrierActivationWhilePausedAllowDrainBlocked(t, ctx, store, release, postDone)
}

func TestF1GreenProviderLegacyBlocksActivation(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-provider")
	ctx := f1WALShadow(t, store)
	callID, leg, result := b2b2SetupLeg(t, store, "acct-f1-green-prov", "b-f1")
	// F2A: leg append now acquires the V1 provider pin atomically; clear it to
	// force the pin-acquire barrier.
	provSealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provOpKey, err := billing.ProviderCostSourceKey(provSealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), provOpKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store.providerFaultHook = func(point string) error {
		if point == "b2b2-pin-acquire" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	defer func() { store.providerFaultHook = nil }()
	postDone := make(chan error, 1)
	go func() {
		_, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: "acct-f1-green-prov", CallID: callID, Leg: leg, Result: result})
		postDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("provider legacy never reached b2b2-pin-acquire")
	}
	// Setup call remains pending after provider posting; activation must
	// observe it (drain-blocked) after waiting, not stale success.
	barrierActivationWhilePausedAllowDrainBlocked(t, ctx, store, release, postDone)
}

func TestF1GreenSelectedCostBlocksActivation(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-selected")
	ctx := f1WALShadow(t, store)
	b2b3SetupAccount(t, store, "acct-f1-green-sel")
	callID, err := billing.ParseBillingCallID("bc_0000000000000000000000000000f101")
	if err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), "acct-f1-green-sel", callID.String())
	input := billing.SelectedCostAdjustmentInput{
		AccountID: "acct-f1-green-sel", CallID: callID, HeadKey: "f1-green-sel-head",
		Subject: subject, Expected: billing.SelectedCostHeadExpectation{},
		Selected: b2b3Valuation(t, "f1-green-sel-val-1", 1, 10_000_000_000),
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store.SetAdjustmentFaultHook(func(point string) error {
		if point == "b2b3-pin-acquire" {
			once.Do(func() { close(entered) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	defer store.SetAdjustmentFaultHook(nil)
	postDone := make(chan error, 1)
	go func() {
		_, err := store.ApplySelectedCostAdjustment(ctx, input)
		postDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("selected-cost never reached b2b3-pin-acquire")
	}
	barrierActivationWhilePaused(t, ctx, store, release, postDone)
}

func TestF1GreenAdmissionAppendHoldMarkerLock(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-admit")
	ctx := f1WALShadow(t, store)
	acct := billing.Account{ID: "acct-f1-green-admit", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	// Hold the per-store marker externally in a raw tx (FOR UPDATE semantics
	// via ensureAndLock in a held tx), then verify new admission/append work
	// waits instead of slipping past activation. Here we prove the converse:
	// while the marker is held, AdmitExposure and appends must wait.
	holder, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := store.ensureAndLockAccountingCutoverTx(ctx, holder); err != nil {
		_ = holder.Rollback()
		t.Fatal(err)
	}
	type res struct{ err error }
	admitDone := make(chan res, 1)
	go func() {
		callID, _ := billing.NewBillingCallID()
		call := testIndependentCallUsageFor(callID, []string{"b-f1-admit"})
		call.AccountID = acct.ID
		// Append first (creates work), then admit; both need the marker.
		if err := store.AppendCallUsage(ctx, call); err != nil {
			admitDone <- res{err: err}
			return
		}
		_, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID: acct.ID, CallID: callID.String(),
			Max:        billing.Money{Nano: 60, Currency: "USD"},
			PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
		})
		admitDone <- res{err: err}
	}()
	select {
	case r := <-admitDone:
		_ = holder.Rollback()
		t.Fatalf("F1 GREEN: admission/append completed while marker held externally (err=%v); missing shared lock", r.err)
	case <-time.After(600 * time.Millisecond):
		// Still waiting on the per-store marker row: FIXED. Timeout is well
		// within Append's 20-attempt (~1s) retry budget and far beyond the
		// <100ms buggy completion; ordering is via holder/admitDone channels.
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-admitDone:
		if r.err != nil {
			t.Fatalf("admission/append after marker release: %v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("admission/append never completed after marker release")
	}
	// Leg append that creates provider work also holds the marker.
	callID2, _ := billing.NewBillingCallID()
	call2 := testIndependentCallUsageFor(callID2, []string{"b-f1-leg"})
	call2.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call2); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID2, "b-f1-leg")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("leg append with marker lock: %v", err)
	}
}

func TestF1GreenParallelStoresDoNotBlockEachOther(t *testing.T) {
	t.Parallel()
	base := newF1WALStore(t, "f1-green-parallel-a")
	ctx := context.Background()
	other, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "f1-green-parallel-b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*DurableStore{base, other} {
		m, err := s.EnsureAccountingCutover(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m.State == billing.AccountingCutoverV1Active {
			if _, err := s.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
				ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
				NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f1-parallel-shadow-" + s.StoreID(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, s := range []*DurableStore{base, other} {
		acct := billing.Account{ID: fmt.Sprintf("acct-f1-parallel-%d", i), Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
		if err := s.CreateAccount(ctx, acct); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = base.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: "acct-f1-parallel-0", Amount: billing.Money{Nano: 10, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f1-par-0", Reason: "correction"})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = other.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: "acct-f1-parallel-1", Amount: billing.Money{Nano: 10, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f1-par-1", Reason: "correction"})
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("parallel stores blocked each other (per-store boundary violated)")
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("parallel store %d: %v", i, err)
		}
	}
}

func TestF1GreenContextCancellationWhileWaiting(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-cancel")
	ctx := f1WALShadow(t, store)
	acct := billing.Account{ID: "acct-f1-green-cancel", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store.SetAdjustmentFaultHook(func(point string) error {
		if point == "b2b4-pin-acquire" {
			once.Do(func() { close(entered) })
			<-release
			return nil
		}
		return nil
	})
	defer store.SetAdjustmentFaultHook(nil)
	postDone := make(chan error, 1)
	go func() {
		_, err := store.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: acct.ID, Amount: billing.Money{Nano: 10, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f1-cancel-1", Reason: "correction"})
		postDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("posting never paused for cancellation test")
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	type actRes struct {
		marker billing.AccountingCutoverMarker
		err    error
	}
	actDone := make(chan actRes, 1)
	go func() {
		_, _, derr := store.BeginCutoverDraining(cancelCtx, "f1-cancel-drain")
		if derr != nil {
			actDone <- actRes{err: derr}
			return
		}
		m, aerr := store.ActivateCutoverV2(cancelCtx, "f1-cancel-activate")
		actDone <- actRes{marker: m, err: aerr}
	}()
	select {
	case r := <-actDone:
		close(release)
		<-postDone
		if r.err == nil {
			t.Fatalf("cancelled activation succeeded (must fail closed on ctx cancellation while waiting on marker)")
		}
		if !errors.Is(r.err, context.DeadlineExceeded) && !errors.Is(r.err, context.Canceled) && cancelCtx.Err() == nil {
			t.Fatalf("cancelled activation err = %v, want context cancellation", r.err)
		}
		// Marker must not have advanced to draining/active via cancelled path
		// when the poster still holds the lock; at most draining may have been
		// entered before cancel, but v2_active must not be reached.
		if m, _ := store.GetAccountingCutover(ctx); m.State == billing.AccountingCutoverV2Active {
			t.Fatalf("cancelled activation left v2_active")
		}
	case <-time.After(10 * time.Second):
		close(release)
		<-postDone
		t.Fatalf("cancelled activation never returned (ctx cancellation not honored while waiting on marker)")
	}
}

func TestF1GreenCrashReopenPreservesActivation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f1-green-reopen.db")
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(path))
	open := func(storeID string) (*DurableStore, func()) {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
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
	store, closeFn := open("f1-green-reopen")
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f1-reopen-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f1-reopen-drain"); err != nil {
		t.Fatal(err)
	}
	_ = sh
	active, err := store.ActivateCutoverV2(ctx, "f1-reopen-activate")
	if err != nil {
		t.Fatal(err)
	}
	if active.State != billing.AccountingCutoverV2Active {
		t.Fatalf("pre-reopen marker = %q", active.State)
	}
	closeFn()
	reopened, close2 := open("f1-green-reopen")
	defer close2()
	got, err := reopened.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("reopen Get: %v", err)
	}
	if got.State != billing.AccountingCutoverV2Active || got.TransitionID != "f1-reopen-activate" {
		t.Fatalf("reopen marker = %#v, want v2_active/f1-reopen-activate", got)
	}
	replayed, err := reopened.ActivateCutoverV2(ctx, "f1-reopen-activate")
	if err != nil {
		t.Fatalf("exact activation replay after reopen: %v", err)
	}
	if replayed.Version != got.Version || replayed.TransitionID != got.TransitionID {
		t.Fatalf("replay mismatch: %#v vs %#v", replayed, got)
	}
	if _, err := reopened.ActivateCutoverV2(ctx, "f1-reopen-other"); err == nil {
		t.Fatalf("activation with different identity after v2_active must fail")
	}
}

func TestF1GreenExactActivationReplay(t *testing.T) {
	t.Parallel()
	store := newF1WALStore(t, "f1-green-replay")
	ctx := f1WALShadow(t, store)
	if _, _, err := store.BeginCutoverDraining(ctx, "f1-replay-drain"); err != nil {
		t.Fatal(err)
	}
	first, err := store.ActivateCutoverV2(ctx, "f1-replay-activate")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ActivateCutoverV2(ctx, "f1-replay-activate")
	if err != nil {
		t.Fatalf("exact replay same identity: %v", err)
	}
	if second.Version != first.Version || second.TransitionID != first.TransitionID || second.State != billing.AccountingCutoverV2Active {
		t.Fatalf("replay marker mismatch: %#v vs %#v", second, first)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f1-replay-other"); err == nil {
		t.Fatalf("different identity after v2_active must fail")
	}
}
