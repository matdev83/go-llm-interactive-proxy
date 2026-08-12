package billingstore

import (
	"context"
	"testing"
)

func TestPhase6OpeningAndSnapshotImmutability(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := phase6SchemaUp(ctx, store.db); err != nil {
		t.Fatalf("phase6 idempotent: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	journalAccount(t, store, "phase6-opening")
	if _, err := store.db.NewRaw(`UPDATE billing_account_openings SET opening_balance_nano = 1 WHERE account_id = ?`, "phase6-opening").Exec(ctx); err == nil {
		t.Fatal("opening-balance row must be immutable")
	}
	if _, err := store.db.NewRaw(`DELETE FROM billing_account_openings WHERE account_id = ?`, "phase6-opening").Exec(ctx); err == nil {
		t.Fatal("opening-balance delete must be rejected")
	}
}
