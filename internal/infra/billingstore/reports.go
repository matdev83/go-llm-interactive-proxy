package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// AccountReport returns only immutable journal evidence and materialized account
// state. It never reads raw metering facts or exposes sealed payload JSON.
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
	return billing.AccountReport{
		Account:         account,
		SpendableNano:   spendable,
		CreditFloorNano: account.CreditFloorNano(),
		Transactions:    transactions,
		NextCursor:      next,
	}, nil
}

// TurnExplanation joins sealed TUR/LUR evidence with processing, authorization,
// journal and point-in-time snapshot evidence. Missing processing or hold rows
// leave those fields zero-valued; only a missing TUR is not found. All returned
// fields are billing safe; prompts, completions, credentials and provider wire
// objects are absent.
func (s *DurableStore) TurnExplanation(ctx context.Context, turKey string) (billing.TurnExplanation, error) {
	record, err := s.GetUsageRecord(ctx, strings.TrimSpace(turKey))
	if err != nil {
		if errors.Is(err, ErrUsageRecordNotFound) {
			return billing.TurnExplanation{}, billing.ErrReportNotFound
		}
		return billing.TurnExplanation{}, err
	}
	var processing billing.UsageRecordProcessing
	processing, err = s.GetProcessing(ctx, record.Key)
	if err != nil {
		if !errors.Is(err, ErrProcessingNotFound) {
			return billing.TurnExplanation{}, err
		}
		processing = billing.UsageRecordProcessing{}
	}
	var authorization billing.AuthorizationReport
	authorization, err = s.authorizationReport(ctx, record.AccountID, record.AuthorizationID, record.Key)
	if err != nil {
		if !errors.Is(err, ErrAuthorizationHoldNotFound) {
			return billing.TurnExplanation{}, err
		}
		authorization = billing.AuthorizationReport{}
	}
	transactions, err := s.loadTurnJournals(ctx, record)
	if err != nil {
		return billing.TurnExplanation{}, err
	}
	snapshots, err := s.loadTurnSnapshots(ctx, record)
	if err != nil {
		return billing.TurnExplanation{}, err
	}
	integrity, err := s.readIntegrityReport(ctx, record.AccountID)
	if err != nil {
		return billing.TurnExplanation{}, err
	}
	currency := authorization.Amount.Currency
	if currency == "" {
		account, accErr := s.GetAccount(ctx, record.AccountID)
		if accErr != nil {
			return billing.TurnExplanation{}, accErr
		}
		currency = account.Currency
	}
	revenue, cost, issues := billing.SummarizeJournalForReport(transactions, currency)
	integrity.Issues = append(integrity.Issues, issues...)
	margin, marginErr := billing.ReportMargin(currency, revenue, cost)
	if marginErr != nil {
		integrity.Issues = append(integrity.Issues, billing.ReconciliationIssue{Code: "margin_overflow", Detail: record.Key})
	}
	if len(integrity.Issues) > 0 {
		integrity.OK = false
	}
	return billing.TurnExplanation{Record: record, Processing: processing, Authorization: authorization, Transactions: transactions, Snapshots: snapshots, Reconciliation: &integrity, Result: billing.TurnResultSummary{CustomerCharge: billing.Money{Currency: currency, Nano: revenue}, ProviderCost: billing.Money{Currency: currency, Nano: cost}, GrossMargin: margin, Processed: processing.Status == billing.ProcessingProcessed, Status: processing.Status}}, nil
}

// SessionReport pages A-leg TURs that share one proxy-owned session identity.
// Headline revenue, COGS, and margin are netted totals for the whole session
// and do not change across pages.
func (s *DurableStore) SessionReport(ctx context.Context, accountID, sessionID string, page billing.PageRequest) (billing.SessionReport, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.SessionReport{}, err
	}
	accountID = strings.TrimSpace(accountID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || sessionID == "" {
		return billing.SessionReport{}, fmt.Errorf("%w: session report requires account and session", billing.ErrReportInvalid)
	}
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return billing.SessionReport{}, billing.ErrReportNotFound
		}
		return billing.SessionReport{}, err
	}
	currency := account.Currency
	transactions, err := s.loadSessionJournals(ctx, account.ID, sessionID)
	if err != nil {
		return billing.SessionReport{}, err
	}
	revenue, cost, issues := billing.SummarizeJournalForReport(transactions, currency)
	margin, err := billing.ReportMargin(currency, revenue, cost)
	if err != nil {
		return billing.SessionReport{}, err
	}
	turKeys, nextKey, err := s.loadSessionTurnPage(ctx, account.ID, sessionID, page)
	if err != nil {
		return billing.SessionReport{}, err
	}
	rows := make([]billing.SessionReportRow, 0, len(turKeys))
	for _, turKey := range turKeys {
		record, recErr := s.GetUsageRecord(ctx, turKey)
		if recErr != nil {
			return billing.SessionReport{}, recErr
		}
		turnRevenue, turnCost, turnIssues := billing.SummarizeJournalForReport(journalsForTurn(transactions, record), currency)
		issues = append(issues, turnIssues...)
		turnMargin, marginErr := billing.ReportMargin(currency, turnRevenue, turnCost)
		if marginErr != nil {
			issues = append(issues, billing.ReconciliationIssue{Code: "margin_overflow", Detail: record.Key})
			turnMargin = billing.Money{Currency: currency}
		}
		rows = append(rows, billing.SessionReportRow{
			TURKey: record.Key, TurnID: record.TurnID, ALegID: record.ALegID,
			CustomerCharge: billing.Money{Nano: turnRevenue, Currency: currency},
			ProviderCost:   billing.Money{Nano: turnCost, Currency: currency},
			GrossMargin:    turnMargin,
		})
	}
	return billing.SessionReport{
		AccountID: account.ID, SessionID: sessionID, Rows: rows,
		CustomerRevenue: billing.Money{Nano: revenue, Currency: currency},
		ProviderCost:    billing.Money{Nano: cost, Currency: currency},
		GrossMargin:     margin, NextKey: nextKey, Issues: issues,
	}, nil
}

// OperatorCostReport pages B-leg LURs for the account. Headline revenue, COGS,
// and margin are netted journal totals for the selected filter range and do not
// change across pages. Zero-amount legs still appear as rows.
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
	cogsByBLeg := netCogsByBLeg(transactions, currency)
	legs, nextKey, err := s.loadOperatorLegPage(ctx, account.ID, filter)
	if err != nil {
		return billing.OperatorCostReport{}, err
	}
	rows := make([]billing.OperatorCostRow, 0, len(legs))
	for _, leg := range legs {
		attr := cogsByBLeg[leg.BLegID]
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
	return billing.OperatorCostReport{
		Rows: rows, CustomerRevenue: billing.Money{Nano: revenue, Currency: currency},
		ProviderCost: billing.Money{Nano: cost, Currency: currency}, GrossMargin: margin,
		NextKey: nextKey, Issues: issues,
	}, nil
}

// TrialBalanceReport proves exact debit/credit equality for the bounded journal
// page selected by the filter. It does not reinterpret raw usage or metering.
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
	page := billing.PageRequest{Limit: 1000}
	var out []billing.JournalTransaction
	for {
		transactions, next, err := s.loadJournalPage(ctx, accountID, currency, book, operationKind, from, to, page)
		if err != nil {
			return nil, err
		}
		out = append(out, transactions...)
		if next == 0 {
			return out, nil
		}
		page.AfterSequence = next
	}
}

type operatorLegRow struct {
	LURKey     string `bun:"lur_key"`
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
	query := `SELECT l.lur_key, l.tur_key, t.turn_id, l.a_leg_id, l.b_leg_id, l.provider_id, l.model_id FROM leg_usage_records l JOIN turn_usage_records t ON t.tur_key = l.tur_key WHERE t.account_id = ? AND l.lur_key > ?`
	args := []any{accountID, afterKey}
	if !filter.From.IsZero() {
		query += ` AND t.finished_at >= ?`
		args = append(args, filter.From)
	}
	if !filter.To.IsZero() {
		query += ` AND t.finished_at < ?`
		args = append(args, filter.To)
	}
	query += ` ORDER BY l.lur_key LIMIT ?`
	args = append(args, filter.Page.Limit+1)
	var rows []operatorLegRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > filter.Page.Limit {
		rows = rows[:filter.Page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].LURKey
		}
	}
	return rows, next, nil
}

type blegCogs struct {
	Amount int64
	Last   billing.JournalTransaction
}

func netCogsByBLeg(transactions []billing.JournalTransaction, currency string) map[string]blegCogs {
	out := make(map[string]blegCogs)
	for _, tx := range transactions {
		if strings.TrimSpace(tx.BLegID) == "" {
			continue
		}
		_, cost, _ := billing.SummarizeJournalForReport([]billing.JournalTransaction{tx}, currency)
		attr := out[tx.BLegID]
		attr.Amount += cost
		if tx.AccountSequence >= attr.Last.AccountSequence {
			attr.Last = tx
		}
		out[tx.BLegID] = attr
	}
	return out
}

func (s *DurableStore) loadJournalPage(ctx context.Context, accountID, currency string, book billing.JournalBook, operationKind string, from, to time.Time, page billing.PageRequest) ([]billing.JournalTransaction, uint64, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, 0, err
	}
	query := `SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND currency = ? AND account_sequence > ?`
	args := []any{accountID, currency, page.AfterSequence}
	if book != "" {
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
		if len(rows) > 0 {
			next = rows[len(rows)-1].AccountSequence
		}
	}
	out, err := loadJournals(ctx, s.db, rows)
	if err != nil {
		return nil, 0, err
	}
	return out, next, nil
}

func (s *DurableStore) loadTurnJournals(ctx context.Context, record billing.TurnUsageRecord) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	if err := s.db.NewRaw(`SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND (turn_id = ? OR turn_id = ? OR a_leg_id = ?) ORDER BY account_sequence`, record.AccountID, record.TurnID, record.Key, record.ALegID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, s.db, rows)
}

func (s *DurableStore) loadSessionJournals(ctx context.Context, accountID, sessionID string) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	query := `SELECT j.transaction_id, j.account_id, j.book, j.currency, j.source_key, j.semantic_fingerprint, j.turn_id, j.a_leg_id, j.b_leg_id, j.account_sequence, j.reversal_of, j.corrects_transaction_id, j.correction_group_id, j.operation_kind, j.balance_before_nano, j.balance_after_nano, j.reserved_before_nano, j.reserved_after_nano, j.spendable_before_nano, j.spendable_after_nano, j.credit_floor_nano, j.credit_limit_nano, j.mode, j.snapshot_version_before, j.snapshot_version_after, j.recorded_at FROM journal_transactions j WHERE j.account_id = ? AND EXISTS (SELECT 1 FROM turn_usage_records t WHERE t.account_id = j.account_id AND t.session_id = ? AND (j.turn_id = t.turn_id OR j.turn_id = t.tur_key OR j.a_leg_id = t.a_leg_id)) ORDER BY j.account_sequence`
	if err := s.db.NewRaw(query, accountID, sessionID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, s.db, rows)
}

func (s *DurableStore) loadSessionTurnPage(ctx context.Context, accountID, sessionID string, page billing.PageRequest) ([]string, string, error) {
	afterKey := strings.TrimSpace(page.AfterKey)
	var rows []struct {
		TURKey string `bun:"tur_key"`
	}
	if err := s.db.NewRaw(`SELECT tur_key FROM turn_usage_records WHERE account_id = ? AND session_id = ? AND tur_key > ? ORDER BY tur_key LIMIT ?`, accountID, sessionID, afterKey, page.Limit+1).Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].TURKey
		}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.TURKey)
	}
	return keys, next, nil
}

func journalsForTurn(transactions []billing.JournalTransaction, record billing.TurnUsageRecord) []billing.JournalTransaction {
	out := make([]billing.JournalTransaction, 0)
	for _, tx := range transactions {
		if tx.TurnID == record.TurnID || tx.TurnID == record.Key || tx.ALegID == record.ALegID {
			out = append(out, tx)
		}
	}
	return out
}

func (s *DurableStore) authorizationReport(ctx context.Context, accountID, authorizationID, turKey string) (billing.AuthorizationReport, error) {
	var row authorizationHoldRow
	err := s.db.NewRaw(`SELECT hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, source_key, fingerprint, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, released_amount_nano, closed_source_key, closed_fingerprint, closed_amount_nano, expires_at, created_at, closed_at FROM authorization_holds WHERE account_id = ? AND authorization_id = ? AND tur_key = ?`, accountID, authorizationID, turKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.AuthorizationReport{}, ErrAuthorizationHoldNotFound
	}
	if err != nil {
		return billing.AuthorizationReport{}, err
	}
	auth, err := authorizationFromRow(row)
	if err != nil {
		return billing.AuthorizationReport{}, err
	}
	out := billing.AuthorizationReport{ID: auth.ID, AccountID: auth.AccountID, TURKey: auth.TURKey, Amount: auth.Amount, Status: row.Status, PricingRef: auth.PricingRef, ChargePolicyRef: auth.ChargePolicyRef, Before: auth.Before, After: auth.After, ClosedReason: billing.ReleaseReason(row.ClosedReason), ReleasedAmount: billing.Money{Nano: row.ReleasedAmount, Currency: row.Currency}, CreatedAt: row.CreatedAt}
	if row.ClosedAt != nil {
		out.ClosedAt = *row.ClosedAt
	}
	return out, nil
}

var (
	_ billing.ReportingStore       = (*DurableStore)(nil)
	_ billing.AuthoritativeBilling = (*DurableStore)(nil)
)

func (s *DurableStore) loadTurnSnapshots(ctx context.Context, record billing.TurnUsageRecord) ([]billing.OperationSnapshot, error) {
	keys := []string{record.Key, settlementReleaseSourceKey(record.Key)}
	for _, leg := range record.Legs {
		keys = append(keys, leg.Key)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+1)
	args = append(args, record.AccountID)
	for _, key := range keys {
		args = append(args, key)
	}
	var rows []operationSnapshotRow
	query := `SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND source_key IN (` + placeholders + `) ORDER BY account_sequence_end, operation_key`
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]billing.OperationSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, billing.OperationSnapshot{OperationKey: row.OperationKey, OperationKind: row.OperationKind, SourceKey: row.SourceKey, Fingerprint: row.Fingerprint, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), Before: billing.AccountSnapshot{BalanceNano: row.BalanceBefore, ReservedNano: row.ReservedBefore, SpendableNano: row.SpendableBefore, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore}, After: billing.AccountSnapshot{BalanceNano: row.BalanceAfter, ReservedNano: row.ReservedAfter, SpendableNano: row.SpendableAfter, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter}, SequenceStart: row.SequenceStart, SequenceEnd: row.SequenceEnd, CreatedAt: row.CreatedAt})
	}
	return out, nil
}
