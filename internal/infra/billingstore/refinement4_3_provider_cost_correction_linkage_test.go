package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestRefinement43ProviderCostCorrectionLinksPersistAcrossRestart(t *testing.T) {
	store, reopen := refinement43FileSQLiteStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-links", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))

	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "provider-links-head", 1, 10, true)
	unchanged := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 2, 10, true)
	second := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 3, 8, true)
	fullReversal := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 4, 0, false)
	repost := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 5, 5, true)
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, first); return err }())
	unchangedResult, err := store.ApplyProviderCostRevision(ctx, unchanged)
	require.NoError(t, err)
	require.True(t, unchangedResult.Applied)
	require.Empty(t, unchangedResult.Posting.Transaction.ID, "an unchanged revision must not append a journal")
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, second); return err }())
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, fullReversal); return err }())
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, repost); return err }())

	assertRefinement43ProviderCorrectionChain(t, store, account.ID, 4)
	var originalID, latestID string
	require.NoError(t, store.db.NewRaw(`SELECT original_transaction_id, last_transaction_id FROM billing_provider_cost_heads WHERE account_id = ? AND call_id = ? AND head_key = ?`, account.ID, first.CallID.String(), first.HeadKey).Scan(ctx, &originalID, &latestID))
	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Equal(t, journals[0].ID, originalID)
	require.Equal(t, journals[3].ID, latestID)

	require.NoError(t, store.Close())
	store = reopen()
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, repost); return err }())
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 4, "exact replay after restart must not append a journal")
	assertRefinement43ProviderCorrectionChain(t, store, account.ID, 4)
}

func TestRefinement43ProviderChargeCorrectionLinksSurviveChildHeadRestart(t *testing.T) {
	store, reopen := refinement43FileSQLiteStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-charge-links", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	leg, err := testIndependentCallLegFor(billing.BillingCallID("bc_00000000000000000000000000000061"), "b-provider-charge-links").Seal()
	require.NoError(t, err)

	first := refinement43ProviderChargeRevision(t, account.ID, leg.CallID, leg, "provider-charge-links", 1, 10)
	second := refinement43ProviderChargeRevision(t, account.ID, leg.CallID, leg, "provider-charge-links", 2, 6)
	fullReversal := refinement43ProviderChargeRevision(t, account.ID, leg.CallID, leg, "provider-charge-links", 3, 0)
	for _, input := range []billing.ProviderCostRevisionInput{first, second, fullReversal} {
		require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, input); return err }())
	}
	assertRefinement43ProviderCorrectionChain(t, store, account.ID, 3)
	require.NoError(t, store.Close())
	store = reopen()
	t.Cleanup(func() { _ = store.Close() })
	duplicate, err := store.ApplyProviderCostRevision(ctx, fullReversal)
	require.NoError(t, err)
	require.True(t, duplicate.Replayed)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 3)
	assertRefinement43ProviderCorrectionChain(t, store, account.ID, 3)
}

func TestRefinement43ProviderCostCorrectionLinksLegacyOriginalAfterAdoption(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-legacy-links", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000062")
	leg, err := testIndependentCallLegFor(callID, "b-legacy-links").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	legacyAmount := billing.Money{Nano: 10, Currency: "USD"}
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: legacyAmount}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))

	adoption := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 10, Currency: "USD"})
	adoption.EvidenceRevision, adoption.Revision = 2, 2
	adoption.InputSetHash = strings.Repeat("b", 64)
	adoption.ValuationID = "legacy-links-adoption"
	adopted, err := store.ApplyProviderCostRevision(ctx, adoption)
	require.NoError(t, err)
	require.True(t, adopted.Applied, "matching legacy amount adopts without an extra journal")
	require.Empty(t, adopted.Posting.Transaction.ID, "matching legacy amount must not append a journal")
	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 1)
	var originalID, latestID string
	originalID, latestID = "", ""
	require.NoError(t, store.db.NewRaw(`SELECT original_transaction_id, last_transaction_id FROM billing_provider_cost_heads WHERE account_id = ? AND call_id = ? AND head_key = ?`, account.ID, callID.String(), adoption.HeadKey).Scan(ctx, &originalID, &latestID))
	require.Equal(t, journals[0].ID, originalID)
	require.Equal(t, journals[0].ID, latestID)

	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 8, Currency: "USD"})
	revision.EvidenceRevision, revision.Revision = 3, 3
	revision.InputSetHash = strings.Repeat("c", 64)
	revision.ValuationID = "legacy-links-correction"
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, revision); return err }())

	journals = refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	require.NotEmpty(t, journals[0].CorrectionGroupID)
	require.Equal(t, journals[0].ID, journals[1].ReversalOf)
	require.Equal(t, journals[0].ID, journals[1].CorrectsTransactionID)
	require.Equal(t, journals[0].CorrectionGroupID, journals[1].CorrectionGroupID)
	originalID, latestID = "", ""
	require.NoError(t, store.db.NewRaw(`SELECT original_transaction_id, last_transaction_id FROM billing_provider_cost_heads WHERE account_id = ? AND call_id = ? AND head_key = ?`, account.ID, callID.String(), revision.HeadKey).Scan(ctx, &originalID, &latestID))
	require.Equal(t, journals[0].ID, originalID)
	require.Equal(t, journals[1].ID, latestID)
}

func TestRefinement43ProviderCostCorrectionLinksRollbackRetryWithoutOrphan(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-link-retry", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "provider-link-retry-head", 1, 10, true)
	sentinel := fmt.Errorf("provider-link-retry-crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	_, err := store.ApplyProviderCostRevision(ctx, first)
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, refinement43ProviderJournals(t, store, account.ID))
	var headCount, fenceCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_heads WHERE account_id = ?`, account.ID).Scan(ctx, &headCount))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_posting_fences WHERE account_id = ?`, account.ID).Scan(ctx, &fenceCount))
	require.Zero(t, headCount)
	require.Zero(t, fenceCount)

	store.SetEconomicFaultHook(nil)
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, first); return err }())
	second := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 2, 8, true)
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, second); return err }())
	assertRefinement43ProviderCorrectionChain(t, store, account.ID, 2)
}

func TestRefinement43ProviderCostCorrectionLinksMigrationBackfillsLatestIdentity(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-link-migration", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	input := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "provider-link-migration-head", 1, 10, true)
	require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, input); return err }())
	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 1)

	for _, table := range []string{"billing_provider_cost_heads", "billing_provider_cost_posting_fences"} {
		_, err := store.db.NewRaw("ALTER TABLE " + table + " DROP COLUMN original_transaction_id").Exec(ctx)
		require.NoError(t, err)
	}
	_, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, BillingProviderCostCorrectionLinksMigrationName).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, store.db))

	var headOriginal, fenceOriginal string
	require.NoError(t, store.db.NewRaw(`SELECT original_transaction_id FROM billing_provider_cost_heads WHERE account_id = ? AND call_id = ? AND head_key = ?`, account.ID, input.CallID.String(), input.HeadKey).Scan(ctx, &headOriginal))
	require.NoError(t, store.db.NewRaw(`SELECT original_transaction_id FROM billing_provider_cost_posting_fences WHERE account_id = ? AND call_id = ?`, account.ID, input.CallID.String()).Scan(ctx, &fenceOriginal))
	require.Equal(t, journals[0].ID, headOriginal)
	require.Equal(t, journals[0].ID, fenceOriginal)
}

func assertRefinement43ProviderCorrectionChain(t *testing.T, store *DurableStore, accountID string, want int) {
	t.Helper()
	journals := refinement43ProviderJournals(t, store, accountID)
	require.Len(t, journals, want)
	require.Empty(t, journals[0].ReversalOf)
	require.Empty(t, journals[0].CorrectsTransactionID)
	require.NotEmpty(t, journals[0].CorrectionGroupID)
	for i := 1; i < len(journals); i++ {
		require.Equal(t, journals[i-1].ID, journals[i].ReversalOf)
		require.Equal(t, journals[i-1].ID, journals[i].CorrectsTransactionID)
		require.Equal(t, journals[0].CorrectionGroupID, journals[i].CorrectionGroupID)
		require.NotEqual(t, journals[i].ID, journals[i].ReversalOf)
	}
	var storedGroup string
	require.NoError(t, store.db.NewRaw(`SELECT correction_group_id FROM journal_transactions WHERE transaction_id = ?`, journals[0].ID).Scan(context.Background(), &storedGroup))
	require.Equal(t, journals[0].CorrectionGroupID, storedGroup)
}

func refinement43FileSQLiteStore(t *testing.T) (*DurableStore, func() *DurableStore) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "billing.sqlite") + "?_pragma=foreign_keys(ON)"
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(16)
		bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
		require.NoError(t, err)
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "test"})
		require.NoError(t, err)
		return store
	}
	store := open()
	t.Cleanup(func() { _ = store.Close() })
	return store, open
}
