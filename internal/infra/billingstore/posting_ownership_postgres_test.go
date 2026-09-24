//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3B1 PostgreSQL parity: same durable posting-ownership pin contract
// on configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a
// DSN is configured; the SQLite suite in posting_ownership_test.go is
// authoritative when PostgreSQL is unavailable.
func TestPostingOwnershipPostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b1-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Ensure postgres: %v", err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-pg", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
	if err != nil {
		t.Fatalf("Acquire postgres: %v", err)
	}
	replayed, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-pg", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch})
	if err != nil {
		t.Fatalf("replay postgres: %v", err)
	}
	if replayed.OperationKey != pin.OperationKey {
		t.Fatalf("postgres replay mismatch")
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-b1-pg", callID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("Get postgres: %v", err)
	}
	if got.OperationKey != pin.OperationKey {
		t.Fatalf("postgres Get mismatch")
	}
	completed, err := store.CompletePostingPin(ctx, billing.CompletePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b1-pg", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch, CompletionOperationKey: pin.OperationKey, CompletionTransactionID: "tx-pg-1"})
	if err != nil {
		t.Fatalf("Complete postgres: %v", err)
	}
	if !completed.IsCompleted() {
		t.Fatalf("postgres completion must be completed")
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after pins: %v", err)
	}
}
