package billingstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Focused schema/parity tests for the A-leg report scope indexes
// (20260925000000). Indexes only: no columns, constraints, data, or writer
// behavior change. Dual-dialect parity rides the shared migration history
// assertion plus these SQLite plan assertions; PostgreSQL direct runs skip
// without a DSN like the rest of the suite.

func explainALegPlan(t *testing.T, store *DurableStore, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sql, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(details, " | ")
}

func requireALegSearch(t *testing.T, store *DurableStore, name, sql string, args ...any) string {
	t.Helper()
	plan := explainALegPlan(t, store, sql, args...)
	require.Contains(t, plan, "SEARCH", "%s must use an index seek, got: %s", name, plan)
	require.NotContains(t, plan, "SCAN ", "%s must not scan, got: %s", name, plan)
	return plan
}

func TestALegReportScopeIndexesExist(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, name := range []string{
		"idx_usage_call_records_account_aleg_sealed",
		"idx_billing_journal_account_turn",
		"idx_usage_leg_records_aleg_call_bleg",
	} {
		var found string
		require.NoError(t, store.db.NewRaw(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(ctx, &found))
		require.Equal(t, name, found, "scope index %q must exist", name)
	}
	// Migration rerun stays idempotent.
	require.NoError(t, Migrate(ctx, store.db))
}

func TestALegReportScopeQueryPlans(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	accountID, aLegID := "aleg-plan-acct", "a-leg-plan"

	plan := requireALegSearch(t, store,
		"calls scope/order",
		`SELECT call_id FROM usage_call_records WHERE account_id = ? AND a_leg_id = ? ORDER BY sealed_at, call_id LIMIT 4`,
		accountID, aLegID)
	require.Contains(t, plan, "idx_usage_call_records_account_aleg_sealed")
	require.NotContains(t, plan, "TEMP B-TREE")

	plan = requireALegSearch(t, store,
		"calls keyset",
		`SELECT call_id FROM usage_call_records WHERE account_id = ? AND a_leg_id = ? AND (sealed_at > ? OR (sealed_at = ? AND call_id > ?)) ORDER BY sealed_at, call_id LIMIT 4`,
		accountID, aLegID, "2001-01-01", "2001-01-01", "bc_x")
	require.Contains(t, plan, "idx_usage_call_records_account_aleg_sealed")
	require.NotContains(t, plan, "TEMP B-TREE")

	// Markers ride the pre-existing composite autoindex; this locks the
	// invariant instead of adding a redundant index.
	plan = requireALegSearch(t, store,
		"markers chunk",
		`SELECT operation_key FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind IN ('customer_call_settlement','customer_no_charge_repair') AND source_key IN (?,?,?)`,
		accountID, "bc_a", "bc_b", "bc_c")

	plan = requireALegSearch(t, store,
		"journals chunk",
		`SELECT transaction_id FROM journal_transactions WHERE account_id = ? AND turn_id IN (?,?,?) AND book = 'financial'`,
		accountID, "bc_a", "bc_b", "bc_c")
	require.Contains(t, plan, "idx_billing_journal_account_turn")

	plan = requireALegSearch(t, store,
		"legs join keyset",
		`SELECT l.call_id, l.b_leg_id FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.a_leg_id = ? AND (l.call_id > ? OR (l.call_id = ? AND l.b_leg_id > ?)) ORDER BY l.call_id, l.b_leg_id LIMIT 4`,
		accountID, aLegID, "bc_a", "bc_a", "b-1")
	require.Contains(t, plan, "idx_usage_leg_records_aleg_call_bleg")
}
