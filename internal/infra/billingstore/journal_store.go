package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var (
	ErrIdentityConflict          = errors.New("billingstore: idempotency identity conflict")
	ErrCorrectionInvalid         = errors.New("billingstore: invalid correction link")
	ErrAccountNotFound           = errors.New("billingstore: account not found")
	ErrUsageRecordNotFound       = errors.New("billingstore: usage record not found")
	ErrAuthorizationHoldNotFound = errors.New("billingstore: authorization hold not found")
	ErrProcessingNotFound        = errors.New("billingstore: usage-record processing not found")
	ErrSequenceConflict          = errors.New("billingstore: account sequence conflict")
	ErrEvidenceIncomplete        = errors.New("billingstore: operation evidence incomplete")
)

var (
	_ billing.UsageRecordAppender = (*DurableStore)(nil)
	_ billing.HoldReleaser        = (*DurableStore)(nil)
	_ billing.AccountProvisioner  = (*DurableStore)(nil)
	_ billing.AuthorizationLookup = (*DurableStore)(nil)
)

// CreateAccount creates the initial materialized account row. Subsequent
// financial mutations must be represented by journal commands, not arbitrary
// account updates.
func (s *DurableStore) CreateAccount(ctx context.Context, account billing.Account) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := account.Validate(); err != nil {
		return err
	}
	if account.ReservedNano != 0 {
		return fmt.Errorf("%w: opening reserved exposure must be zero", billing.ErrAccountInvalid)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.NewRaw(`
INSERT INTO billing_accounts(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
`, account.ID, account.Currency, string(account.Mode), account.CreditLimit, account.BalanceNano, account.BalanceNano, account.ReservedNano, account.Version, string(account.State), now, now).Exec(ctx); err != nil {
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
	var row accountRow
	err := s.db.NewRaw(`SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state FROM billing_accounts WHERE account_id = ?`, strings.TrimSpace(accountID)).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return billing.Account{}, err
	}
	return billing.Account{ID: row.ID, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), CreditLimit: row.CreditLimit, BalanceNano: row.Balance, ReservedNano: row.Reserved, Version: row.Version, State: billing.AccountState(row.State)}, nil
}

// AppendUsageRecord persists a sealed TUR and its immutable LUR rows before
// creating mutable pending processing metadata. Same key/fingerprint is a
// no-op; a conflicting replay is rejected without mutation.
func (s *DurableStore) AppendUsageRecord(ctx context.Context, record billing.TurnUsageRecord) error {
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() error {
		return s.appendUsageRecordAttempt(ctx, record)
	})
}

func (s *DurableStore) appendUsageRecordAttempt(ctx context.Context, record billing.TurnUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM turn_usage_records WHERE tur_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.TurnUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing TUR: %w", unmarshalErr)
		}
		if replayErr := billing.CheckReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		// Replay must still ensure the processing queue row exists so a restored
		// TUR without processing metadata cannot silently miss settlement.
		// A pre-existing processing row with a different fingerprint is an
		// integrity error — never leave mismatched metadata in place.
		now := time.Now().UTC()
		if err := ensureProcessingRow(ctx, tx, sealed.Key, sealed.Fingerprint, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit TUR replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup TUR: %w", err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode TUR: %w", err)
	}
	pricing, _ := json.Marshal(sealed.CustomerPricingRef)
	policy, _ := json.Marshal(sealed.ChargePolicyRef)
	sealedAt := time.Now().UTC()
	_, err = tx.NewRaw(`
INSERT INTO turn_usage_records(
 tur_key, fingerprint, schema_version, account_id, turn_id, a_leg_id, authorization_id, session_id,
 started_at, finished_at, outcome, customer_pricing_ref, charge_policy_ref, payload_json, sealed_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, sealed.Key, sealed.Fingerprint, sealed.SchemaVersion, sealed.AccountID, sealed.TurnID, sealed.ALegID, sealed.AuthorizationID, sealed.SessionID,
		sealed.StartedAt, sealed.FinishedAt, string(sealed.Outcome), string(pricing), string(policy), string(payload), sealedAt).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert TUR: %w", err)
	}
	for _, leg := range sealed.Legs {
		legPayload, marshalErr := json.Marshal(leg)
		if marshalErr != nil {
			return fmt.Errorf("billingstore: encode LUR: %w", marshalErr)
		}
		_, err = tx.NewRaw(`
INSERT INTO leg_usage_records(lur_key, tur_key, fingerprint, a_leg_id, b_leg_id, sequence, backend_id, provider_id, model_id, outcome, surfaced, payload_json, sealed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
`, leg.Key, sealed.Key, leg.Fingerprint, leg.ALegID, leg.BLegID, leg.Seq, leg.BackendID, leg.ProviderID, leg.ModelID, string(leg.Outcome), string(leg.Surfaced), string(legPayload), sealedAt).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: insert LUR: %w", err)
		}
	}
	if err := ensureProcessingRow(ctx, tx, sealed.Key, sealed.Fingerprint, sealedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit TUR: %w", err)
	}
	return nil
}

// ensureProcessingRow inserts pending metadata for a sealed TUR, or verifies an
// existing row matches the sealed fingerprint. Conflicting fingerprints fail
// closed without mutating either table.
func ensureProcessingRow(ctx context.Context, tx bun.Tx, turKey, fingerprint string, now time.Time) error {
	var existingFP string
	err := tx.NewRaw(`SELECT tur_fingerprint FROM usage_record_processing WHERE tur_key = ?`, turKey).Scan(ctx, &existingFP)
	if err == nil {
		if existingFP != fingerprint {
			return billing.ErrProcessingConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup processing for TUR %s: %w", turKey, err)
	}
	if _, err := tx.NewRaw(`INSERT INTO usage_record_processing(tur_key, tur_fingerprint, status, retry_count, updated_at) VALUES (?,?,?,0,?)`, turKey, fingerprint, "pending", now).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert processing state: %w", err)
	}
	return nil
}

// postJournalTransaction is the internal balanced-journal primitive. Trusted
// command methods are the only production-facing write APIs; callers cannot
// supply or override replay order through an arbitrary posting port.
func (s *DurableStore) postJournalTransaction(ctx context.Context, input billing.JournalTransaction) (billing.JournalTransaction, error) {
	if s == nil || s.db == nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: nil store")
	}
	if err := input.Validate(); err != nil {
		return billing.JournalTransaction{}, err
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() (billing.JournalTransaction, error) {
		return s.postJournalAttempt(ctx, input)
	})
}

func (s *DurableStore) postJournalAttempt(ctx context.Context, input billing.JournalTransaction) (billing.JournalTransaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: begin journal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Validate correction target scope before locking the submitting account so
	// a correction against an existing transaction in another account reports
	// the stable integrity error rather than masking it as account-not-found.
	// The same validation is repeated under the account lock below; that locked
	// pass is authoritative for concurrent correction/replay races.
	if err := prepareCorrection(ctx, tx, &input); err != nil {
		return billing.JournalTransaction{}, err
	}
	// Lock before source-key lookup and sequence allocation so concurrent
	// replays serialize into fingerprint compare-before-no-op and concurrent
	// distinct reversals serialize into prepareCorrection rather than racing
	// inserts.
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
	recordedAt := time.Now().UTC()
	_, err = tx.NewRaw(`
INSERT INTO journal_transactions(
 transaction_id, account_id, book, currency, source_key, semantic_fingerprint,	 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
	 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
	 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
	 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at

) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint,
		sealed.TurnID, sealed.ALegID, sealed.BLegID, sealed.AccountSequence, sealed.ReversalOf, sealed.CorrectsTransactionID,
		sealed.CorrectionGroupID, sealed.OperationKind, sealed.BalanceBefore, sealed.BalanceAfter, sealed.ReservedBefore,
		sealed.ReservedAfter, sealed.SpendableBefore, sealed.SpendableAfter, sealed.CreditFloor, sealed.CreditLimit,
		sealed.Mode, sealed.SnapshotVersionBefore, sealed.SnapshotVersionAfter, recordedAt).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			// The transaction is discarded by the deferred rollback. The outer
			// retry starts a fresh transaction so a concurrent source-key race
			// can resolve to same-fingerprint replay rather than a false failure.
			return billing.JournalTransaction{}, fmt.Errorf("billingstore: journal unique race: %w", err)
		}
		return billing.JournalTransaction{}, fmt.Errorf("billingstore: insert journal transaction: %w", err)
	}
	for ordinal, entry := range sealed.Entries {
		if _, err := tx.NewRaw(`
INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano)
VALUES (?,?,?,?,?,?)
`, sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(ctx); err != nil {
			return billing.JournalTransaction{}, fmt.Errorf("billingstore: insert journal entry: %w", err)
		}
	}
	if _, err := tx.NewRaw(`UPDATE billing_accounts SET version = version + 1, updated_at = ? WHERE account_id = ?`, recordedAt, sealed.AccountID).Exec(ctx); err != nil {
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

// lockProcessingForSettlement serializes settlement against claim/reclaim on the
// same TUR so a foreign lease cannot be installed mid-settlement transaction.
func lockProcessingForSettlement(ctx context.Context, tx bun.Tx, dialectName dialect.Name, turKey string) error {
	if dialectName == dialect.PG {
		var key string
		err := tx.NewRaw(`SELECT tur_key FROM usage_record_processing WHERE tur_key = ? FOR UPDATE`, turKey).Scan(ctx, &key)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProcessingNotFound
		}
		if err != nil {
			return fmt.Errorf("billingstore: lock processing: %w", err)
		}
		return nil
	}
	result, err := tx.NewRaw(`UPDATE usage_record_processing SET updated_at = updated_at WHERE tur_key = ?`, turKey).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: lock processing: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return fmt.Errorf("billingstore: lock processing: %w", err)
		}
		return ErrProcessingNotFound
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
		// Reject a second distinct reversal of the same original. Same-SourceKey
		// replay of an existing reversal is handled by lookupJournalBySource
		// (fingerprint compare-before-no-op). Later replacements may still point
		// at the original or prior correction within the shared correction group.
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
		// Req 7.4: corrections use reversal plus replacement. A replacement may
		// only post after a distinct reversal of the same target already exists
		// (or this transaction itself is that reversal via ReversalOf).
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
	 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
	 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at

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

func loadJournal(ctx context.Context, q bun.IDB, row journalTransactionRow) (billing.JournalTransaction, bool, error) {
	journals, err := loadJournals(ctx, q, []journalTransactionRow{row})
	if err != nil {
		return billing.JournalTransaction{}, false, err
	}
	if len(journals) != 1 {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: load journal: expected one row")
	}
	return journals[0], true, nil
}

// loadJournals hydrates journal headers with a single batched entry fetch.
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
	return billing.JournalTransaction{
		ID: row.ID, Book: billing.JournalBook(row.Book), Currency: row.Currency, SourceKey: row.SourceKey,
		SemanticFingerprint: row.SemanticFingerprint, AccountID: row.AccountID, TurnID: row.TurnID,
		ALegID: row.ALegID, BLegID: row.BLegID, AccountSequence: row.AccountSequence,
		ReversalOf: row.ReversalOf, CorrectsTransactionID: row.CorrectsTransactionID,
		CorrectionGroupID: row.CorrectionGroupID, OperationKind: row.OperationKind,
		BalanceBefore: row.BalanceBefore, BalanceAfter: row.BalanceAfter,
		ReservedBefore: row.ReservedBefore, ReservedAfter: row.ReservedAfter,
		SpendableBefore: row.SpendableBefore, SpendableAfter: row.SpendableAfter,
		CreditFloor: row.CreditFloor, CreditLimit: row.CreditLimit, Mode: row.Mode,
		SnapshotVersionBefore: row.SnapshotVersionBefore, SnapshotVersionAfter: row.SnapshotVersionAfter,
		Entries: entries,
	}
}

// JournalTransactions returns immutable copies in deterministic account order.
func (s *DurableStore) JournalTransactions(ctx context.Context, accountID string) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	if err := s.db.NewRaw(`
SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint,	 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
	 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
	 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
	 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at

FROM journal_transactions WHERE account_id = ? ORDER BY account_sequence`, accountID).Scan(ctx, &rows); err != nil {
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
