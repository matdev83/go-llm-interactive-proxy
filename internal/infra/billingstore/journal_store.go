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
	"github.com/uptrace/bun/dialect"
)

var (
	ErrIdentityConflict           = errors.New("billingstore: idempotency identity conflict")
	ErrCorrectionInvalid          = errors.New("billingstore: invalid correction link")
	ErrAccountNotFound            = errors.New("billingstore: account not found")
	ErrUsageRecordNotFound        = errors.New("billingstore: usage record not found")
	ErrSequenceConflict           = errors.New("billingstore: account sequence conflict")
	ErrEvidenceIncomplete         = errors.New("billingstore: operation evidence incomplete")
	ErrProviderJournalGenericPath = errors.New("billingstore: provider COGS requires provider journal writer")
)

var (
	_ billing.ExposureAdmissionStore = (*DurableStore)(nil)
	_ billing.CallUsageStore         = (*DurableStore)(nil)
	_ billing.CompleteCallClaimer    = (*DurableStore)(nil)
	_ billing.AccountProvisioner     = (*DurableStore)(nil)
)

func (s *DurableStore) CreateAccount(ctx context.Context, account billing.Account) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := account.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.NewRaw(`INSERT INTO billing_accounts(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, version, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, account.ID, account.Currency, string(account.Mode), account.CreditLimit, account.BalanceNano, account.BalanceNano, account.Version, string(account.State), now, now).Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			return wrapAccountProvisionerError(fmt.Errorf("%w: account %s already exists", ErrIdentityConflict, account.ID))
		}
		return err
	}
	openingFingerprint := fmt.Sprintf("opening:v1:%s:%d:%s:%s:%d", account.ID, account.BalanceNano, account.Currency, account.Mode, account.CreditLimit)
	if _, err := tx.NewRaw(`INSERT INTO billing_account_openings(account_id, opening_balance_nano, currency, mode, credit_limit_nano, fingerprint, created_at) VALUES (?,?,?,?,?,?,?)`, account.ID, account.BalanceNano, account.Currency, string(account.Mode), account.CreditLimit, openingFingerprint, now).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func wrapAccountProvisionerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrIdentityConflict), errors.Is(err, ErrOperationConflict):
		return fmt.Errorf("%w: %w", billing.ErrAccountConflict, err)
	case errors.Is(err, ErrAccountNotFound):
		return fmt.Errorf("%w: %w", billing.ErrAccountNotFound, err)
	default:
		return err
	}
}

func (s *DurableStore) GetAccount(ctx context.Context, accountID string) (billing.Account, error) {
	return getAccountTx(ctx, s.db, strings.TrimSpace(accountID))
}

func (s *DurableStore) postJournalTransaction(ctx context.Context, input billing.JournalTransaction) (billing.JournalTransaction, error) {
	if s == nil || s.db == nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: nil store")
	}
	if input.OperationKind == "provider_call_cogs" {
		return billing.JournalTransaction{}, ErrProviderJournalGenericPath
	}
	if err := input.Validate(); err != nil {
		return billing.JournalTransaction{}, err
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 80, Delay: 5 * time.Millisecond}, func() (billing.JournalTransaction, error) {
		return s.postJournalAttempt(ctx, input)
	})
}

func (s *DurableStore) postJournalAttempt(ctx context.Context, input billing.JournalTransaction) (billing.JournalTransaction, error) {
	if input.OperationKind == "provider_call_cogs" {
		return billing.JournalTransaction{}, ErrProviderJournalGenericPath
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: begin journal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := prepareCorrection(ctx, tx, &input); err != nil {
		return billing.JournalTransaction{}, err
	}
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), input.AccountID); err != nil {
		return billing.JournalTransaction{}, err
	}
	if err := prepareCorrection(ctx, tx, &input); err != nil {
		return billing.JournalTransaction{}, err
	}
	if existing, found, lookupErr := lookupJournalBySource(ctx, tx, input.AccountID, input.Book, input.SourceKey); lookupErr != nil {
		return billing.JournalTransaction{}, lookupErr
	} else if found {
		fp, fpErr := input.CanonicalFingerprint()
		if fpErr != nil {
			return billing.JournalTransaction{}, fpErr
		}
		if existing.SemanticFingerprint != fp {
			return billing.JournalTransaction{}, ErrIdentityConflict
		}
		return existing, nil
	}
	sealed, err := input.Seal()
	if err != nil {
		return billing.JournalTransaction{}, err
	}
	if input.SemanticFingerprint != "" && input.SemanticFingerprint != sealed.SemanticFingerprint {
		return billing.JournalTransaction{}, billing.ErrJournalFingerprint
	}
	var sequence uint64
	if err := tx.NewRaw(`SELECT COALESCE(MAX(account_sequence), 0) FROM journal_transactions WHERE account_id = ?`, sealed.AccountID).Scan(ctx, &sequence); err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: allocate account sequence: %w", err)
	}
	sequence++
	sealed.AccountSequence = sequence
	// reserved snapshot columns are retained only for immutable historical audit rows;
	// current journal commands have no reserved state and write the proven zero literals.
	_, err = tx.NewRaw(`INSERT INTO journal_transactions( transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint,
		sealed.TurnID, sealed.ALegID, sealed.BLegID, sealed.AccountSequence, sealed.ReversalOf, sealed.CorrectsTransactionID,
		sealed.CorrectionGroupID, sealed.OperationKind, sealed.BalanceBefore, sealed.BalanceAfter, 0, 0,
		sealed.SpendableBefore, sealed.SpendableAfter, sealed.CreditFloor, sealed.CreditLimit,
		sealed.Mode, sealed.SnapshotVersionBefore, sealed.SnapshotVersionAfter).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return billing.JournalTransaction{}, fmt.Errorf("billingstore: journal unique race: %w", err)
		}
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: insert journal transaction: %w", err)
	}
	if err := tx.NewRaw(`SELECT recorded_at FROM journal_transactions WHERE transaction_id = ?`, sealed.ID).Scan(ctx, &sealed.RecordedAt); err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: read journal timestamp: %w", err)
	}
	for ordinal, entry := range sealed.Entries {
		if _, err := tx.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`, sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(ctx); err != nil {
			return billing.JournalTransaction{}, fmt.Errorf("billingstore: insert journal entry: %w", err)
		}
	}
	if _, err := tx.NewRaw(`UPDATE billing_accounts SET version = version + 1, updated_at = ? WHERE account_id = ?`, time.Now().UTC(), sealed.AccountID).Exec(ctx); err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: update account version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: commit journal: %w", err)
	}
	return sealed, nil
}

func lockAccount(ctx context.Context, tx bun.Tx, dialectName dialect.Name, accountID string) error {
	if dialectName == dialect.PG {
		var id string
		if err := tx.NewRaw(`SELECT account_id FROM billing_accounts WHERE account_id = ? FOR UPDATE`, accountID).Scan(ctx, &id); errors.Is(err, sql.ErrNoRows) {
			return ErrAccountNotFound
		} else if err != nil {
			return fmt.Errorf("billingstore: lock account: %w", err)
		}
		return nil
	}
	result, err := tx.NewRaw(`UPDATE billing_accounts SET updated_at = updated_at WHERE account_id = ?`, accountID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: lock account: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return fmt.Errorf("billingstore: lock account: %w", err)
		}
		return ErrAccountNotFound
	}
	return nil
}

func prepareCorrection(ctx context.Context, tx bun.Tx, input *billing.JournalTransaction) error {
	reversalTarget := strings.TrimSpace(input.ReversalOf)
	replacementTarget := strings.TrimSpace(input.CorrectsTransactionID)
	if reversalTarget != "" && replacementTarget != "" && reversalTarget != replacementTarget {
		return fmt.Errorf("%w: reversal and replacement targets differ", ErrCorrectionInvalid)
	}
	targetID := reversalTarget
	if targetID == "" {
		targetID = replacementTarget
	}
	if targetID == "" {
		return nil
	}
	var target journalTransactionRow
	err := tx.NewRaw(`SELECT transaction_id, account_id, book, currency, correction_group_id FROM journal_transactions WHERE transaction_id = ?`, targetID).Scan(ctx, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: target %q does not exist", ErrCorrectionInvalid, targetID)
	}
	if err != nil {
		return fmt.Errorf("%w: lookup target: %v", ErrCorrectionInvalid, err)
	}
	if target.ID == input.ID || target.AccountID != input.AccountID || target.Book != string(input.Book) || target.Currency != input.Currency {
		return fmt.Errorf("%w: target scope mismatch", ErrCorrectionInvalid)
	}
	if reversalTarget != "" {
		var priorSource string
		priorErr := tx.NewRaw(`
SELECT source_key FROM journal_transactions
WHERE account_id = ? AND book = ? AND reversal_of = ?
LIMIT 1
`, input.AccountID, string(input.Book), reversalTarget).Scan(ctx, &priorSource)
		if priorErr == nil && priorSource != input.SourceKey {
			return fmt.Errorf("%w: original %q already has a posted reversal", ErrCorrectionInvalid, reversalTarget)
		}
		if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: lookup prior reversal: %v", ErrCorrectionInvalid, priorErr)
		}
	}
	if replacementTarget != "" {
		if reversalTarget == "" {
			var reversalSource string
			revErr := tx.NewRaw(`
SELECT source_key FROM journal_transactions
WHERE account_id = ? AND book = ? AND reversal_of = ?
LIMIT 1
`, input.AccountID, string(input.Book), replacementTarget).Scan(ctx, &reversalSource)
			if errors.Is(revErr, sql.ErrNoRows) {
				return fmt.Errorf("%w: replacement requires a prior reversal of %q", ErrCorrectionInvalid, replacementTarget)
			}
			if revErr != nil {
				return fmt.Errorf("%w: lookup prior reversal for replacement: %v", ErrCorrectionInvalid, revErr)
			}
			if reversalSource == input.SourceKey {
				return fmt.Errorf("%w: replacement cannot be the same source as the reversal of %q", ErrCorrectionInvalid, replacementTarget)
			}
		}
	}
	if input.CorrectionGroupID == "" {
		input.CorrectionGroupID = target.CorrectionGroupID
		if input.CorrectionGroupID == "" {
			input.CorrectionGroupID = target.ID
		}
	} else if target.CorrectionGroupID != "" && input.CorrectionGroupID != target.CorrectionGroupID {
		return fmt.Errorf("%w: correction group mismatch", ErrCorrectionInvalid)
	}
	return nil
}

func lookupJournalBySource(ctx context.Context, q bun.IDB, accountID string, book billing.JournalBook, sourceKey string) (billing.JournalTransaction, bool, error) {
	var row journalTransactionRow
	err := q.NewRaw(`
SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint,	 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
	 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
	 spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano,
	 mode, snapshot_version_before, snapshot_version_after, recorded_at
FROM journal_transactions WHERE account_id = ? AND book = ? AND source_key = ?
`, accountID, string(book), sourceKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.JournalTransaction{}, false, nil
	}
	if err != nil {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: lookup journal source: %w", err)
	}
	journals, loadErr := loadJournals(ctx, q, []journalTransactionRow{row})
	if loadErr != nil {
		return billing.JournalTransaction{}, false, loadErr
	}
	if len(journals) != 1 {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: lookup journal source: expected one row")
	}
	return journals[0], true, nil
}

func requireJournalBySource(ctx context.Context, q bun.IDB, accountID string, book billing.JournalBook, sourceKey string) (billing.JournalTransaction, error) {
	journal, found, err := lookupJournalBySource(ctx, q, accountID, book, sourceKey)
	if err != nil {
		return billing.JournalTransaction{}, err
	}
	if !found {
		return billing.JournalTransaction{}, fmt.Errorf("%w: missing journal source %q", ErrEvidenceIncomplete, sourceKey)
	}
	return journal, nil
}

func loadJournals(ctx context.Context, q bun.IDB, rows []journalTransactionRow) ([]billing.JournalTransaction, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	args, err := sqlInArgs(ids)
	if err != nil {
		return nil, err
	}
	query := `SELECT transaction_id, ordinal, ledger_account, side, currency, amount_nano FROM journal_entries WHERE transaction_id IN (` + sqlPlaceholders(len(ids)) + `) ORDER BY transaction_id, ordinal`
	var entries []journalEntryRow
	if err := q.NewRaw(query, args...).Scan(ctx, &entries); err != nil {
		return nil, fmt.Errorf("billingstore: load journal entries: %w", err)
	}
	byID := make(map[string][]billing.JournalEntry, len(rows))
	for _, entry := range entries {
		byID[entry.TransactionID] = append(byID[entry.TransactionID], billing.JournalEntry{
			LedgerAccount: entry.LedgerAccount,
			Side:          billing.JournalSide(entry.Side),
			Amount:        billing.Money{Nano: entry.AmountNano, Currency: entry.Currency},
		})
	}
	out := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, journalFromRow(row, byID[row.ID]))
	}
	return out, nil
}

func journalFromRow(row journalTransactionRow, entries []billing.JournalEntry) billing.JournalTransaction {
	sequence := uint64(0)
	if row.AccountSequence.Valid && row.AccountSequence.Int64 > 0 {
		sequence = uint64(row.AccountSequence.Int64)
	}
	return billing.JournalTransaction{
		ID: row.ID, Book: billing.JournalBook(row.Book), Currency: row.Currency, SourceKey: row.SourceKey,
		SemanticFingerprint: row.SemanticFingerprint, AccountID: row.AccountID, TurnID: row.TurnID,
		ALegID: row.ALegID, BLegID: row.BLegID, AccountSequence: sequence,
		ReversalOf: row.ReversalOf, CorrectsTransactionID: row.CorrectsTransactionID,
		CorrectionGroupID: row.CorrectionGroupID, OperationKind: row.OperationKind,
		BalanceBefore: row.BalanceBefore, BalanceAfter: row.BalanceAfter,
		SpendableBefore: row.SpendableBefore, SpendableAfter: row.SpendableAfter,
		CreditFloor: row.CreditFloor, CreditLimit: row.CreditLimit, Mode: row.Mode,
		SnapshotVersionBefore: row.SnapshotVersionBefore, SnapshotVersionAfter: row.SnapshotVersionAfter,
		RecordedAt: row.RecordedAt, Entries: entries,
	}
}

func (s *DurableStore) JournalTransactions(ctx context.Context, accountID string) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	if err := s.db.NewRaw(`
SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint,	 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
	 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
	 spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano,
	 mode, snapshot_version_before, snapshot_version_after, recorded_at
FROM journal_transactions WHERE account_id = ? AND book = 'financial'`+journalOrderClause(""), accountID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, s.db, rows)
}

func isSQLiteBusy(err error) bool {
	text := strings.ToLower(errString(err))
	return strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked") || strings.Contains(text, "busy")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
