//go:build integration

package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// TestStatementImportLedgerPostgresDirect proves the durable statement import
// ledger contract on direct PostgreSQL with the same atomic replay/conflict,
// scope, immutability and restart behavior as SQLite.
func TestStatementImportLedgerPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "statement-pg"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("schema and indexes exist", func(t *testing.T) {
		require.Contains(t, RequiredMigrationNames, BillingStatementImportMigrationName)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingStatementImportMigrationName).Scan(ctx, &count))
		require.Equal(t, 1, count)
		for _, index := range []string{
			billingStatementRevisionScopeIndex, billingStatementRevisionTenantIndex,
			billingStatementLineStatementIndex, billingStatementLineScopeIndex,
		} {
			var name string
			require.NoError(t, store.db.NewRaw(`SELECT indexname FROM pg_indexes WHERE tablename IN ('billing_statement_revisions','billing_statement_lines') AND indexname = ?`, index).Scan(ctx, &name))
			require.Equal(t, index, name)
		}
		for _, table := range []string{"billing_statement_revisions", "billing_statement_lines"} {
			var name string
			require.NoError(t, store.db.NewRaw(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(ctx, &name))
			require.Equal(t, table, name)
		}
	})

	normalized := statementImportNormalized(t, "statement-pg", "statement-1", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	fingerprint, found, err := store.LookupStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, normalized.Fingerprint, fingerprint)

	identities := []economics.StatementLineIdentity{normalized.Lines[0].Identity, normalized.Lines[1].Identity}
	retained, err := store.LookupStatementLines(ctx, identities)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	require.Equal(t, normalized.Lines[0].Fingerprint, retained[identities[0].Key()])

	require.NoError(t, store.AppendStatementRevision(ctx, normalized), "exact replay must be a no-op")
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	statementConflict := statementImportNormalized(t, "statement-pg", "statement-1", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, statementConflict), billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	// A changed restatement under the same line revision must roll back the
	// whole statement revision with no partial rows.
	revisionConflict := statementImportNormalized(t, "statement-pg", "statement-1", 2,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-3", Revision: 1, Amount: "5"},
	)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, revisionConflict), billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	accepted := statementImportNormalized(t, "statement-pg", "statement-1", 2,
		statementImportLineSpec{ID: "line-1", Revision: 2, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, accepted))
	require.Equal(t, 2, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 3, statementImportLineRows(t, store))

	got, err := store.GetStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.Equal(t, normalized.Fingerprint, got.Fingerprint)
	require.Len(t, got.Lines, 2)

	// Restart over the same database handle preserves immutable rows and the
	// replay identity without in-memory state.
	reopened, err := openStore(ctx, store.db, Config{StoreID: "statement-pg"})
	require.NoError(t, err)
	require.NoError(t, reopened.AppendStatementRevision(ctx, normalized))
	require.Equal(t, 2, statementImportRevisionCount(t, store, "statement-1"))

	// Retained rows are immutable on PostgreSQL.
	_, err = store.db.ExecContext(ctx, `UPDATE billing_statement_revisions SET fingerprint = 'forged' WHERE store_id = 'statement-pg'`)
	require.Error(t, err)
	_, err = store.db.ExecContext(ctx, `DELETE FROM billing_statement_lines WHERE store_id = 'statement-pg'`)
	require.Error(t, err)

	// Foreign-store identities fail closed and never discover other stores'
	// rows.
	foreign := statementImportNormalized(t, "statement-pg-other", "statement-1", 1)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, foreign), ErrEconomicsOutOfScope)
	_, _, err = store.LookupStatementRevision(ctx, foreign.Identity)
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	_, err = store.GetStatementRevision(ctx, foreign.Identity)
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	other, err := openStore(ctx, store.db, Config{StoreID: "statement-pg-other"})
	require.NoError(t, err)
	_, err = other.GetStatementRevision(ctx, normalized.Identity)
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	_, err = other.GetStatementRevision(ctx, economics.StatementIdentity{
		StoreID: "statement-pg-other", ProviderAccountKey: "provider-account", StatementID: "statement-1",
		PeriodID: "period-1", Revision: 1,
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestStatementImportLedgerPostgresConcurrentAtomicity proves concurrent
// identical imports produce one durable effect and racing conflicts leave no
// mixed state on direct PostgreSQL.
func TestStatementImportLedgerPostgresConcurrentAtomicity(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "statement-pg-race"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	identical := statementImportNormalized(t, "statement-pg-race", "statement-identical", 1)
	const workers = 4
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = store.AppendStatementRevision(ctx, identical)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "identical worker %d", i)
	}
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-identical"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	left := statementImportNormalized(t, "statement-pg-race", "statement-conflict", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "1.25"})
	right := statementImportNormalized(t, "statement-pg-race", "statement-conflict", 2,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"})
	errs = make([]error, 2)
	start = make(chan struct{})
	for i, record := range []billing.NormalizedStatement{left, right} {
		wg.Add(1)
		go func(index int, statement billing.NormalizedStatement) {
			defer wg.Done()
			<-start
			errs[index] = store.AppendStatementRevision(ctx, statement)
		}(i, record)
	}
	close(start)
	wg.Wait()
	successes := 0
	conflicts := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, billing.ErrStatementImportConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent append error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-conflict"))
	require.Equal(t, 3, statementImportLineRows(t, store))

	// A poisoned retained line must fail the self-validating read instead of
	// being returned as evidence on PostgreSQL.
	drift := statementImportNormalized(t, "statement-pg-race", "statement-drift", 1)
	envelope := mustMarshalStatementEnvelope(t, drift)
	insertRawStatementRevision(t, store, "statement-pg-race", drift.Identity.Key(), "statement-drift",
		envelope, drift.Fingerprint, "tenant-1", "principal-1")
	insertRawStatementLine(t, store, "statement-pg-race", drift.Identity.Key(), drift, 0, strings.Repeat("0", 64))
	_, err = store.GetStatementRevision(ctx, drift.Identity)
	require.ErrorIs(t, err, ErrStatementImportMismatch)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, drift), ErrStatementImportMismatch)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-drift"))
}

func mustMarshalStatementEnvelope(t *testing.T, normalized billing.NormalizedStatement) []byte {
	t.Helper()
	payloads, err := buildStatementImportPayloads(normalized)
	require.NoError(t, err)
	return []byte(payloads.envelopeJSON)
}

// TestStatementImportLedgerPostgresForeignLineTenantRejectsAtomically proves a
// trusted tenant-1 batch containing any tenant-2 line (matched or unmatched)
// fails with a typed scope error before ledger append and leaves zero
// statement/line rows on direct PostgreSQL. Exact valid replay remains green.
func TestStatementImportLedgerPostgresForeignLineTenantRejectsAtomically(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		lineIndex int
	}{
		{name: "matched", lineIndex: 0},
		{name: "unmatched", lineIndex: 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
			store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "statement-pg-tenant-scope"})
			require.NoError(t, err)
			defer func() { _ = store.Close() }()
			require.NoError(t, VerifySchema(ctx, store.db))

			scope := billing.TrustedStatementScope{
				StoreID: "statement-pg-tenant-scope", TenantID: "tenant-1", PrincipalID: "principal-1",
				ProviderAccountKeys: []string{"provider-account"},
			}
			valid := tenantScopeTestBatch(t, "statement-pg-tenant-scope", "statement-tenant-scope")
			foreign := tenantScopeTestBatch(t, "statement-pg-tenant-scope", "statement-tenant-scope")
			foreign.Lines[tc.lineIndex].Subject.TenantID = "tenant-2"

			service, err := billing.NewStatementImportService(store)
			require.NoError(t, err)
			_, err = service.Import(ctx, scope, foreign)
			require.ErrorIs(t, err, billing.ErrStatementImportScopeMismatch)
			require.Equal(t, 0, statementImportRevisionCount(t, store, "statement-tenant-scope"))
			require.Equal(t, 0, statementImportLineRows(t, store))

			result, err := service.Import(ctx, scope, valid)
			require.NoError(t, err)
			require.Equal(t, []string{"line-1"}, result.Accepted)
			require.Equal(t, []string{"line-2"}, result.Unmatched)
			require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-tenant-scope"))
			require.Equal(t, 2, statementImportLineRows(t, store))
		})
	}
}
