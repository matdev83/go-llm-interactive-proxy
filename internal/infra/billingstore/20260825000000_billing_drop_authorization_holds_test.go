package billingstore

import (
	"context"
	"strings"
	"testing"
)

func TestVerifySchemaRejectsRetiredAuthorizationHoldsTable(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	// Inject dummy authorization_holds table into fresh schema
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE authorization_holds (hold_key TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create dummy authorization_holds table: %v", err)
	}

	err := VerifySchema(ctx, store.db)
	if err == nil {
		t.Fatal("VerifySchema should have failed because retired authorization_holds table exists")
	}
	if !strings.Contains(err.Error(), "authorization_holds") {
		t.Fatalf("VerifySchema error %q should name retired authorization_holds", err.Error())
	}
}
