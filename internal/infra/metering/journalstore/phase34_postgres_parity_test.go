//go:build integration

package journalstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase34_Postgres_DirectPooled_CorrectionAggregateIdentical(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	ensureDirectJournalSchema(t, adminDSN)
	ensurePooledJournalSchema(t, adminDSN)

	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("p34-parity")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})

	direct := newPostgresJournal(t, adminDSN, storeID)
	seedPhase34CorrectionStream(t, direct)

	pooled, _ := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)
	pageDirect, err := direct.List(ctx, metering.Query{StreamID: "stream-parity", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	pagePooled, err := pooled.List(ctx, metering.Query{StreamID: "stream-parity", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	snapD, err := aggregate.Apply(pageDirect.Facts)
	if err != nil {
		t.Fatal(err)
	}
	snapP, err := aggregate.Apply(pagePooled.Facts)
	if err != nil {
		t.Fatal(err)
	}
	if snapD.Quantities[metering.ComponentOutputToken] != 7 ||
		snapP.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("direct=%v pooled=%v want output=7", snapD.Quantities, snapP.Quantities)
	}
}

func TestPhase34_Postgres_VerifySchemaV2(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
	if err := journalstore.VerifySchema(context.Background(), bunDB); err != nil {
		t.Fatalf("VerifySchema V2: %v", err)
	}
}

func TestPhase34_PostgresPooled_VerifySchemaFailsWhenV2IndexMissing(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	pooledJournalTestMu.Lock()
	t.Cleanup(pooledJournalTestMu.Unlock)
	ensurePooledJournalSchema(t, adminDSN)
	runtime := sharedPooledJournalRuntime(t, runtimeDSN)
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })

	for _, name := range journalstore.V2BoundedIndexNames {
		t.Run(name, func(t *testing.T) {
			restoreSQL := restoreV2IndexSQL(name)
			t.Cleanup(func() {
				restoreCtx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
				defer cancel()
				_, _ = admin.ExecContext(restoreCtx, restoreSQL)
			})
			mutateCtx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
			defer cancel()
			if _, err := admin.ExecContext(mutateCtx, `DROP INDEX IF EXISTS `+name); err != nil {
				t.Fatalf("drop %s: %v", name, err)
			}
			verifyCtx, verifyCancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
			defer verifyCancel()
			err := journalstore.VerifySchema(verifyCtx, runtime)
			if err == nil {
				t.Fatalf("VerifySchema must fail after dropping %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("VerifySchema error %q must mention %s", err.Error(), name)
			}
			_, _ = admin.ExecContext(mutateCtx, restoreSQL)
		})
	}
}

func TestPhase34_Postgres_ConcurrentFactIdentityAppendRace(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)
	storeID := testkit.UniquePostgresStoreID("p34-race-direct")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	runFactIdentityRace(t, store, storeID+"-stream", storeID+"-fact")
}

func TestPhase34_PostgresPooled_ConcurrentFactIdentityAppendRace(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	ensurePooledJournalSchema(t, adminDSN)
	storeID := testkit.UniquePostgresStoreID("p34-race-pooled")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})
	store, _ := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)
	runFactIdentityRace(t, store, storeID+"-stream", storeID+"-fact")
}

func restoreV2IndexSQL(name string) string {
	switch name {
	case "metering_facts_store_stream_fact_id_key":
		return `CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_stream_fact_id_key ON metering_facts(store_id, stream_id, fact_id)`
	case "idx_metering_facts_store_stream_seq":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_stream_seq ON metering_facts(store_id, stream_id, sequence)`
	case "idx_metering_facts_store_attempt":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_attempt ON metering_facts(store_id, attempt_id) WHERE attempt_id <> ''`
	case "idx_metering_facts_store_recorded":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_recorded ON metering_facts(store_id, recorded_at_unix)`
	case "idx_metering_facts_store_plane":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_plane ON metering_facts(store_id, perspective, boundary, lifecycle_scope)`
	case "idx_metering_fact_supersessions_to":
		return `CREATE INDEX IF NOT EXISTS idx_metering_fact_supersessions_to ON metering_fact_supersessions(store_id, stream_id, to_fact_id)`
	default:
		return `SELECT 1`
	}
}

func seedPhase34CorrectionStream(t *testing.T, store *journalstore.DurableStore) {
	t.Helper()
	ctx := context.Background()
	base := validFact("base-parity", "stream-parity", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "base-parity"
	base.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 10, Present: true,
	}}
	corr := validFact("corr-parity", "stream-parity", 2)
	corr.Kind = metering.FactKindCorrection
	corr.IdentityVersion = metering.IdentityVersionV1
	corr.SourceID = "corr-parity"
	corr.Supersedes = []string{"base-parity"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: -3, Present: true,
	}}
	if err := store.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, corr); err != nil {
		t.Fatal(err)
	}
}
