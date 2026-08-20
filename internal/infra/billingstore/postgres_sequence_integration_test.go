//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestPostgresCallLegSequencePersistence proves SQLite/PostgreSQL parity for
// the nullable attempt_seq migration: explicit sequence persistence and
// restore, sequence-uniqueness conflicts within one call, replay conflicts on a
// changed sequence, and legacy NULL-sequence row readability under the old v1
// fingerprint contract. Skips cleanly when the integration DSN is absent.
func TestPostgresCallLegSequencePersistence(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "sequence-postgres"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	runAppendCallLegUsagePersistsAttemptSequence(t, store)
	runAppendCallLegUsageRejectsDuplicateAttemptSequenceWithinCall(t, store)
	runAppendCallLegUsageReplayConflictOnChangedSequence(t, store)
	runLegacyNullAttemptSequenceRowsRemainReadable(t, store)
	runBillingStoreContract(t, store, fmt.Sprintf("sequence-contract-%d", time.Now().UnixNano()))
}
