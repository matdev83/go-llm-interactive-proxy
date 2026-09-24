package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	_ "modernc.org/sqlite"
)

// Task 17.4 RED: runtime startup compatibility and strict-admission quiesce
// (Migration Strategy step 7, requirements 10.5, 17.5, 18.4).
//
// A process must prove compatible accounting format/epoch-reader capability
// before serving; otherwise startup fails with ErrAccountingStaleBinary or
// the operator runs explicitly quiesced (every strict admission denied with
// ErrAccountingStrictQuiesced and zero new effects). Forward-compatible
// current binaries serve V1 and V2 stores; legacy stores and doubles without
// the snapshot port keep prior behavior.

var recoveryRuntimeTestSequence atomic.Int64

func newRecoveryRuntimeStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:recovery-runtime-174-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", recoveryRuntimeTestSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func recoveryRuntimeAccount(t *testing.T, store *billingstore.DurableStore, accountID string) {
	t.Helper()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
}

func recoveryRuntimeActivateEmpty(t *testing.T, store *billingstore.DurableStore) {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "rec174-rt-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "rec174-rt-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "rec174-rt-activate"); err != nil {
		t.Fatal(err)
	}
}

func recoveryRuntimeClosure(callID billing.BillingCallID, accountID string, bLegs []string) billing.CallUsageRecord {
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: "a-shared", SessionID: "sess-shared",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    bLegs,
	}
}

func recoveryRuntimeLeg(callID billing.BillingCallID, bLegID string) billing.CallLegUsageRecord {
	return billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-shared", BLegID: bLegID, AttemptSeq: 1,
		BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
		Outcome:  billing.LegOutcomeWinner,
		Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 7, Present: true},
			OutputTokens: billing.Quantity{Value: 3, Present: true},
			Cost:         billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
			DedupeKey:    "provider-charge-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

type recoveryRuntimeRater struct {
	t      *testing.T
	charge int64
}

func (s recoveryRuntimeRater) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return f3rbBoundResult(s.t, complete.Closure, s.charge), nil
}

func (s recoveryRuntimeRater) ResolveCallRatingForOwner(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure, _ string) (billing.CallRatingResult, error) {
	return f3rbBoundResult(s.t, complete.Closure, s.charge), nil
}

type recoveryRuntimeProvider struct{}

func (recoveryRuntimeProvider) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 9, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

func recoveryRuntimePostV2(t *testing.T, store *billingstore.DurableStore, accountID string) billing.BillingCallID {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	closure := recoveryRuntimeClosure(callID, accountID, []string{"b-rt"})
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, recoveryRuntimeLeg(callID, "b-rt"), billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, recoveryRuntimeProvider{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, recoveryRuntimeRater{t: t, charge: 120}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	return callID
}

type recoveryStubAdmission struct {
	admits int
	fail   error
}

func (s *recoveryStubAdmission) Admit(_ context.Context, _ coreRuntime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	s.admits++
	if s.fail != nil {
		return billing.CallExposure{}, s.fail
	}
	return billing.CallExposure{AccountID: "acct-stub"}, nil
}

func TestRecovery174StartupVerifiesCompatibleBinary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stale := billing.AccountingBinaryCapability{SupportsV1Reader: true}
	current := billing.CurrentAccountingBinaryCapability()

	fresh := newRecoveryRuntimeStore(t, "rec174-rt-fresh")
	snapshot, err := runtimebundle.VerifyBillingAccountingRecovery(ctx, fresh, stale)
	if err != nil {
		t.Fatalf("fresh store must verify stale binary: %v", err)
	}
	if snapshot.HasV2MonetaryPosting || snapshot.MarkerFound {
		t.Fatalf("fresh snapshot mismatch: %#v", snapshot)
	}
	if _, err := runtimebundle.VerifyBillingAccountingRecovery(ctx, fresh, current); err != nil {
		t.Fatalf("fresh store must verify current binary: %v", err)
	}

	posted := newRecoveryRuntimeStore(t, "rec174-rt-posted")
	recoveryRuntimeAccount(t, posted, "acct-rec174-rt")
	recoveryRuntimeActivateEmpty(t, posted)
	recoveryRuntimePostV2(t, posted, "acct-rec174-rt")
	if _, err := runtimebundle.VerifyBillingAccountingRecovery(ctx, posted, current); err != nil {
		t.Fatalf("V2-posted store must verify current binary: %v", err)
	}
	if _, err := runtimebundle.VerifyBillingAccountingRecovery(ctx, posted, stale); !errors.Is(err, billing.ErrAccountingStaleBinary) {
		t.Fatalf("V2-posted store must reject stale binary, got %v", err)
	}
	if _, err := runtimebundle.VerifyBillingAccountingRecovery(ctx, nil, current); err == nil {
		t.Fatalf("nil store must fail verification")
	}
}

func TestRecovery174QuiescedAdmissionDeniesStrictWithZeroEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stale := billing.AccountingBinaryCapability{SupportsV1Reader: true}
	current := billing.CurrentAccountingBinaryCapability()

	posted := newRecoveryRuntimeStore(t, "rec174-rt-quiesce")
	recoveryRuntimeAccount(t, posted, "acct-rec174-rtq")
	recoveryRuntimeActivateEmpty(t, posted)
	recoveryRuntimePostV2(t, posted, "acct-rec174-rtq")
	snapshot, err := posted.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasV2MonetaryPosting {
		t.Fatalf("posted store must carry V2 postings")
	}
	inner := &recoveryStubAdmission{}
	quiesced, err := runtimebundle.NewQuiescedStrictAdmission(inner, snapshot, stale)
	if err != nil {
		t.Fatalf("quiesce compose: %v", err)
	}
	if !quiesced.Quiesced() {
		t.Fatalf("stale capability on V2 postings must quiesce")
	}
	pinsBefore := countPostingPins(t, posted)
	if _, err := quiesced.Admit(ctx, coreRuntime.BillingExposureAdmissionInput{}); !errors.Is(err, billing.ErrAccountingStrictQuiesced) {
		t.Fatalf("quiesced Admit must fail with ErrAccountingStrictQuiesced, got %v", err)
	}
	if inner.admits != 0 {
		t.Fatalf("quiesced Admit reached inner admission %d times (must be zero effects)", inner.admits)
	}
	if n := countPostingPins(t, posted); n != pinsBefore {
		t.Fatalf("quiesced Admit created pins %d -> %d", pinsBefore, n)
	}
	// A compatible binary on the same snapshot is not quiesced and delegates.
	open, err := runtimebundle.NewQuiescedStrictAdmission(inner, snapshot, current)
	if err != nil {
		t.Fatalf("open compose: %v", err)
	}
	if open.Quiesced() {
		t.Fatalf("current capability must not quiesce")
	}
	if _, err := open.Admit(ctx, coreRuntime.BillingExposureAdmissionInput{}); err != nil {
		t.Fatalf("open Admit must delegate: %v", err)
	}
	if inner.admits != 1 {
		t.Fatalf("open Admit must delegate exactly once, got %d", inner.admits)
	}
	// Quiesce verdict is deterministic: same inputs always agree.
	again, err := runtimebundle.NewQuiescedStrictAdmission(&recoveryStubAdmission{}, snapshot, stale)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Quiesced() {
		t.Fatalf("quiesce verdict must be deterministic")
	}
}

func TestRecovery174QuiesceComposeFailsClosed(t *testing.T) {
	t.Parallel()
	snapshot := billing.AccountingRecoverySnapshot{}
	if _, err := runtimebundle.NewQuiescedStrictAdmission(&recoveryStubAdmission{}, snapshot, billing.CurrentAccountingBinaryCapability()); err == nil {
		t.Fatalf("quiesce without store scope must fail closed")
	}
	posted := newRecoveryRuntimeStore(t, "rec174-rt-qnil")
	recoveryRuntimeAccount(t, posted, "acct-rec174-rtqn")
	recoveryRuntimeActivateEmpty(t, posted)
	good, err := posted.GetAccountingRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimebundle.NewQuiescedStrictAdmission(nil, good, billing.CurrentAccountingBinaryCapability()); err == nil {
		t.Fatalf("nil inner admission must fail closed")
	}
	if _, err := runtimebundle.NewQuiescedStrictAdmission(&recoveryStubAdmission{}, good, billing.AccountingBinaryCapability{}); err == nil {
		t.Fatalf("zero capability must fail closed")
	}
}

func countPostingPins(t *testing.T, store *billingstore.DurableStore) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ?`, store.StoreID()).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
