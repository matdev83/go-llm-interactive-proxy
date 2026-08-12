package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

// postJournalInTx appends a sealed journal transaction inside a caller-owned
// account transaction. The caller owns account locking and materialized-state
// mutation; this helper only handles semantic replay, sequence allocation, and
// immutable journal rows.
func (s *DurableStore) postJournalInTx(ctx context.Context, tx bun.Tx, input billing.JournalTransaction) (billing.JournalTransaction, bool, error) {
	if err := prepareCorrection(ctx, tx, &input); err != nil {
		return billing.JournalTransaction{}, false, err
	}
	if existing, found, err := lookupJournalBySource(ctx, tx, input.AccountID, input.Book, input.SourceKey); err != nil {
		return billing.JournalTransaction{}, false, err
	} else if found {
		fp, fpErr := input.CanonicalFingerprint()
		if fpErr != nil {
			return billing.JournalTransaction{}, false, fpErr
		}
		if existing.SemanticFingerprint != fp {
			return billing.JournalTransaction{}, false, ErrIdentityConflict
		}
		return existing, true, nil
	}
	sealed, err := input.Seal()
	if err != nil {
		return billing.JournalTransaction{}, false, err
	}
	if input.SemanticFingerprint != "" && input.SemanticFingerprint != sealed.SemanticFingerprint {
		return billing.JournalTransaction{}, false, billing.ErrJournalFingerprint
	}
	var sequence uint64
	if err := tx.NewRaw(`SELECT COALESCE(MAX(account_sequence), 0) FROM journal_transactions WHERE account_id = ?`, sealed.AccountID).Scan(ctx, &sequence); err != nil {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: allocate trusted sequence: %w", err)
	}
	sealed.AccountSequence = sequence + 1
	now := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO journal_transactions(transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint, sealed.TurnID, sealed.ALegID, sealed.BLegID, sealed.AccountSequence, sealed.ReversalOf, sealed.CorrectsTransactionID, sealed.CorrectionGroupID, sealed.OperationKind, sealed.BalanceBefore, sealed.BalanceAfter, sealed.ReservedBefore, sealed.ReservedAfter, sealed.SpendableBefore, sealed.SpendableAfter, sealed.CreditFloor, sealed.CreditLimit, sealed.Mode, sealed.SnapshotVersionBefore, sealed.SnapshotVersionAfter, now).Exec(ctx)
	if err != nil {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: insert trusted journal: %w", err)
	}
	for ordinal, entry := range sealed.Entries {
		if _, err := tx.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`, sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(ctx); err != nil {
			return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: insert trusted journal entry: %w", err)
		}
	}
	return sealed, false, nil
}

func loadJournalForReplay(ctx context.Context, tx bun.Tx, accountID string, book billing.JournalBook, sourceKey string) (billing.JournalTransaction, error) {
	journal, found, err := lookupJournalBySource(ctx, tx, accountID, book, sourceKey)
	if err != nil {
		return billing.JournalTransaction{}, err
	}
	if !found {
		return billing.JournalTransaction{}, sql.ErrNoRows
	}
	return journal, nil
}
