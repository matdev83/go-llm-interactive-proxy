//go:build integration

package billingstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// TestOperatorReadersPostgresDirect proves the 16.2A operator reader
// contract on direct PostgreSQL with the same scoped loads, paging,
// vocabulary mapping and isolation as SQLite. Adapters share one
// placeholder SQL shape through Bun on both dialects and add no
// migration, so parity is structural as well as behavioral. Allowance
// parity inherits the journalstore account-window PG contract.
func TestOperatorReadersPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	first := orTestRetention(t, store.StoreID(), "or-pg-1", 1)
	require.NoError(t, store.AppendReconciliationRetention(ctx, first))
	second := orTestRetention(t, store.StoreID(), "or-pg-2", 1)
	second.CreatedAt = second.CreatedAt.AddDate(0, 0, 1)
	require.NoError(t, store.AppendReconciliationRetention(ctx, second))

	page, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, economics.DiscrepancyDiscrepant, page.Items[0].QuantityStatus)

	rest, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1, Cursor: page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	require.NotEqual(t, page.Items[0].ID, rest.Items[0].ID)

	// Account ownership is enforced in SQL before ordering/LIMIT on both
	// dialects: a foreign account row must not leak or deny the matching page.
	accountScoped := orTestRetentionForSubject(t, store.StoreID(), "or-pg-account", 1,
		orTestRetentionSubjectWithAccount(store.StoreID(), "or-pg-account-a"))
	accountScoped.CreatedAt = accountScoped.CreatedAt.AddDate(0, 0, 2)
	require.NoError(t, store.AppendReconciliationRetention(ctx, accountScoped))
	foreign := orTestRetentionForSubject(t, store.StoreID(), "or-pg-foreign", 1,
		orTestRetentionSubjectWithAccount(store.StoreID(), "or-pg-account-b"))
	foreign.CreatedAt = foreign.CreatedAt.AddDate(0, 0, 3)
	require.NoError(t, store.AppendReconciliationRetention(ctx, foreign))

	scoped, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-pg-account-a"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, scoped.Items, 1)
	require.Equal(t, "or-pg-account", scoped.Items[0].ID)
	require.Equal(t, "or-pg-account-a", scoped.Items[0].Subject.AccountID)

	normalized := statementImportNormalized(t, store.StoreID(), "stmt-or-pg", 1,
		statementImportLineSpec{ID: "line-pg", Revision: 1, Unmatched: true, UnmatchedReason: "aggregate"},
		statementImportLineSpec{ID: "line-pg-match", Revision: 1, Amount: "3.00"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))
	lines, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Outcome: economics.StatementLineUnmatched, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, lines.Lines, 1)
	require.Equal(t, "line-pg", lines.Lines[0].LineID)

	// Finding 5B parity: the exact statement-line identity filter resolves one
	// durable line on PostgreSQL as well.
	exact, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", LineID: "line-pg-match", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, exact.Lines, 1)
	require.Equal(t, "line-pg-match", exact.Lines[0].LineID)
	require.Equal(t, economics.StatementLineMatched, exact.Lines[0].Outcome)

	account := billing.Account{ID: "or-pg-adj", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	orTestAdjustmentHead(t, store, account.ID, callID, "head-or-pg", "USD", 4_000_000_000)
	adjustments, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: account.ID}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, adjustments.Adjustments, 1)

	// Finding 6: cursors are authenticated by the durable per-store key on
	// PostgreSQL as well. A recomputed position must fail closed and the legacy
	// unauthenticated op1 format must be rejected. VerifySchema above already
	// asserted the cursor-key migration/table parity.
	forged := forgeOperatorCursorReencode(t, page.NextCursor, func(cursor *operatorCursor) { cursor.RowID += 9 })
	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1, Cursor: forged,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)

	filter := struct {
		Store, Tenant, Account, Kind, ID string
	}{store.StoreID(), "tenant-or", "", "", ""}
	legacyPayload, err := json.Marshal(operatorCursor{
		Version: 1, Kind: "discrepancies", StoreID: store.StoreID(), Filter: operatorFilterHash(filter),
		CreatedAt: 1_700_030_200, RecordID: "or-pg-1", RecordVersion: 1, RowID: 1,
	})
	require.NoError(t, err)
	legacy := "op1." + base64.RawURLEncoding.EncodeToString(legacyPayload)
	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1, Cursor: legacy,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
}
