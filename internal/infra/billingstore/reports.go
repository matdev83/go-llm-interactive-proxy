package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

const providerJournalOperationKind = "provider_call_cogs"

// journalOrderClause keeps customer pagination sequence-based while giving the
// provider stream its own immutable recorded_at/transaction_id order. The
// provider branch intentionally does not inspect account_sequence, because
// upgraded databases may contain historical provider sequences.
func journalOrderClause(operationKind string) string {
	if operationKind == providerJournalOperationKind {
		return " ORDER BY recorded_at, transaction_id"
	}
	if operationKind != "" {
		return " ORDER BY account_sequence, recorded_at, transaction_id"
	}
	return " ORDER BY CASE WHEN operation_kind = '" + providerJournalOperationKind + "' THEN 1 ELSE 0 END, CASE WHEN operation_kind = '" + providerJournalOperationKind + "' THEN recorded_at END, CASE WHEN operation_kind = '" + providerJournalOperationKind + "' THEN transaction_id END, CASE WHEN operation_kind <> '" + providerJournalOperationKind + "' THEN account_sequence END, recorded_at, transaction_id"
}

const operationSnapshotOrderClause = " ORDER BY CASE WHEN operation_kind IN ('provider_call_cogs', 'provider_cost_unreconciled') THEN 1 ELSE 0 END, CASE WHEN operation_kind IN ('provider_call_cogs', 'provider_cost_unreconciled') THEN created_at END, CASE WHEN operation_kind IN ('provider_call_cogs', 'provider_cost_unreconciled') THEN operation_key END, CASE WHEN operation_kind NOT IN ('provider_call_cogs', 'provider_cost_unreconciled') THEN account_sequence_end END, created_at, operation_key"

func (s *DurableStore) AccountReport(ctx context.Context, accountID string, page billing.PageRequest) (billing.AccountReport, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.AccountReport{}, err
	}
	account, err := s.GetAccount(ctx, strings.TrimSpace(accountID))
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return billing.AccountReport{}, billing.ErrReportNotFound
		}
		return billing.AccountReport{}, err
	}
	spendable, err := account.SpendableNano()
	if err != nil {
		return billing.AccountReport{}, err
	}
	transactions, next, err := s.loadJournalPage(ctx, account.ID, account.Currency, billing.JournalBook(""), "", time.Time{}, time.Time{}, page)
	if err != nil {
		return billing.AccountReport{}, err
	}
	// Customer pagination remains sequence-based, while provider rows are an
	// independent audit stream ordered by recorded_at/transaction_id. Include
	// the provider stream without making NULL sequences look customer-sequenced.
	providerTransactions, err := s.loadJournalRange(ctx, account.ID, account.Currency, billing.JournalBook(""), "provider_call_cogs", time.Time{}, time.Time{})
	if err != nil {
		return billing.AccountReport{}, err
	}
	transactions = append(transactions, providerTransactions...)
	var openExposureNano int64
	if err := s.db.NewRaw(`SELECT COALESCE(SUM(max_exposure_nano), 0) FROM call_exposures WHERE account_id = ? AND status = 'open'`, account.ID).Scan(ctx, &openExposureNano); err != nil {
		return billing.AccountReport{}, err
	}
	return billing.AccountReport{
		Account:         account,
		SpendableNano:   spendable,
		CreditFloorNano: account.CreditFloorNano(),
		OpenExposure:    billing.Money{Nano: openExposureNano, Currency: account.Currency},
		Transactions:    transactions,
		NextCursor:      next,
	}, nil
}

func (s *DurableStore) OperatorCostReport(ctx context.Context, filter billing.ReportFilter) (billing.OperatorCostReport, error) {
	filter, err := filter.Normalize()
	if err != nil {
		return billing.OperatorCostReport{}, err
	}
	if filter.AccountID == "" {
		return billing.OperatorCostReport{}, fmt.Errorf("%w: operator report requires account", billing.ErrReportInvalid)
	}
	account, err := s.GetAccount(ctx, filter.AccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return billing.OperatorCostReport{}, billing.ErrReportNotFound
		}
		return billing.OperatorCostReport{}, err
	}
	currency := account.Currency
	if filter.Currency != "" && filter.Currency != currency {
		return billing.OperatorCostReport{}, billing.ErrMoneyCurrencyMismatch
	}
	book := filter.Book
	if book == "" {
		book = billing.JournalBookFinancial
	}
	transactions, err := s.loadJournalRange(ctx, account.ID, currency, book, "", filter.From, filter.To)
	if err != nil {
		return billing.OperatorCostReport{}, err
	}
	revenue, cost, issues := billing.SummarizeJournalForReport(transactions, currency)
	margin, err := billing.ReportMargin(currency, revenue, cost)
	if err != nil {
		return billing.OperatorCostReport{}, err
	}
	cogsByLeg := netCogsByCallAndBLeg(transactions, currency)
	legs, nextKey, err := s.loadOperatorLegPage(ctx, account.ID, filter)
	if err != nil {
		return billing.OperatorCostReport{}, err
	}
	rows := make([]billing.OperatorCostRow, 0, len(legs))
	for _, leg := range legs {
		attr := cogsByLeg[cogsLegKey(leg.TurnID, leg.BLegID)]
		rows = append(rows, billing.OperatorCostRow{
			TURKey: leg.TURKey, TurnID: leg.TurnID, ALegID: leg.ALegID, BLegID: leg.BLegID,
			ProviderID: leg.ProviderID, ModelID: leg.ModelID, Transaction: attr.Last,
			Amount: billing.Money{Nano: attr.Amount, Currency: currency},
		})
	}
	integrity, integrityErr := s.readIntegrityReport(ctx, account.ID)
	if integrityErr != nil {
		return billing.OperatorCostReport{}, integrityErr
	}
	issues = append(issues, integrity.Issues...)
	var unreconciled int
	if err := s.db.NewRaw(`
SELECT COUNT(1) FROM billing_operation_snapshots u
WHERE u.account_id = ? AND u.operation_kind = 'provider_cost_unreconciled'
AND NOT EXISTS (
	SELECT 1 FROM billing_operation_snapshots c
	WHERE c.account_id = u.account_id AND c.operation_kind = 'provider_call_cogs' AND c.source_key = u.source_key
)`, account.ID).Scan(ctx, &unreconciled); err != nil {
		return billing.OperatorCostReport{}, err
	}
	if unreconciled > 0 {
		issues = append(issues, billing.ReconciliationIssue{Code: "unreconciled_cost", Detail: account.ID})
	}
	return billing.OperatorCostReport{
		Rows: rows, CustomerRevenue: billing.Money{Nano: revenue, Currency: currency},
		ProviderCost: billing.Money{Nano: cost, Currency: currency}, GrossMargin: margin,
		UnreconciledCosts: unreconciled, NextKey: nextKey, Issues: issues,
	}, nil
}

func (s *DurableStore) TrialBalanceReport(ctx context.Context, filter billing.ReportFilter) (billing.TrialBalanceReport, error) {
	filter, err := filter.Normalize()
	if err != nil {
		return billing.TrialBalanceReport{}, err
	}
	if filter.AccountID == "" {
		return billing.TrialBalanceReport{}, fmt.Errorf("%w: trial balance requires account", billing.ErrReportInvalid)
	}
	account, err := s.GetAccount(ctx, filter.AccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return billing.TrialBalanceReport{}, billing.ErrReportNotFound
		}
		return billing.TrialBalanceReport{}, err
	}
	currency := account.Currency
	if filter.Currency != "" && filter.Currency != currency {
		return billing.TrialBalanceReport{}, billing.ErrMoneyCurrencyMismatch
	}
	transactions, next, err := s.loadJournalPage(ctx, account.ID, currency, filter.Book, "", filter.From, filter.To, filter.Page)
	if err != nil {
		return billing.TrialBalanceReport{}, err
	}
	providerTransactions, err := s.loadJournalRange(ctx, account.ID, currency, filter.Book, "provider_call_cogs", filter.From, filter.To)
	if err != nil {
		return billing.TrialBalanceReport{}, err
	}
	transactions = append(transactions, providerTransactions...)
	var debit, credit int64
	issues := make([]billing.ReconciliationIssue, 0)
	for _, transaction := range transactions {
		if err := transaction.Validate(); err != nil {
			issues = append(issues, billing.ReconciliationIssue{Code: "journal_invalid", Sequence: transaction.AccountSequence, Detail: transaction.ID})
		}
		for _, entry := range transaction.Entries {
			if entry.Amount.Currency != currency || entry.Amount.Nano <= 0 {
				issues = append(issues, billing.ReconciliationIssue{Code: "entry_invalid", Sequence: transaction.AccountSequence, Detail: transaction.ID})
				continue
			}
			switch entry.Side {
			case billing.JournalDebit:
				debit, err = billing.AddReportAmount(debit, entry.Amount, currency)
				if err != nil {
					issues = append(issues, billing.ReconciliationIssue{Code: "debit_overflow", Sequence: transaction.AccountSequence, Detail: transaction.ID})
				}
			case billing.JournalCredit:
				credit, err = billing.AddReportAmount(credit, entry.Amount, currency)
				if err != nil {
					issues = append(issues, billing.ReconciliationIssue{Code: "credit_overflow", Sequence: transaction.AccountSequence, Detail: transaction.ID})
				}
			default:
				issues = append(issues, billing.ReconciliationIssue{Code: "entry_side_invalid", Sequence: transaction.AccountSequence, Detail: transaction.ID})
			}
		}
	}
	imbalanceMoney, imbalanceErr := billing.ReportDifference(currency, debit, credit)
	if imbalanceErr != nil {
		issues = append(issues, billing.ReconciliationIssue{Code: "imbalance_overflow"})
	}
	byBook := make(map[billing.JournalBook]billing.TrialBalanceTotals)
	for _, transaction := range transactions {
		totals, exists := byBook[transaction.Book]
		if !exists {
			totals.Valid = true
		}
		for _, entry := range transaction.Entries {
			if entry.Side == billing.JournalDebit {
				amount, amountErr := billing.AddReportAmount(totals.Debit.Nano, entry.Amount, currency)
				if amountErr != nil {
					totals.Valid = false
					issues = append(issues, billing.ReconciliationIssue{Code: "book_debit_overflow", Sequence: transaction.AccountSequence, Detail: transaction.ID})
					continue
				}
				totals.Debit.Nano = amount
				totals.Debit.Currency = currency
			} else if entry.Side == billing.JournalCredit {
				amount, amountErr := billing.AddReportAmount(totals.Credit.Nano, entry.Amount, currency)
				if amountErr != nil {
					totals.Valid = false
					issues = append(issues, billing.ReconciliationIssue{Code: "book_credit_overflow", Sequence: transaction.AccountSequence, Detail: transaction.ID})
					continue
				}
				totals.Credit.Nano = amount
				totals.Credit.Currency = currency
			}
		}
		imbalance, imbalanceErr := billing.ReportDifference(currency, totals.Debit.Nano, totals.Credit.Nano)
		if imbalanceErr != nil {
			totals.Valid = false
			issues = append(issues, billing.ReconciliationIssue{Code: "book_imbalance_overflow", Sequence: transaction.AccountSequence, Detail: transaction.ID})
		} else {
			totals.Imbalance = imbalance
		}
		byBook[transaction.Book] = totals
	}
	integrity, integrityErr := s.readIntegrityReport(ctx, account.ID)
	if integrityErr != nil {
		return billing.TrialBalanceReport{}, integrityErr
	}
	if imbalanceErr != nil {
		imbalanceMoney = billing.Money{Currency: currency}
	}
	balancedByBook := true
	for _, totals := range byBook {
		if !totals.Valid || totals.Imbalance.Nano != 0 {
			balancedByBook = false
		}
	}
	pageBalanced := imbalanceMoney.Nano == 0 && balancedByBook && len(issues) == 0
	integrityOK := integrity.OK
	completePage := next == 0
	return billing.TrialBalanceReport{
		AccountID: account.ID, Currency: currency, From: filter.From, To: filter.To,
		Debit: billing.Money{Nano: debit, Currency: currency}, Credit: billing.Money{Nano: credit, Currency: currency},
		Imbalance: imbalanceMoney, ByBook: byBook, PageBalanced: pageBalanced,
		Balanced: pageBalanced && completePage && integrityOK, Transactions: len(transactions),
		NextCursor: next, Issues: issues, Integrity: &integrity,
	}, nil
}

func (s *DurableStore) loadJournalRange(ctx context.Context, accountID, currency string, book billing.JournalBook, operationKind string, from, to time.Time) ([]billing.JournalTransaction, error) {
	if book != "" && book != billing.JournalBookFinancial {
		return nil, fmt.Errorf("%w: unsupported current journal book %q", billing.ErrReportInvalid, book)
	}
	query := `SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND currency = ?`
	args := []any{accountID, currency}
	if book == "" {
		query += ` AND book = 'financial'`
	} else {
		query += ` AND book = ?`
		args = append(args, string(book))
	}
	if operationKind != "" {
		query += ` AND operation_kind = ?`
		args = append(args, operationKind)
	}
	if !from.IsZero() {
		query += ` AND recorded_at >= ?`
		args = append(args, from)
	}
	if !to.IsZero() {
		query += ` AND recorded_at < ?`
		args = append(args, to)
	}
	query += journalOrderClause(operationKind)
	var rows []journalTransactionRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, s.db, rows)
}

type operatorLegRow struct {
	Key        string `bun:"leg_key"`
	TURKey     string `bun:"tur_key"`
	TurnID     string `bun:"turn_id"`
	ALegID     string `bun:"a_leg_id"`
	BLegID     string `bun:"b_leg_id"`
	ProviderID string `bun:"provider_id"`
	ModelID    string `bun:"model_id"`
}

func (s *DurableStore) loadOperatorLegPage(ctx context.Context, accountID string, filter billing.ReportFilter) ([]operatorLegRow, string, error) {
	afterKey := strings.TrimSpace(filter.AfterKey)
	if afterKey == "" {
		afterKey = strings.TrimSpace(filter.Page.AfterKey)
	}
	query := `SELECT l.usage_leg_key AS leg_key, l.call_id AS tur_key, l.call_id AS turn_id, l.a_leg_id, l.b_leg_id, l.provider_id, l.model_id FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.usage_leg_key > ?`
	args := []any{accountID, afterKey}
	if !filter.From.IsZero() {
		query += ` AND l.finished_at >= ?`
		args = append(args, filter.From)
	}
	if !filter.To.IsZero() {
		query += ` AND l.finished_at < ?`
		args = append(args, filter.To)
	}
	query += ` ORDER BY l.usage_leg_key LIMIT ?`
	args = append(args, filter.Page.Limit+1)
	var rows []operatorLegRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > filter.Page.Limit {
		rows = rows[:filter.Page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].Key
		}
	}
	return rows, next, nil
}

type blegCogs struct {
	Amount int64
	Last   billing.JournalTransaction
}

func cogsLegKey(turnID, bLegID string) string {
	return strings.TrimSpace(turnID) + "\x00" + strings.TrimSpace(bLegID)
}

func netCogsByCallAndBLeg(transactions []billing.JournalTransaction, currency string) map[string]blegCogs {
	out := make(map[string]blegCogs)
	for _, tx := range transactions {
		if strings.TrimSpace(tx.BLegID) == "" || strings.TrimSpace(tx.TurnID) == "" {
			continue
		}
		_, cost, _ := billing.SummarizeJournalForReport([]billing.JournalTransaction{tx}, currency)
		key := cogsLegKey(tx.TurnID, tx.BLegID)
		attr := out[key]
		attr.Amount += cost
		if attr.Last.ID == "" || tx.RecordedAt.After(attr.Last.RecordedAt) || (tx.RecordedAt.Equal(attr.Last.RecordedAt) && tx.ID > attr.Last.ID) {
			attr.Last = tx
		}
		out[key] = attr
	}
	return out
}

func (s *DurableStore) loadJournalPage(ctx context.Context, accountID, currency string, book billing.JournalBook, operationKind string, from, to time.Time, page billing.PageRequest) ([]billing.JournalTransaction, uint64, error) {
	if book != "" && book != billing.JournalBookFinancial {
		return nil, 0, fmt.Errorf("%w: unsupported current journal book %q", billing.ErrReportInvalid, book)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, 0, err
	}
	query := `SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND currency = ? AND account_sequence IS NOT NULL AND account_sequence > ?`
	args := []any{accountID, currency, page.AfterSequence}
	if book == "" {
		query += ` AND book = 'financial'`
	} else {
		query += ` AND book = ?`
		args = append(args, string(book))
	}
	if operationKind != "" {
		query += ` AND operation_kind = ?`
		args = append(args, operationKind)
	}
	if !from.IsZero() {
		query += ` AND recorded_at >= ?`
		args = append(args, from)
	}
	if !to.IsZero() {
		query += ` AND recorded_at < ?`
		args = append(args, to)
	}
	query += ` ORDER BY account_sequence LIMIT ?`
	args = append(args, page.Limit+1)
	var rows []journalTransactionRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, 0, err
	}
	next := uint64(0)
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		if len(rows) > 0 && rows[len(rows)-1].AccountSequence.Valid && rows[len(rows)-1].AccountSequence.Int64 > 0 {
			next = uint64(rows[len(rows)-1].AccountSequence.Int64)
		}
	}
	out, err := loadJournals(ctx, s.db, rows)
	if err != nil {
		return nil, 0, err
	}
	return out, next, nil
}

func journalsForTurnID(transactions []billing.JournalTransaction, ids ...string) []billing.JournalTransaction {
	out := make([]billing.JournalTransaction, 0)
	for _, tx := range transactions {
		for _, id := range ids {
			if tx.TurnID == id {
				out = append(out, tx)
				break
			}
		}
	}
	return out
}

var (
	_ billing.ReportingStore       = (*DurableStore)(nil)
	_ billing.AuthoritativeBilling = (*DurableStore)(nil)
)
