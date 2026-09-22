//go:build integration

package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Task 17.4 PostgreSQL parity: V2 posting detection, recovery snapshot, and
// stale-binary rejection on configured direct PostgreSQL. Skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite in
// accounting_recovery_test.go is authoritative when unavailable.
func TestRecovery174PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "rec174-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stale := billing.AccountingBinaryCapability{SupportsV1Reader: true}
	current := billing.CurrentAccountingBinaryCapability()

	hasV2, err := store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatalf("fresh HasV2 postgres: %v", err)
	}
	if hasV2 {
		t.Fatalf("fresh postgres store must report no V2 postings")
	}
	acct := billing.Account{ID: "acct-rec174-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}
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
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "rec174-pg-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "rec174-pg-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "rec174-pg-activate"); err != nil {
		t.Fatalf("postgres activation: %v", err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	closure := testIndependentCallUsageFor(callID, []string{"b-pg"})
	closure.AccountID = "acct-rec174-pg"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-pg", CallID: callID.String(),
		Max:        billing.Money{Nano: 700, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-pg"), billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("postgres provider worker: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 110}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("postgres customer worker: %v", err)
	}
	hasV2, err = store.HasV2MonetaryPostings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasV2 {
		t.Fatalf("postgres posted V2 pipeline must report V2 postings")
	}
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.MarkerFound || snapshot.Marker.State != billing.AccountingCutoverV2Active || !snapshot.HasV2MonetaryPosting {
		t.Fatalf("postgres snapshot mismatch: %#v", snapshot)
	}
	if err := billing.CheckCaptureRollbackAllowed(snapshot); !errors.Is(err, billing.ErrAccountingRollbackBlocked) {
		t.Fatalf("postgres V2 postings must block rollback, got %v", err)
	}
	if err := billing.CheckAccountingStartup(snapshot, stale); !errors.Is(err, billing.ErrAccountingStaleBinary) {
		t.Fatalf("postgres V2 postings must reject stale binary, got %v", err)
	}
	if err := billing.CheckAccountingStartup(snapshot, current); err != nil {
		t.Fatalf("postgres V2 postings must serve current binary: %v", err)
	}
	markerBefore, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyAccountingRecovery(ctx, stale); !errors.Is(err, billing.ErrAccountingStaleBinary) {
		t.Fatalf("postgres stale verify must fail, got %v", err)
	}
	markerAfter, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if markerAfter != markerBefore {
		t.Fatalf("postgres rejected startup mutated marker")
	}
	if _, err := store.VerifyAccountingRecovery(ctx, current); err != nil {
		t.Fatalf("postgres compatible verify: %v", err)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after recovery: %v", err)
	}
}
