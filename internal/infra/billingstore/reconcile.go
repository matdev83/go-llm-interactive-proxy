package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

var ErrReconciliationFailed = errors.New("billingstore: reconciliation failed")

func (s *DurableStore) AccountStatus(ctx context.Context, accountID string) (billing.Account, error) {
	return s.GetAccount(ctx, accountID)
}

func (s *DurableStore) MarkAccountReconcileRequired(ctx context.Context, accountID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), strings.TrimSpace(accountID)); err != nil {
		return err
	}
	if err := setReconcileRequiredTx(ctx, tx, strings.TrimSpace(accountID)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DurableStore) ReconcileAccount(ctx context.Context, accountID string) (billing.ReconciliationReport, error) {
	if s == nil || s.db == nil {
		return billing.ReconciliationReport{}, fmt.Errorf("billingstore: nil store")
	}
	accountID = strings.TrimSpace(accountID)
	var lastErr error
	for attempt := range 20 {
		report, stale, err := s.reconcileAccountAttempt(ctx, accountID)
		if !stale {
			return report, err
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("billingstore: reconcile snapshot went stale")
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*5*time.Millisecond); waitErr != nil {
			return billing.ReconciliationReport{}, waitErr
		}
	}
	return billing.ReconciliationReport{}, fmt.Errorf("%w: %v", billing.ErrBillingStoreUnavailable, lastErr)
}

type reconcileSnapshot struct {
	Account          billing.Account
	OpeningBalance   int64
	OpeningCurrency  string
	OpeningMode      string
	OpeningLimit     int64
	OpeningPresent   bool
	OpeningFP        string
	ExpectedCurrency string
	ExpectedMode     string
	ExpectedLimit    int64
	PolicyMismatch   bool
	Journals         []billing.JournalTransaction
	Snapshots        []operationSnapshotRow
	BlockedEvents    int
}

func (s *DurableStore) reconcileAccountAttempt(ctx context.Context, accountID string) (billing.ReconciliationReport, bool, error) {
	snap, err := s.loadReconcileSnapshot(ctx, accountID)
	if err != nil {
		return billing.ReconciliationReport{}, false, err
	}
	var report billing.ReconciliationReport
	if !snap.OpeningPresent {
		report = billing.ReconciliationReport{AccountID: accountID}
		report.AddIssue("opening_evidence_missing", 0, accountID)
		return s.commitReconcileOutcome(ctx, accountID, snap, report, false)
	}
	replayAccount := snap.Account
	replayAccount.Currency = snap.ExpectedCurrency
	replayAccount.Mode = billing.AccountMode(snap.ExpectedMode)
	replayAccount.CreditLimit = snap.ExpectedLimit
	report = billing.ReplayAccount(replayAccount, snap.OpeningBalance, snap.Journals)
	if snap.PolicyMismatch && snap.Account.State == billing.AccountReady {
		report.AddIssue("opening_policy_mismatch", 0, "immutable opening/policy evidence does not match materialized policy")
	}
	validateSettlementSnapshotsRows(snap.Account.ID, snap.Snapshots, snap.Journals, snap.Account.State == billing.AccountReconcileRequired, &report)
	if report.OK && snap.Account.State == billing.AccountReady && (report.Rebuilt.BalanceNano != snap.Account.BalanceNano || report.Rebuilt.ReservedNano != snap.Account.ReservedNano || report.Rebuilt.SpendableNano != mustSpendable(snap.Account)) {
		report.AddIssue("materialized_state_mismatch", 0, "materialized balance/reserved/spendable differs from journal replay")
	}
	if report.OK && snap.Account.State == billing.AccountReconcileRequired && snap.BlockedEvents == 0 {
		report.AddIssue("reconciliation_audit_missing", 0, "reconcile_required has no audited transition")
	}
	return s.commitReconcileOutcome(ctx, accountID, snap, report, report.OK)
}

func (s *DurableStore) loadReconcileSnapshot(ctx context.Context, accountID string) (reconcileSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return reconcileSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	account, err := getAccountForReconcileTx(ctx, tx, accountID)
	if err != nil {
		return reconcileSnapshot{}, err
	}
	snap := reconcileSnapshot{Account: account}
	var opening struct {
		Balance     int64  `bun:"opening_balance_nano"`
		Currency    string `bun:"currency"`
		Mode        string `bun:"mode"`
		CreditLimit int64  `bun:"credit_limit_nano"`
		Fingerprint string `bun:"fingerprint"`
	}
	if err := tx.NewRaw(`SELECT opening_balance_nano, currency, mode, credit_limit_nano, fingerprint FROM billing_account_openings WHERE account_id = ?`, accountID).Scan(ctx, &opening); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return reconcileSnapshot{}, err
		}
		return snap, nil
	}
	snap.OpeningPresent = true
	snap.OpeningBalance = opening.Balance
	snap.OpeningCurrency = opening.Currency
	snap.OpeningMode = opening.Mode
	snap.OpeningLimit = opening.CreditLimit
	snap.OpeningFP = opening.Fingerprint
	snap.ExpectedCurrency, snap.ExpectedMode, snap.ExpectedLimit = opening.Currency, opening.Mode, opening.CreditLimit
	var latestPolicy struct {
		Currency    string `bun:"currency"`
		Mode        string `bun:"mode"`
		CreditLimit int64  `bun:"credit_limit_nano"`
	}
	policyErr := tx.NewRaw(`SELECT currency, mode, credit_limit_nano FROM billing_account_policy_events WHERE account_id = ? ORDER BY id DESC LIMIT 1`, accountID).Scan(ctx, &latestPolicy)
	if policyErr == nil {
		snap.ExpectedCurrency, snap.ExpectedMode, snap.ExpectedLimit = latestPolicy.Currency, latestPolicy.Mode, latestPolicy.CreditLimit
	} else if !errors.Is(policyErr, sql.ErrNoRows) {
		return reconcileSnapshot{}, policyErr
	}
	snap.PolicyMismatch = snap.ExpectedCurrency != account.Currency || snap.ExpectedMode != string(account.Mode) || snap.ExpectedLimit != account.CreditLimit || opening.Fingerprint == ""
	journals, err := loadAllJournals(ctx, tx, accountID)
	if err != nil {
		return reconcileSnapshot{}, err
	}
	snap.Journals = journals
	if err := tx.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? ORDER BY account_sequence_end, operation_key`, accountID).Scan(ctx, &snap.Snapshots); err != nil {
		return reconcileSnapshot{}, err
	}
	if account.State == billing.AccountReconcileRequired {
		if err := tx.NewRaw(`SELECT COUNT(1) FROM billing_reconciliation_events WHERE account_id = ? AND to_state = 'reconcile_required'`, accountID).Scan(ctx, &snap.BlockedEvents); err != nil {
			return reconcileSnapshot{}, err
		}
	}
	return snap, nil
}

func (s *DurableStore) commitReconcileOutcome(ctx context.Context, accountID string, snap reconcileSnapshot, report billing.ReconciliationReport, repair bool) (billing.ReconciliationReport, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return report, false, err
	}
	account, err := getAccountForReconcileTx(ctx, tx, accountID)
	if err != nil {
		return report, false, err
	}
	if account.Version != snap.Account.Version || account.State != snap.Account.State ||
		account.BalanceNano != snap.Account.BalanceNano || account.ReservedNano != snap.Account.ReservedNano ||
		account.Currency != snap.Account.Currency || account.Mode != snap.Account.Mode || account.CreditLimit != snap.Account.CreditLimit {
		return report, true, nil
	}
	var maxSeq uint64
	var journalCount int
	if err := tx.NewRaw(`SELECT COALESCE(MAX(account_sequence), 0), COUNT(1) FROM journal_transactions WHERE account_id = ?`, accountID).Scan(ctx, &maxSeq, &journalCount); err != nil {
		return report, false, err
	}
	var snapMax uint64
	if len(snap.Journals) > 0 {
		snapMax = snap.Journals[len(snap.Journals)-1].AccountSequence
	}
	if maxSeq != snapMax || journalCount != len(snap.Journals) {
		return report, true, nil
	}
	if !snap.OpeningPresent {
		if markErr := setReconcileRequiredTx(ctx, tx, accountID); markErr != nil {
			return report, false, markErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return report, false, commitErr
		}
		return report, false, fmt.Errorf("%w: opening evidence missing", ErrReconciliationFailed)
	}
	if !repair {
		if err := setReconcileRequiredTx(ctx, tx, accountID); err != nil {
			return report, false, err
		}
		if err := tx.Commit(); err != nil {
			return report, false, err
		}
		return report, false, ErrReconciliationFailed
	}
	eventKey := fmt.Sprintf("reconcile:v1:%s:%d:%d:%d:%d", accountID, account.Version, report.Rebuilt.BalanceNano, report.Rebuilt.ReservedNano, report.Rebuilt.SpendableNano)
	var existingEvent string
	eventErr := tx.NewRaw(`SELECT event_key FROM billing_reconciliation_events WHERE event_key = ?`, eventKey).Scan(ctx, &existingEvent)
	if eventErr != nil && !errors.Is(eventErr, sql.ErrNoRows) {
		return report, false, eventErr
	}
	if errors.Is(eventErr, sql.ErrNoRows) {
		if _, err := tx.NewRaw(`INSERT INTO billing_reconciliation_events(event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, reserved_nano, spendable_nano, created_at) VALUES (?,?,?,?,?,?,?,?,?)`, eventKey, accountID, string(account.State), string(billing.AccountReady), report.FirstMismatchSequence, report.Rebuilt.BalanceNano, report.Rebuilt.ReservedNano, report.Rebuilt.SpendableNano, nowUTC()).Exec(ctx); err != nil {
			return report, false, err
		}
	}
	accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET currency = ?, mode = ?, credit_limit_nano = ?, balance_nano = ?, reserved_nano = 0, version = ?, state = 'ready', updated_at = ? WHERE account_id = ? AND version = ?`, snap.ExpectedCurrency, snap.ExpectedMode, snap.ExpectedLimit, report.Rebuilt.BalanceNano, report.Rebuilt.Version, nowUTC(), accountID, account.Version).Exec(ctx)
	if err != nil {
		return report, false, err
	}
	if err := requireRowsAffected(accountResult, 1, "reconcile account version"); err != nil {
		return report, true, nil
	}
	if err := tx.Commit(); err != nil {
		return report, false, err
	}
	return report, false, nil
}

func mustSpendable(account billing.Account) int64 {
	spendable, _ := account.SpendableNano()
	return spendable
}

func setReconcileRequiredTx(ctx context.Context, tx bun.Tx, accountID string) error {
	var fromState string
	if err := tx.NewRaw(`SELECT state FROM billing_accounts WHERE account_id = ?`, accountID).Scan(ctx, &fromState); err != nil {
		return err
	}
	now := nowUTC()
	eventKey := fmt.Sprintf("reconcile-required:v1:%s:%d", accountID, now.UnixNano())
	if _, err := tx.NewRaw(`INSERT INTO billing_reconciliation_events(event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, reserved_nano, spendable_nano, created_at) VALUES (?,?,?,?,0,0,0,0,?)`, eventKey, accountID, fromState, string(billing.AccountReconcileRequired), now).Exec(ctx); err != nil {
		return err
	}
	_, err := tx.NewRaw(`UPDATE billing_accounts SET state = 'reconcile_required', updated_at = ? WHERE account_id = ?`, now, accountID).Exec(ctx)
	return err
}

func loadAllJournals(ctx context.Context, q bun.IDB, accountID string) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	if err := q.NewRaw(`SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? ORDER BY account_sequence`, accountID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, q, rows)
}

func validateSettlementSnapshots(ctx context.Context, tx bun.Tx, accountID string, journals []billing.JournalTransaction, allowMaterializedDrift bool, report *billing.ReconciliationReport) {
	var rows []operationSnapshotRow
	if err := tx.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? ORDER BY account_sequence_end, operation_key`, accountID).Scan(ctx, &rows); err != nil {
		report.AddIssue("snapshot_read_failed", 0, err.Error())
		return
	}
	validateSettlementSnapshotsRows(accountID, rows, journals, allowMaterializedDrift, report)
}

func validateSettlementSnapshotsRows(accountID string, rows []operationSnapshotRow, journals []billing.JournalTransaction, allowMaterializedDrift bool, report *billing.ReconciliationReport) {
	bySequence := make(map[uint64]billing.JournalTransaction, len(journals))
	bySource := make(map[string]struct{}, len(journals))
	maxSnapshotVersion := uint64(0)
	for _, journal := range journals {
		bySequence[journal.AccountSequence] = journal
		if journal.SourceKey != "" {
			bySource[journal.SourceKey] = struct{}{}
		}
	}
	for _, row := range rows {
		if row.AccountID != accountID || row.SequenceStart > row.SequenceEnd || row.Fingerprint == "" || (row.IntegrityFingerprint != "" && row.IntegrityFingerprint != snapshotIntegrity(row.OperationKey, row.AccountID, row.OperationKind, row.SourceKey, row.Fingerprint, billing.AccountSnapshot{BalanceNano: row.BalanceBefore, ReservedNano: row.ReservedBefore, SpendableNano: row.SpendableBefore, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore}, billing.AccountSnapshot{BalanceNano: row.BalanceAfter, ReservedNano: row.ReservedAfter, SpendableNano: row.SpendableAfter, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter}, row.SequenceStart, row.SequenceEnd)) || row.Currency == "" || row.Mode == "" {
			report.AddIssue("snapshot_invalid", row.SequenceEnd, row.OperationKey)
			continue
		}
		if row.VersionAfter > maxSnapshotVersion {
			maxSnapshotVersion = row.VersionAfter
		}
		if row.SequenceEnd == 0 {
			switch row.OperationKind {
			case "customer_settlement", "provider_cogs", "authorization_release", "authorization":
				if _, posted := bySource[row.OperationKey]; posted {
					report.AddIssue("snapshot_sequence_missing", 0, row.OperationKey)
				}
			}
			continue
		}
		for sequence := row.SequenceStart; sequence <= row.SequenceEnd; sequence++ {
			if _, ok := bySequence[sequence]; !ok {
				report.AddIssue("snapshot_sequence_range_incomplete", sequence, row.OperationKey)
			}
			if sequence == ^uint64(0) {
				break
			}
		}
		startJournal, startOK := bySequence[row.SequenceStart]
		journal, ok := bySequence[row.SequenceEnd]
		if !ok {
			report.AddIssue("snapshot_sequence_missing", row.SequenceEnd, row.OperationKey)
			continue
		}
		if !startOK || row.BalanceBefore != startJournal.BalanceBefore || row.ReservedBefore != startJournal.ReservedBefore || row.SpendableBefore != startJournal.SpendableBefore || row.VersionBefore != startJournal.SnapshotVersionBefore || row.BalanceAfter != journal.BalanceAfter || row.ReservedAfter != journal.ReservedAfter || row.SpendableAfter != journal.SpendableAfter || row.VersionAfter != journal.SnapshotVersionAfter || row.Currency != journal.Currency || row.Mode != journal.Mode || row.CreditFloor != journal.CreditFloor || row.CreditLimit != journal.CreditLimit {
			report.AddIssue("snapshot_mismatch", row.SequenceEnd, row.OperationKey)
		}
	}
	if maxSnapshotVersion != 0 {
		if report.Current.Version != maxSnapshotVersion && !allowMaterializedDrift {
			report.AddIssue("snapshot_version_mismatch", 0, fmt.Sprintf("materialized=%d snapshot=%d", report.Current.Version, maxSnapshotVersion))
		}
		if report.Rebuilt.Version == 0 || allowMaterializedDrift {
			report.Rebuilt.Version = maxSnapshotVersion
		}
	}
}
func nowUTC() time.Time { return time.Now().UTC() }
