package billingstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.3A authorized retention linkage: only minimum safe sanitized
// evidence is retained, immutable financial/retention rows cannot be
// deleted without failing closed, and required selected-cost head, journal,
// adjustment and statement linkage survives close/reopen. No authorized
// deletion path exists for financial rows: the only DELETE statements in
// production code target non-financial work/outbox queues.

func retentionLinkageFileStore(t *testing.T, path string) *DurableStore {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	require.NoError(t, err)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	return store
}

func TestRetentionLinkageSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention-linkage.db")
	store := retentionLinkageFileStore(t, path)
	ctx := context.Background()

	account := edTestAccount(t, store, "rl-acct", "USD")
	callID := edTestCallID(t)
	orTestAdjustmentHead(t, store, account.ID, callID, "head-rl", "USD", 10_000_000_000)
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-rl", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	adjustmentsBefore, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: account.ID}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, adjustmentsBefore.Adjustments, 1)
	headBefore, err := store.GetSelectedCostHead(ctx, account.ID, callID, "head-rl")
	require.NoError(t, err)
	linesBefore, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, linesBefore.Lines, 2)
	require.NoError(t, store.Close())

	reopened := retentionLinkageFileStore(t, path)
	t.Cleanup(func() { _ = reopened.Close() })

	adjustmentsAfter, err := reopened.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: reopened.StoreID(), AccountID: account.ID}, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, adjustmentsBefore, adjustmentsAfter, "adjustment linkage must survive reopen")
	headAfter, err := reopened.GetSelectedCostHead(ctx, account.ID, callID, "head-rl")
	require.NoError(t, err)
	require.Equal(t, headBefore, headAfter, "selected-cost head linkage must survive reopen")
	require.Equal(t, adjustmentsAfter.Adjustments[0].OperationKey, headAfter.LastOperationKey,
		"adjustment operation must still resolve to the head operation")
	linesAfter, err := reopened.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: reopened.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, linesBefore, linesAfter, "statement linkage must survive reopen")
}

func TestFinancialTablesRejectDeletes(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	account := edTestAccount(t, store, "rl-del", "USD")
	callID := edTestCallID(t)
	orTestAdjustmentHead(t, store, account.ID, callID, "head-del", "USD", 10_000_000_000)
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-del", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))
	delSubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-del", callID.String(), "b-del")
	delObs := edTestObservation(t, "obs-del", metering.OriginProvider, "stream-del", 1, delSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	delRefs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), delObs)}
	require.NoError(t, store.AppendValuation(ctx, edTestValuation(t, "or-del-val", economics.BasisProviderReported, delSubject, delRefs, edTestCurrencyTotal(t, "USD", "1.32"))))
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-del", 1)))

	var journalTx string
	require.NoError(t, store.db.NewRaw(`SELECT transaction_id FROM journal_transactions WHERE account_id = ? LIMIT 1`, account.ID).Scan(ctx, &journalTx))
	require.NotEmpty(t, journalTx, "adjustment flow must leave journal linkage")
	tables := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "journal_transactions", query: `DELETE FROM journal_transactions WHERE account_id = ?`, args: []any{account.ID}},
		{name: "journal_entries", query: `DELETE FROM journal_entries WHERE transaction_id = ?`, args: []any{journalTx}},
		{name: "billing_valuations", query: `DELETE FROM billing_valuations WHERE store_id = ?`, args: []any{store.StoreID()}},
		{name: "billing_reconciliations", query: `DELETE FROM billing_reconciliations WHERE store_id = ?`, args: []any{store.StoreID()}},
		{name: "billing_selected_cost_adjustments", query: `DELETE FROM billing_selected_cost_adjustments WHERE store_id = ?`, args: []any{store.StoreID()}},
		{name: "billing_statement_revisions", query: `DELETE FROM billing_statement_revisions WHERE store_id = ?`, args: []any{store.StoreID()}},
		{name: "billing_statement_lines", query: `DELETE FROM billing_statement_lines WHERE store_id = ?`, args: []any{store.StoreID()}},
	}
	for _, tc := range tables {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.db.NewRaw(tc.query, tc.args...).Exec(ctx)
			require.Error(t, err, "DELETE on %s must fail closed to preserve linkage", tc.name)
		})
	}

	// Linkage is intact after the rejected deletes.
	head, err := store.GetSelectedCostHead(ctx, account.ID, callID, "head-del")
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.Version)
	var valuations int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuations))
	require.Equal(t, 1, valuations)
}
