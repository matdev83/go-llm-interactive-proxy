//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 F6+F8 PostgreSQL parity: renewable mandatory claim authority on
// configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN
// is configured; SQLite suite is authoritative when PG is unavailable.
func TestF6F8PostgresRenewalParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f6f8-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f6f8-pg-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := billing.Account{ID: "acct-f6f8-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("AppendCallUsage pg: %v", err)
	}
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{
		Kind: billing.PostingOperationCustomerSettlement, AccountID: acct.ID, CallID: callID,
		Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: shadow.Version, ExpectedMarkerEpoch: shadow.Epoch,
	}); err != nil {
		t.Fatalf("acquire pg pin: %v", err)
	}
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("pg renewal must succeed, got: %v", err)
	}
	if meta.MarkerEpoch != shadow.Epoch || meta.Owner != billing.PostingOwnerV1 {
		t.Fatalf("pg token epoch %d owner %q, want %d V1", meta.MarkerEpoch, meta.Owner, shadow.Epoch)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema pg after F6F8: %v", err)
	}
}
