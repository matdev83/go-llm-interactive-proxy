//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3B2a PostgreSQL parity: same draining/classify/activate contract on
// configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN
// is configured; the SQLite suite in cutover_coordinator_test.go is
// authoritative when PostgreSQL is unavailable.
func TestCutoverCoordinatorPostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b2a-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Ensure postgres: %v", err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: marker.Version, ExpectedEpoch: marker.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-pg-shadow"})
	if err != nil {
		t.Fatalf("shadow postgres: %v", err)
	}
	_ = shadow
	// Seed one V1 call+leg via ordinary appends (allowed pre-drain).
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-pg"})
	call.AccountID = "acct-b2a-pg"
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("AppendCallUsage postgres: %v", err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-pg")); err != nil {
		t.Fatalf("AppendCallLegUsage postgres: %v", err)
	}
	draining, status, err := store.BeginCutoverDraining(ctx, "b2a-pg-drain")
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
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after B2a: %v", err)
	}
}
