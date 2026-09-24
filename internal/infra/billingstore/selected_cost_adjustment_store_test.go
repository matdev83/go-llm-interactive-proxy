package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3B durable selected-cost adjustment integration fixtures. The
// adapter must compose the 13.3A planner into one local transaction: load/lock
// head, compare-and-swap, persist the immutable adjustment operation and
// valuation link, append the balanced journal delta and advance the head. Any
// pending/stale/conflict/replay outcome must leave zero new effects. Every
// price below is a synthetic fixture, not a provider tariff.

const (
	selectedCostAdjustmentCallRaw = "bc_00000000000000000000000000000133"
	selectedCostAdjustmentHeadKey = "selected-cost-adjustment-head-133"
	selectedCostAdjustmentBLegID  = "b-leg-133"
)

func selectedCostAdjustmentCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.ParseBillingCallID(selectedCostAdjustmentCallRaw)
	require.NoError(t, err)
	return id
}

func selectedCostAdjustmentSubject(t *testing.T, accountID string) metering.SubjectRef {
	t.Helper()
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: "a-leg-133", BillingCallID: selectedCostAdjustmentCallRaw, BLegID: selectedCostAdjustmentBLegID,
	}
}

func selectedCostAdjustmentExact(t *testing.T, currency string, nanos int64) *billing.MonetaryExactAmount {
	t.Helper()
	decimal := metering.DecimalFromNanoUnits(nanos)
	return &billing.MonetaryExactAmount{Currency: currency, Decimal: &decimal}
}

func selectedCostAdjustmentAmountRatString(t *testing.T, amount *billing.MonetaryExactAmount) string {
	t.Helper()
	require.NotNil(t, amount)
	value, err := amount.Rat()
	require.NoError(t, err)
	return value.RatString()
}

func selectedCostAdjustmentRef(t *testing.T, valuationID string, revision uint64) billing.SelectedCostValuationRef {
	t.Helper()
	return billing.SelectedCostValuationRef{
		ValuationID: valuationID, Revision: revision,
		InputSetHash: fmt.Sprintf("%064x", revision),
	}
}

func selectedCostAdjustmentValuation(t *testing.T, valuationID string, revision uint64, status billing.OperatorCostSelectionStatus, currency string, nanos int64) billing.SelectedCostValuation {
	t.Helper()
	valuation, err := billing.NewSelectedCostValuation(selectedCostAdjustmentRef(t, valuationID, revision), billing.OperatorCostSelectionResult{
		Status: status, Provenance: billing.OperatorCostProvenanceAttempted, Currency: currency,
		Amount: selectedCostAdjustmentExact(t, currency, nanos),
	})
	require.NoError(t, err)
	return valuation
}

func selectedCostAdjustmentFXBasis(t *testing.T, rate string) *billing.OperatorCostFXBasis {
	t.Helper()
	parsed, err := metering.ParseDecimal(rate)
	require.NoError(t, err)
	return &billing.OperatorCostFXBasis{
		ID: "fx-basis-133", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: &parsed,
	}
}

func selectedCostAdjustmentFXValuation(t *testing.T, valuationID string, revision uint64, basis *billing.OperatorCostFXBasis, nativeNanos, postedNanos int64) billing.SelectedCostValuation {
	t.Helper()
	valuation, err := billing.NewSelectedCostValuation(selectedCostAdjustmentRef(t, valuationID, revision), billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusFinal, Provenance: billing.OperatorCostProvenanceAttempted,
		Currency: basis.ToCurrency, Amount: selectedCostAdjustmentExact(t, basis.ToCurrency, postedNanos),
		NativeAmount: selectedCostAdjustmentExact(t, basis.FromCurrency, nativeNanos), FX: basis,
	})
	require.NoError(t, err)
	return valuation
}

func selectedCostAdjustmentInput(t *testing.T, accountID string, expected billing.SelectedCostHeadExpectation, selected billing.SelectedCostValuation) billing.SelectedCostAdjustmentInput {
	t.Helper()
	return billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: selectedCostAdjustmentCallID(t), HeadKey: selectedCostAdjustmentHeadKey,
		Subject: selectedCostAdjustmentSubject(t, accountID), Expected: expected, Selected: selected,
	}
}

func selectedCostAdjustmentAccount(t *testing.T, store *DurableStore, accountID string) billing.Account {
	t.Helper()
	account := billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 1_000, State: billing.AccountReady, Version: 1,
	}
	require.NoError(t, store.CreateAccount(context.Background(), account))
	return account
}

func selectedCostAdjustmentJournals(t *testing.T, store *DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	rows, err := store.JournalTransactions(context.Background(), accountID)
	require.NoError(t, err)
	filtered := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		if row.OperationKind == "provider_call_cogs" {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func selectedCostAdjustmentJournalByID(t *testing.T, journals []billing.JournalTransaction, id string) billing.JournalTransaction {
	t.Helper()
	for _, journal := range journals {
		if journal.ID == id {
			return journal
		}
	}
	t.Fatalf("journal %s not found", id)
	return billing.JournalTransaction{}
}

func selectedCostAdjustmentRowCount(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_selected_cost_adjustments WHERE store_id = ? AND account_id = ?`, store.storeID, accountID).Scan(context.Background(), &count))
	return count
}

type selectedCostAdjustmentHeadColumns struct {
	AmountNano          int64  `bun:"amount_nano"`
	Currency            string `bun:"currency"`
	HeadVersion         int64  `bun:"head_version"`
	EvidenceRevision    int64  `bun:"evidence_revision"`
	ValuationRevision   int64  `bun:"valuation_revision"`
	SelectionStatus     string `bun:"selection_status"`
	SelectionReason     string `bun:"selection_reason"`
	SelectionBasis      string `bun:"selection_basis"`
	SelectionProvenance string `bun:"selection_provenance"`
	PostedAmountJSON    string `bun:"posted_amount_json"`
	NativeAmountJSON    string `bun:"native_amount_json"`
	FXJSON              string `bun:"fx_json"`
	PostingState        string `bun:"posting_state"`
	LastOperationKey    string `bun:"last_operation_key"`
	LastTransactionID   string `bun:"last_transaction_id"`
}

func selectedCostAdjustmentHeadColumnsFor(t *testing.T, store *DurableStore, accountID, headKey string) selectedCostAdjustmentHeadColumns {
	t.Helper()
	var row selectedCostAdjustmentHeadColumns
	require.NoError(t, store.db.NewRaw(`SELECT amount_nano, currency, head_version, evidence_revision, valuation_revision,
			selection_status, selection_reason, selection_basis, selection_provenance, posted_amount_json,
			native_amount_json, fx_json, posting_state, last_operation_key, last_transaction_id
		FROM billing_provider_cost_heads WHERE store_id = ? AND account_id = ? AND head_key = ?`,
		store.storeID, accountID, headKey).Scan(context.Background(), &row))
	return row
}

type selectedCostAdjustmentStoredRow struct {
	ID                   int64  `bun:"id"`
	OperationKey         string `bun:"operation_key"`
	LinkKey              string `bun:"link_key"`
	Fingerprint          string `bun:"fingerprint"`
	Status               string `bun:"status"`
	Comparison           string `bun:"comparison"`
	Posting              string `bun:"posting"`
	PreviousValuationID  string `bun:"previous_valuation_id"`
	PreviousRevision     int64  `bun:"previous_revision"`
	PreviousInputSetHash string `bun:"previous_input_set_hash"`
	CurrentValuationID   string `bun:"current_valuation_id"`
	CurrentRevision      int64  `bun:"current_revision"`
	CurrentInputSetHash  string `bun:"current_input_set_hash"`
	Currency             string `bun:"currency"`
	FXJSON               string `bun:"fx_json"`
	AdjustmentRevision   int64  `bun:"adjustment_revision"`
	DeltaJSON            string `bun:"delta_json"`
	JournalTransactionID string `bun:"journal_transaction_id"`
}

func selectedCostAdjustmentStoredRows(t *testing.T, store *DurableStore, accountID string) []selectedCostAdjustmentStoredRow {
	t.Helper()
	var rows []selectedCostAdjustmentStoredRow
	require.NoError(t, store.db.NewRaw(`SELECT id, operation_key, link_key, fingerprint, status, comparison, posting,
			previous_valuation_id, previous_revision, previous_input_set_hash,
			current_valuation_id, current_revision, current_input_set_hash,
			currency, fx_json, adjustment_revision, delta_json, journal_transaction_id
		FROM billing_selected_cost_adjustments WHERE store_id = ? AND account_id = ? ORDER BY id`,
		store.storeID, accountID).Scan(context.Background(), &rows))
	return rows
}

func selectedCostAdjustmentFileStore(t *testing.T) (*DurableStore, func() *DurableStore) {
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

func selectedCostAdjustmentAssertZeroNewEffects(t *testing.T, store *DurableStore, accountID string, journals, adjustments int) {
	t.Helper()
	require.Len(t, selectedCostAdjustmentJournals(t, store, accountID), journals)
	require.Equal(t, adjustments, selectedCostAdjustmentRowCount(t, store, accountID))
}

// TestSelectedCostAdjustmentSQLiteInitialAndDownwardCorrection locks the
// acceptance vector: an initial 10 USD selected valuation posts +10 and a
// correction to 8 USD posts exactly one -2 economic delta via positive
// reversed debit/credit legs, advancing the head and the customer account
// untouched.
func TestSelectedCostAdjustmentSQLiteInitialAndDownwardCorrection(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-downward")

	initial := selectedCostAdjustmentValuation(t, "valuation-133-initial", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)
	require.Equal(t, billing.SelectedCostReasonInitialPosting, applied.Reason)
	require.Equal(t, billing.SelectedCostComparisonNotEvaluated, applied.Comparison)
	require.Equal(t, billing.SelectedCostPostingApplied, applied.Posting)
	require.Nil(t, applied.Previous)
	require.Equal(t, initial.Ref, applied.Current)
	require.Equal(t, "10", selectedCostAdjustmentAmountRatString(t, applied.Delta))
	require.Equal(t, uint64(1), applied.HeadVersion)
	require.NotEmpty(t, applied.OperationKey)
	require.NotEmpty(t, applied.LinkKey)
	require.NotEmpty(t, applied.Fingerprint)
	require.NotEmpty(t, applied.TransactionID)

	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 1)
	require.Equal(t, applied.OperationKey, journals[0].SourceKey)
	require.Equal(t, applied.TransactionID, journals[0].ID)
	require.Equal(t, "provider_call_cogs", journals[0].OperationKind)
	require.Equal(t, selectedCostAdjustmentHeadKey, journals[0].CorrectionGroupID)
	require.Zero(t, journals[0].AccountSequence)
	require.Equal(t, account.BalanceNano, journals[0].BalanceBefore)
	require.Equal(t, account.BalanceNano, journals[0].BalanceAfter)
	require.Len(t, journals[0].Entries, 2)
	require.Equal(t, "inference_provider_cogs", journals[0].Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, journals[0].Entries[0].Side)
	require.Equal(t, int64(10_000_000_000), journals[0].Entries[0].Amount.Nano)
	require.Equal(t, "provider_payable_clearing", journals[0].Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, journals[0].Entries[1].Side)

	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(10_000_000_000), head.AmountNano)
	require.Equal(t, "USD", head.Currency)
	require.Equal(t, int64(1), head.HeadVersion)
	require.Equal(t, int64(1), head.EvidenceRevision)
	require.Equal(t, int64(1), head.ValuationRevision)
	require.Equal(t, string(billing.OperatorCostSelectionStatusFinal), head.SelectionStatus)
	require.Equal(t, string(billing.OperatorCostProvenanceAttempted), head.SelectionProvenance)
	require.Equal(t, string(billing.SelectedCostPostingApplied), head.PostingState)
	require.NotEmpty(t, head.PostedAmountJSON)
	require.Equal(t, applied.OperationKey, head.LastOperationKey)
	require.Equal(t, applied.TransactionID, head.LastTransactionID)

	stored := selectedCostAdjustmentStoredRows(t, store, account.ID)
	require.Len(t, stored, 1)
	require.Equal(t, applied.OperationKey, stored[0].OperationKey)
	require.Equal(t, applied.LinkKey, stored[0].LinkKey)
	require.Equal(t, applied.Fingerprint, stored[0].Fingerprint)
	require.Equal(t, string(billing.SelectedCostTransitionApplied), stored[0].Status)
	require.Equal(t, string(billing.SelectedCostPostingApplied), stored[0].Posting)
	require.Empty(t, stored[0].PreviousValuationID)
	require.Equal(t, initial.Ref.ValuationID, stored[0].CurrentValuationID)
	require.Equal(t, int64(initial.Ref.Revision), stored[0].CurrentRevision)
	require.Equal(t, initial.Ref.InputSetHash, stored[0].CurrentInputSetHash)
	require.Equal(t, "USD", stored[0].Currency)
	require.Equal(t, int64(1), stored[0].AdjustmentRevision)
	require.Equal(t, applied.TransactionID, stored[0].JournalTransactionID)
	require.JSONEq(t, `{"currency":"USD","decimal":{"coefficient":"10","scale":0}}`, stored[0].DeltaJSON)

	unchanged, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, account.Version, unchanged.Version)

	corrected := selectedCostAdjustmentValuation(t, "valuation-133-corrected", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, corrected))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, result.Status)
	require.Equal(t, billing.SelectedCostReasonSameNativeCurrency, result.Reason)
	require.Equal(t, billing.SelectedCostComparisonComparable, result.Comparison)
	require.Equal(t, corrected.Ref, result.Current)
	require.NotNil(t, result.Previous)
	require.Equal(t, initial.Ref, *result.Previous)
	require.Equal(t, "-2", selectedCostAdjustmentAmountRatString(t, result.Delta))
	require.Equal(t, uint64(2), result.HeadVersion)

	journals = selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	initialJournal := selectedCostAdjustmentJournalByID(t, journals, applied.TransactionID)
	correctionJournal := selectedCostAdjustmentJournalByID(t, journals, result.TransactionID)
	require.Equal(t, initialJournal.ID, correctionJournal.ReversalOf)
	require.Equal(t, initialJournal.ID, correctionJournal.CorrectsTransactionID)
	require.Equal(t, "provider_payable_clearing", correctionJournal.Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, correctionJournal.Entries[0].Side)
	require.Equal(t, int64(2_000_000_000), correctionJournal.Entries[0].Amount.Nano)
	require.Equal(t, "inference_provider_cogs", correctionJournal.Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, correctionJournal.Entries[1].Side)
	require.Equal(t, int64(2_000_000_000), correctionJournal.Entries[1].Amount.Nano)
	require.Equal(t, account.BalanceNano, correctionJournal.BalanceBefore)
	require.Equal(t, account.BalanceNano, correctionJournal.BalanceAfter)

	head = selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(8_000_000_000), head.AmountNano)
	require.Equal(t, int64(2), head.HeadVersion)
	require.Equal(t, int64(2), head.ValuationRevision)
	require.Equal(t, result.OperationKey, head.LastOperationKey)
	require.Equal(t, result.TransactionID, head.LastTransactionID)

	stored = selectedCostAdjustmentStoredRows(t, store, account.ID)
	require.Len(t, stored, 2)
	require.Equal(t, initial.Ref.ValuationID, stored[1].PreviousValuationID)
	require.Equal(t, int64(initial.Ref.Revision), stored[1].PreviousRevision)
	require.Equal(t, corrected.Ref.ValuationID, stored[1].CurrentValuationID)
	require.Equal(t, "USD", stored[1].Currency)
	require.Equal(t, int64(2), stored[1].AdjustmentRevision)
	require.Equal(t, result.TransactionID, stored[1].JournalTransactionID)
	require.Equal(t, result.LinkKey, stored[1].LinkKey)

	unchanged, err = store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, account.Version, unchanged.Version)

	upward := selectedCostAdjustmentValuation(t, "valuation-133-upward", 3, billing.OperatorCostSelectionStatusFinal, "USD", 12_000_000_000)
	upwardResult, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: result.HeadVersion, Previous: &corrected}, upward))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, upwardResult.Status)
	require.Equal(t, "4", selectedCostAdjustmentAmountRatString(t, upwardResult.Delta))
	journals = selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 3)
	upwardJournal := selectedCostAdjustmentJournalByID(t, journals, upwardResult.TransactionID)
	require.Equal(t, correctionJournal.ID, upwardJournal.ReversalOf)
	require.Equal(t, "inference_provider_cogs", upwardJournal.Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, upwardJournal.Entries[0].Side)
	require.Equal(t, int64(4_000_000_000), upwardJournal.Entries[0].Amount.Nano)
	require.Equal(t, "provider_payable_clearing", upwardJournal.Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, upwardJournal.Entries[1].Side)

	head = selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(12_000_000_000), head.AmountNano)
	require.Equal(t, int64(3), head.HeadVersion)
	require.Equal(t, int64(3), head.ValuationRevision)
}

// TestSelectedCostAdjustmentSQLiteNoFrozenFXMismatchPostsNothing locks the
// 10 USD to 8 EUR acceptance vector: without a shared explicit frozen FX basis
// the correction stays pending with zero journal, link or head effects.
func TestSelectedCostAdjustmentSQLiteNoFrozenFXMismatchPostsNothing(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-nofx")

	initial := selectedCostAdjustmentValuation(t, "valuation-133-usd", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

	eur := selectedCostAdjustmentValuation(t, "valuation-133-eur", 2, billing.OperatorCostSelectionStatusFinal, "EUR", 8_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, eur))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
	require.Equal(t, billing.SelectedCostReasonPostedCurrencyMismatch, result.Reason)
	require.Equal(t, billing.SelectedCostComparisonIncomparable, result.Comparison)
	require.Equal(t, billing.SelectedCostPostingPending, result.Posting)
	require.Nil(t, result.Delta)
	require.Empty(t, result.OperationKey)
	require.Empty(t, result.LinkKey)
	require.Equal(t, initial.Ref, *result.Previous)
	require.Equal(t, eur.Ref, result.Current)

	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(10_000_000_000), head.AmountNano)
	require.Equal(t, int64(1), head.HeadVersion)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
}

// TestSelectedCostAdjustmentSQLiteFrozenFXBasis locks the explicit frozen FX
// path: identical frozen basis material may post the converted delta while a
// changed rate, a changed basis id or one-sided basis material remains pending.
func TestSelectedCostAdjustmentSQLiteFrozenFXBasis(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-fx")

	basis := selectedCostAdjustmentFXBasis(t, "0.8")
	initial := selectedCostAdjustmentFXValuation(t, "valuation-133-fx-a", 1, basis, 10_000_000_000, 8_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)
	require.Equal(t, "8", selectedCostAdjustmentAmountRatString(t, applied.Delta))

	shared := selectedCostAdjustmentFXValuation(t, "valuation-133-fx-b", 2, basis, 8_000_000_000, 6_400_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, shared))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, result.Status)
	require.Equal(t, billing.SelectedCostReasonFrozenFXBasis, result.Reason)
	require.Equal(t, "USD", result.Delta.Currency)
	require.Equal(t, "-8/5", selectedCostAdjustmentAmountRatString(t, result.Delta))

	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	correctionJournal := selectedCostAdjustmentJournalByID(t, journals, result.TransactionID)
	require.Equal(t, "USD", correctionJournal.Currency)
	require.Equal(t, int64(1_600_000_000), correctionJournal.Entries[0].Amount.Nano)
	require.Equal(t, "provider_payable_clearing", correctionJournal.Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, correctionJournal.Entries[0].Side)

	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(6_400_000_000), head.AmountNano)
	require.NotEmpty(t, head.NativeAmountJSON)
	require.NotEmpty(t, head.FXJSON)
	require.JSONEq(t, `{"currency":"EUR","decimal":{"coefficient":"8","scale":0}}`, head.NativeAmountJSON)

	changedRate := selectedCostAdjustmentFXBasis(t, "0.9")
	mismatch := selectedCostAdjustmentFXValuation(t, "valuation-133-fx-c", 3, changedRate, 8_000_000_000, 7_200_000_000)
	result, err = store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: result.HeadVersion, Previous: &shared}, mismatch))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
	require.Equal(t, billing.SelectedCostReasonFrozenFXBasisMismatch, result.Reason)
	require.Equal(t, billing.SelectedCostComparisonIncomparable, result.Comparison)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)

	oneSided := selectedCostAdjustmentFXValuation(t, "valuation-133-fx-d", 3, basis, 10_000_000_000, 8_000_000_000)
	usdHead := selectedCostAdjustmentValuation(t, "valuation-133-fx-usd", 1, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	other := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-fx-onesided")
	oneSidedApplied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, other.ID, billing.SelectedCostHeadExpectation{}, usdHead))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, oneSidedApplied.Status)
	result, err = store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, other.ID,
		billing.SelectedCostHeadExpectation{Version: oneSidedApplied.HeadVersion, Previous: &usdHead}, oneSided))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
	require.Equal(t, billing.SelectedCostReasonFrozenFXBasisMismatch, result.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, other.ID, 1, 1)
}

// TestSelectedCostAdjustmentSQLiteReplayAndRestartReturnsStableIdentity locks
// idempotency: an exact replay after restart returns the original operation
// identity with no new journal, link or head transition.
func TestSelectedCostAdjustmentSQLiteReplayAndRestartReturnsStableIdentity(t *testing.T) {
	store, reopen := selectedCostAdjustmentFileStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-replay")
	initial := selectedCostAdjustmentValuation(t, "valuation-133-replay", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	input := selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial)

	applied, err := store.ApplySelectedCostAdjustment(ctx, input)
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

	replayed, err := store.ApplySelectedCostAdjustment(ctx, input)
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, replayed.Status)
	require.Equal(t, billing.SelectedCostReasonAlreadyApplied, replayed.Reason)
	require.Equal(t, billing.SelectedCostPostingReplayed, replayed.Posting)
	require.Equal(t, applied.OperationKey, replayed.OperationKey)
	require.Equal(t, applied.LinkKey, replayed.LinkKey)
	require.Equal(t, applied.Fingerprint, replayed.Fingerprint)
	require.Equal(t, applied.TransactionID, replayed.TransactionID)
	require.Equal(t, selectedCostAdjustmentAmountRatString(t, applied.Delta), selectedCostAdjustmentAmountRatString(t, replayed.Delta))
	require.Equal(t, uint64(1), replayed.HeadVersion)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	head, err := store.GetSelectedCostHead(ctx, account.ID, input.CallID, input.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.Version)
	require.NotNil(t, head.Selected)
	require.True(t, initial.IdentityEqual(*head.Selected))

	restarted, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, restarted.Status)
	require.Equal(t, applied.OperationKey, restarted.OperationKey)
	require.Equal(t, applied.TransactionID, restarted.TransactionID)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	require.NoError(t, store.Close())
	store = reopen()
	t.Cleanup(func() { _ = store.Close() })
	afterRestart, err := store.ApplySelectedCostAdjustment(ctx, input)
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, afterRestart.Status)
	require.Equal(t, applied.OperationKey, afterRestart.OperationKey)
	require.Equal(t, applied.TransactionID, afterRestart.TransactionID)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
	headColumns := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(1), headColumns.HeadVersion)
	require.Equal(t, int64(10_000_000_000), headColumns.AmountNano)
}

// TestSelectedCostAdjustmentSQLiteZeroDeltaAdvancesHeadWithoutJournal locks the
// no-op correction: a new selected revision with the same amount persists the
// immutable link and advances the head without appending a journal row.
func TestSelectedCostAdjustmentSQLiteZeroDeltaAdvancesHeadWithoutJournal(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-zero")

	initial := selectedCostAdjustmentValuation(t, "valuation-133-zero-a", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

	zero := selectedCostAdjustmentValuation(t, "valuation-133-zero-b", 2, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, zero))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionNoOp, result.Status)
	require.Equal(t, billing.SelectedCostReasonZeroDelta, result.Reason)
	require.Equal(t, "0", selectedCostAdjustmentAmountRatString(t, result.Delta))
	require.Empty(t, result.TransactionID)
	require.Equal(t, uint64(2), result.HeadVersion)

	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 1)
	stored := selectedCostAdjustmentStoredRows(t, store, account.ID)
	require.Len(t, stored, 2)
	require.Equal(t, string(billing.SelectedCostTransitionNoOp), stored[1].Status)
	require.Empty(t, stored[1].JournalTransactionID)
	require.Equal(t, initial.Ref.ValuationID, stored[1].PreviousValuationID)
	require.Equal(t, zero.Ref.ValuationID, stored[1].CurrentValuationID)
	require.Equal(t, result.LinkKey, stored[1].LinkKey)

	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(2), head.HeadVersion)
	require.Equal(t, int64(10_000_000_000), head.AmountNano)
	require.Equal(t, zero.Ref.ValuationID, result.Current.ValuationID)
}

// TestSelectedCostAdjustmentSQLitePendingSelectionsPersistNothing proves that
// provisional, unknown-attempted and non-operator selections never create a
// head, adjustment, link or journal row.
func TestSelectedCostAdjustmentSQLitePendingSelectionsPersistNothing(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	provisional, err := billing.NewSelectedCostValuation(selectedCostAdjustmentRef(t, "valuation-133-provisional", 1), billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusProvisional, Provenance: billing.OperatorCostProvenanceAttempted,
		Currency: "USD", Amount: selectedCostAdjustmentExact(t, "USD", 10_000_000_000),
	})
	require.NoError(t, err)
	unknown, err := billing.NewSelectedCostValuation(selectedCostAdjustmentRef(t, "valuation-133-unknown", 1), billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusUnknown, Provenance: billing.OperatorCostProvenanceAttempted, Currency: "USD",
	})
	require.NoError(t, err)
	notPayable, err := billing.NewSelectedCostValuation(selectedCostAdjustmentRef(t, "valuation-133-not-payable", 1), billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusNotOperatorPayable, Provenance: billing.OperatorCostProvenanceAttempted,
		Currency: "USD", Amount: selectedCostAdjustmentExact(t, "USD", 10_000_000_000),
	})
	require.NoError(t, err)

	cases := []struct {
		name     string
		selected billing.SelectedCostValuation
		reason   billing.SelectedCostHeadTransitionReason
	}{
		{"provisional", provisional, billing.SelectedCostReasonProvisionalSelection},
		{"unknown attempted", unknown, billing.SelectedCostReasonMissingAttemptedUsage},
		{"non operator payable", notPayable, billing.SelectedCostReasonNotOperatorPayable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-pending-"+testCase.name)
			result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, testCase.selected))
			require.NoError(t, err)
			require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
			require.Equal(t, testCase.reason, result.Reason)
			require.Equal(t, billing.SelectedCostPostingPending, result.Posting)
			selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 0, 0)
			_, err = store.GetSelectedCostHead(ctx, account.ID, selectedCostAdjustmentCallID(t), selectedCostAdjustmentHeadKey)
			require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)
		})
	}
}

// TestSelectedCostAdjustmentSQLiteStaleAndConflictHaveZeroEffects locks the CAS
// and revision fences: stale expected versions, stale revisions and same-
// revision identity conflicts never append money or mutate the head.
func TestSelectedCostAdjustmentSQLiteStaleAndConflictHaveZeroEffects(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-cas")

	rev2 := selectedCostAdjustmentValuation(t, "valuation-133-rev2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, rev2))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

	staleVersion := selectedCostAdjustmentValuation(t, "valuation-133-stale", 3, billing.OperatorCostSelectionStatusFinal, "USD", 9_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, staleVersion))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionStale, result.Status)
	require.Equal(t, billing.SelectedCostReasonStaleHeadVersion, result.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	identityConflict := selectedCostAdjustmentValuation(t, "valuation-133-other", 2, billing.OperatorCostSelectionStatusFinal, "USD", 9_000_000_000)
	result, err = store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &identityConflict}, identityConflict))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionConflict, result.Status)
	require.Equal(t, billing.SelectedCostReasonHeadIdentityConflict, result.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	staleRevision := selectedCostAdjustmentValuation(t, "valuation-133-rev1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	result, err = store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &rev2}, staleRevision))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionStale, result.Status)
	require.Equal(t, billing.SelectedCostReasonStaleRevision, result.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	revisionConflict := selectedCostAdjustmentValuation(t, "valuation-133-rev2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 7_000_000_000)
	result, err = store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &rev2}, revisionConflict))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionConflict, result.Status)
	require.Equal(t, billing.SelectedCostReasonRevisionConflict, result.Reason)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)

	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(1), head.HeadVersion)
	require.Equal(t, int64(10_000_000_000), head.AmountNano)
}

// TestSelectedCostAdjustmentSQLiteFaultInjectionRollsBackEachWriteBoundary
// injects a fault after the journal, after the immutable adjustment/link row
// and after the head advance; every boundary must roll back all-or-nothing and
// stay retryable.
func TestSelectedCostAdjustmentSQLiteFaultInjectionRollsBackEachWriteBoundary(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sentinel := errors.New("selected-cost-adjustment-crash")
	stages := []string{
		"after_selected_cost_adjustment_journal",
		"after_selected_cost_adjustment_link",
		"after_selected_cost_adjustment",
	}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			account := selectedCostAdjustmentAccount(t, store, fmt.Sprintf("selected-cost-adjustment-fault-%d", index))
			initial := selectedCostAdjustmentValuation(t, "valuation-133-fault-initial", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
			input := selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial)
			store.SetEconomicFaultHook(func(crashed string) error {
				if crashed == stage {
					return sentinel
				}
				return nil
			})
			_, err := store.ApplySelectedCostAdjustment(ctx, input)
			require.ErrorIs(t, err, sentinel)
			selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 0, 0)
			_, err = store.GetSelectedCostHead(ctx, account.ID, input.CallID, input.HeadKey)
			require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)

			store.SetEconomicFaultHook(nil)
			retried, err := store.ApplySelectedCostAdjustment(ctx, input)
			require.NoError(t, err)
			require.Equal(t, billing.SelectedCostTransitionApplied, retried.Status)
			selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
			head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
			require.Equal(t, int64(1), head.HeadVersion)
			require.Equal(t, int64(10_000_000_000), head.AmountNano)
		})
	}
	store.SetEconomicFaultHook(nil)
}

// TestSelectedCostAdjustmentSQLiteRacingSameAndDifferentRevisions proves one
// effect for racing identical operations and CAS ordering for racing distinct
// revisions.
func TestSelectedCostAdjustmentSQLiteRacingSameAndDifferentRevisions(t *testing.T) {
	store, _ := selectedCostAdjustmentFileStore(t)
	ctx := context.Background()

	t.Run("same operation converges", func(t *testing.T) {
		account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-race-same")
		initial := selectedCostAdjustmentValuation(t, "valuation-133-race-same", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
		input := selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial)

		results := make([]billing.SelectedCostAdjustmentResult, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				results[index], errs[index] = store.ApplySelectedCostAdjustment(ctx, input)
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "worker %d", i)
		}
		applied, replayed := 0, 0
		for _, result := range results {
			switch result.Status {
			case billing.SelectedCostTransitionApplied:
				applied++
			case billing.SelectedCostTransitionReplay:
				replayed++
			}
			require.Equal(t, results[0].OperationKey, result.OperationKey)
		}
		require.Equal(t, 1, applied)
		require.Equal(t, 1, replayed)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
		head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
		require.Equal(t, int64(1), head.HeadVersion)
	})

	t.Run("different revisions obey CAS", func(t *testing.T) {
		account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-race-different")
		left := selectedCostAdjustmentValuation(t, "valuation-133-race-left", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
		right := selectedCostAdjustmentValuation(t, "valuation-133-race-right", 3, billing.OperatorCostSelectionStatusFinal, "USD", 9_000_000_000)
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
		for i, err := range errs {
			require.NoError(t, err, "worker %d", i)
		}
		applied := 0
		for _, result := range results {
			if result.Status == billing.SelectedCostTransitionApplied {
				applied++
			} else {
				require.Contains(t, []billing.SelectedCostHeadTransitionStatus{
					billing.SelectedCostTransitionStale, billing.SelectedCostTransitionConflict,
				}, result.Status)
				require.Nil(t, result.Delta)
			}
		}
		require.Equal(t, 1, applied)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
		head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
		require.Equal(t, int64(1), head.HeadVersion)
		require.Contains(t, []int64{8_000_000_000, 9_000_000_000}, head.AmountNano)
	})
}

// TestSelectedCostAdjustmentSQLiteReadsLegacyHeads proves migration
// compatibility: a head created by the existing provider-cost writer and a
// pre-migration row with blank identity columns both remain readable and
// correctable through the selected-cost adjustment adapter.
func TestSelectedCostAdjustmentSQLiteReadsLegacyHeads(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-legacy")

	subject := selectedCostAdjustmentSubject(t, account.ID)
	legacy := refinement43ProviderRevisionInput(account.ID, selectedCostAdjustmentCallID(t), selectedCostAdjustmentHeadKey, 1, 10_000_000_000, true)
	legacy.Subject = subject
	legacy.ALegID, legacy.BLegID = "", ""
	legacy.Evidence.Subject = subject
	for i := range legacy.Evidence.Observations {
		legacy.Evidence.Observations[i].Subject = subject
		legacy.Evidence.Observations[i].Correlation.ALegID = subject.ALegID
		legacy.Evidence.Observations[i].Correlation.BLegID = subject.BLegID
	}
	legacyPosting, err := store.ApplyProviderCostRevision(ctx, legacy)
	require.NoError(t, err)
	require.True(t, legacyPosting.Applied)
	head, err := store.GetSelectedCostHead(ctx, account.ID, legacy.CallID, legacy.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.Version)
	require.NotNil(t, head.Selected)
	require.Equal(t, legacy.ValuationID, head.Selected.Ref.ValuationID)
	require.Equal(t, uint64(1), head.Selected.Ref.Revision)
	require.Equal(t, "USD", head.Selected.Currency)
	require.Equal(t, "10", selectedCostAdjustmentAmountRatString(t, head.Selected.Amount))
	require.Nil(t, head.Selected.NativeAmount)
	require.Nil(t, head.Selected.FX)

	// Emulate a row written before the additive identity migration: the reader
	// must derive the exact posted amount from the legacy integer nanos.
	_, err = store.db.NewRaw(`UPDATE billing_provider_cost_heads SET valuation_revision = 0, posted_amount_json = '', native_amount_json = '', fx_json = '' WHERE store_id = ? AND account_id = ? AND head_key = ?`,
		store.storeID, account.ID, selectedCostAdjustmentHeadKey).Exec(ctx)
	require.NoError(t, err)
	head, err = store.GetSelectedCostHead(ctx, account.ID, legacy.CallID, legacy.HeadKey)
	require.NoError(t, err)
	require.Equal(t, "10", selectedCostAdjustmentAmountRatString(t, head.Selected.Amount))

	corrected := selectedCostAdjustmentValuation(t, "valuation-133-legacy-correction", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected}, corrected))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, result.Status)
	require.Equal(t, billing.SelectedCostReasonSameNativeCurrency, result.Reason)
	require.Equal(t, "-2", selectedCostAdjustmentAmountRatString(t, result.Delta))

	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	legacyJournal := selectedCostAdjustmentJournalByID(t, journals, legacyPosting.Posting.Transaction.ID)
	correctionJournal := selectedCostAdjustmentJournalByID(t, journals, result.TransactionID)
	require.Equal(t, legacyJournal.ID, correctionJournal.ReversalOf)
	require.Equal(t, legacyJournal.ID, correctionJournal.CorrectsTransactionID)

	reloaded, err := store.GetSelectedCostHead(ctx, account.ID, legacy.CallID, legacy.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(2), reloaded.Version)
	require.True(t, corrected.IdentityEqual(*reloaded.Selected))
}

// TestSelectedCostAdjustmentSQLiteSchemaIsRegistered verifies the additive
// dual-dialect migration artifacts and the immutability of retained adjustment
// operations.
func TestSelectedCostAdjustmentSQLiteSchemaIsRegistered(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.Contains(t, RequiredMigrationNames, BillingSelectedCostAdjustmentsMigrationName)
	require.NoError(t, VerifySchema(ctx, store.db))

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingSelectedCostAdjustmentsMigrationName).Scan(ctx, &count))
	require.Equal(t, 1, count)
	for _, column := range []string{
		"valuation_revision", "selection_status", "selection_reason", "selection_basis",
		"selection_provenance", "posted_amount_json", "native_amount_json", "fx_json", "posting_state",
	} {
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_provider_cost_heads') WHERE name = ?`, column).Scan(ctx, &count), column)
		require.Equal(t, 1, count, column)
	}
	for _, table := range []string{"billing_selected_cost_adjustments"} {
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &count))
		require.Equal(t, 1, count, table)
	}
	var index string
	require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, billingSelectedCostAdjustmentHeadIndex).Scan(ctx, &index))
	require.Equal(t, billingSelectedCostAdjustmentHeadIndex, index)

	account := selectedCostAdjustmentAccount(t, store, "selected-cost-adjustment-immutable")
	initial := selectedCostAdjustmentValuation(t, "valuation-133-immutable", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	_, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE billing_selected_cost_adjustments SET fingerprint = 'forged' WHERE store_id = ?`, store.storeID)
	require.Error(t, err)
	_, err = store.db.ExecContext(ctx, `DELETE FROM billing_selected_cost_adjustments WHERE store_id = ?`, store.storeID)
	require.Error(t, err)
	require.Equal(t, 1, selectedCostAdjustmentRowCount(t, store, account.ID))
}
