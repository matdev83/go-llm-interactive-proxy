//go:build integration

package billingstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestSelectedCostAdjustmentPostgresDirect proves the durable selected-cost
// adjustment adapter on direct PostgreSQL with the same atomic CAS, link,
// journal, replay, no-FX and immutability behavior as SQLite.
func TestSelectedCostAdjustmentPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("schema catalog", func(t *testing.T) {
		require.Contains(t, RequiredMigrationNames, BillingSelectedCostAdjustmentsMigrationName)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingSelectedCostAdjustmentsMigrationName).Scan(ctx, &count))
		require.Equal(t, 1, count)
		for _, column := range []string{
			"valuation_revision", "selection_status", "selection_reason", "selection_basis",
			"selection_provenance", "posted_amount_json", "native_amount_json", "fx_json", "posting_state",
		} {
			require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_provider_cost_heads' AND column_name = ?`, column).Scan(ctx, &count), column)
			require.Equal(t, 1, count, column)
		}
		for _, index := range []string{billingSelectedCostAdjustmentOperationIndex, billingSelectedCostAdjustmentLinkIndex, billingSelectedCostAdjustmentHeadIndex} {
			var name string
			require.NoError(t, store.db.NewRaw(`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'billing_selected_cost_adjustments' AND indexname = ?`, index).Scan(ctx, &name))
			require.Equal(t, index, name)
		}
	})

	account := billing.Account{ID: "selected-cost-pg-account", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))

	initial := selectedCostAdjustmentValuation(t, "valuation-pg-initial", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)
	require.Equal(t, "10", selectedCostAdjustmentAmountRatString(t, applied.Delta))
	require.NotEmpty(t, applied.TransactionID)

	replayed, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, replayed.Status)
	require.Equal(t, applied.OperationKey, replayed.OperationKey)
	require.Equal(t, applied.TransactionID, replayed.TransactionID)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	eur := selectedCostAdjustmentValuation(t, "valuation-pg-eur", 2, billing.OperatorCostSelectionStatusFinal, "EUR", 8_000_000_000)
	pending, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, eur))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionPending, pending.Status)
	require.Equal(t, billing.SelectedCostReasonPostedCurrencyMismatch, pending.Reason)
	require.Nil(t, pending.Delta)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	zero := selectedCostAdjustmentValuation(t, "valuation-pg-zero", 2, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	noOp, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, zero))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionNoOp, noOp.Status)
	require.Empty(t, noOp.TransactionID)
	require.Equal(t, uint64(2), noOp.HeadVersion)
	require.Len(t, selectedCostAdjustmentJournals(t, store, account.ID), 1)

	corrected := selectedCostAdjustmentValuation(t, "valuation-pg-corrected", 3, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: noOp.HeadVersion, Previous: &zero}, corrected))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, result.Status)
	require.Equal(t, "-2", selectedCostAdjustmentAmountRatString(t, result.Delta))
	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	initialJournal := selectedCostAdjustmentJournalByID(t, journals, applied.TransactionID)
	correctionJournal := selectedCostAdjustmentJournalByID(t, journals, result.TransactionID)
	require.Equal(t, initialJournal.ID, correctionJournal.ReversalOf)
	require.Equal(t, initialJournal.ID, correctionJournal.CorrectsTransactionID)
	require.Equal(t, "provider_payable_clearing", correctionJournal.Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, correctionJournal.Entries[0].Side)
	require.Equal(t, "inference_provider_cogs", correctionJournal.Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, correctionJournal.Entries[1].Side)
	require.Equal(t, account.BalanceNano, correctionJournal.BalanceBefore)
	require.Equal(t, account.BalanceNano, correctionJournal.BalanceAfter)

	head, err := store.GetSelectedCostHead(ctx, account.ID, selectedCostAdjustmentCallID(t), selectedCostAdjustmentHeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(3), head.Version)
	require.True(t, corrected.IdentityEqual(*head.Selected))
	unchanged, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.BalanceNano, unchanged.BalanceNano)

	_, err = store.db.ExecContext(ctx, `UPDATE billing_selected_cost_adjustments SET fingerprint = 'forged' WHERE store_id = ?`, store.storeID)
	require.Error(t, err)
	_, err = store.db.ExecContext(ctx, `DELETE FROM billing_selected_cost_adjustments WHERE store_id = ?`, store.storeID)
	require.Error(t, err)
	require.Equal(t, 3, selectedCostAdjustmentRowCount(t, store, account.ID))

	// Restart over the same database handle returns the stable operation
	// identity without a new journal or head transition.
	reopened, err := openStore(ctx, store.db, Config{StoreID: "test"})
	require.NoError(t, err)
	afterRestart, err := reopened.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: result.HeadVersion, Previous: &corrected}, corrected))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, afterRestart.Status)
	require.Equal(t, result.OperationKey, afterRestart.OperationKey)
	require.Equal(t, result.TransactionID, afterRestart.TransactionID)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 3)

	// A stale CAS read with a different selection is a zero-effect fence.
	stale := selectedCostAdjustmentValuation(t, "valuation-pg-stale", 4, billing.OperatorCostSelectionStatusFinal, "USD", 9_000_000_000)
	staleResult, err := reopened.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, stale))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionStale, staleResult.Status)
	require.Equal(t, billing.SelectedCostReasonStaleHeadVersion, staleResult.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 3)
}

// TestSelectedCostAdjustmentPostgresDirectRollbackAndRaces proves all-or-
// nothing rollback at every write boundary and one effect under concurrent
// identical or distinct revisions on direct PostgreSQL.
func TestSelectedCostAdjustmentPostgresDirectRollbackAndRaces(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("fault rolls back every write", func(t *testing.T) {
		sentinel := errors.New("selected-cost-adjustment-pg-crash")
		stages := []string{
			"after_selected_cost_adjustment_journal",
			"after_selected_cost_adjustment_link",
			"after_selected_cost_adjustment",
		}
		for index, stage := range stages {
			account := billing.Account{ID: fmt.Sprintf("selected-cost-pg-fault-%d", index), Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			initial := selectedCostAdjustmentValuation(t, "valuation-pg-fault", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
			input := selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial)
			store.SetEconomicFaultHook(func(crashed string) error {
				if crashed == stage {
					return sentinel
				}
				return nil
			})
			_, err := store.ApplySelectedCostAdjustment(ctx, input)
			require.ErrorIs(t, err, sentinel, stage)
			selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 0, 0)
			store.SetEconomicFaultHook(nil)
			retried, err := store.ApplySelectedCostAdjustment(ctx, input)
			require.NoError(t, err)
			require.Equal(t, billing.SelectedCostTransitionApplied, retried.Status)
			selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
		}
		store.SetEconomicFaultHook(nil)
	})

	t.Run("same operation converges", func(t *testing.T) {
		account := billing.Account{ID: "selected-cost-pg-same", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
		require.NoError(t, store.CreateAccount(ctx, account))
		initial := selectedCostAdjustmentValuation(t, "valuation-pg-same", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
		input := selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial)
		const workers = 4
		results := make([]billing.SelectedCostAdjustmentResult, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				results[index], errs[index] = store.ApplySelectedCostAdjustment(ctx, input)
			}(i)
		}
		wg.Wait()
		applied, replayed := 0, 0
		for i, err := range errs {
			require.NoError(t, err, "worker %d", i)
			switch results[i].Status {
			case billing.SelectedCostTransitionApplied:
				applied++
			case billing.SelectedCostTransitionReplay:
				replayed++
			}
			require.Equal(t, results[0].OperationKey, results[i].OperationKey)
		}
		require.Equal(t, 1, applied)
		require.Equal(t, workers-1, replayed)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
	})

	t.Run("different revisions obey CAS", func(t *testing.T) {
		account := billing.Account{ID: "selected-cost-pg-different", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
		require.NoError(t, store.CreateAccount(ctx, account))
		left := selectedCostAdjustmentValuation(t, "valuation-pg-left", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
		right := selectedCostAdjustmentValuation(t, "valuation-pg-right", 3, billing.OperatorCostSelectionStatusFinal, "USD", 9_000_000_000)
		inputs := []billing.SelectedCostAdjustmentInput{
			selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, left),
			selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, right),
		}
		results := make([]billing.SelectedCostAdjustmentResult, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				results[index], errs[index] = store.ApplySelectedCostAdjustment(ctx, inputs[index])
			}(i)
		}
		wg.Wait()
		applied := 0
		for i, err := range errs {
			require.NoError(t, err, "worker %d", i)
			if results[i].Status == billing.SelectedCostTransitionApplied {
				applied++
			} else {
				require.Contains(t, []billing.SelectedCostHeadTransitionStatus{
					billing.SelectedCostTransitionStale, billing.SelectedCostTransitionConflict,
				}, results[i].Status)
			}
		}
		require.Equal(t, 1, applied)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
	})
}
