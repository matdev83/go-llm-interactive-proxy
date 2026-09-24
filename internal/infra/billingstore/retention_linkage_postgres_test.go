//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// TestRetentionLinkagePostgresDirect proves the 16.3A retention linkage
// contract on direct PostgreSQL: immutable financial/retention rows reject
// deletes in both dialects (triggers ship in both DDLs), and adjustment
// linkage resolves after the write. SQLite parity lives in
// retention_linkage_test.go.
func TestRetentionLinkagePostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "rl-pg", "USD")
	callID := edTestCallID(t)
	orTestAdjustmentHead(t, store, account.ID, callID, "head-rl-pg", "USD", 10_000_000_000)
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-rl-pg", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "journal_transactions", query: `DELETE FROM journal_transactions WHERE account_id = ?`, args: []any{account.ID}},
		{name: "billing_selected_cost_adjustments", query: `DELETE FROM billing_selected_cost_adjustments WHERE store_id = ?`, args: []any{store.StoreID()}},
		{name: "billing_statement_lines", query: `DELETE FROM billing_statement_lines WHERE store_id = ?`, args: []any{store.StoreID()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.db.NewRaw(tc.query, tc.args...).Exec(ctx)
			require.Error(t, err, "DELETE on %s must fail closed", tc.name)
		})
	}

	head, err := store.GetSelectedCostHead(ctx, account.ID, callID, "head-rl-pg")
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.Version)
	page, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: account.ID}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Adjustments, 1)
	require.Equal(t, head.LastOperationKey, page.Adjustments[0].OperationKey)
}
