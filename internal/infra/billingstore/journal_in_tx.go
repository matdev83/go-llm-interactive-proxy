package billingstore

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

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
	if input.OperationKind != "provider_call_cogs" {
		if err := tx.NewRaw(`SELECT COALESCE(MAX(account_sequence), 0) FROM journal_transactions WHERE account_id = ?`, sealed.AccountID).Scan(ctx, &sequence); err != nil {
			return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: allocate trusted sequence: %w", err)
		}
		sealed.AccountSequence = sequence + 1
	}
	var accountSequence any
	if input.OperationKind == "provider_call_cogs" {
		accountSequence = nil
	} else {
		accountSequence = sealed.AccountSequence
	}
	// These legacy snapshot columns remain decode-only audit storage; current
	// journal commands never carry reserved state and write zero literals.
	_, err = tx.NewRaw(`INSERT INTO journal_transactions(transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint, sealed.TurnID, sealed.ALegID, sealed.BLegID, accountSequence, sealed.ReversalOf, sealed.CorrectsTransactionID, sealed.CorrectionGroupID, sealed.OperationKind, sealed.BalanceBefore, sealed.BalanceAfter, 0, 0, sealed.SpendableBefore, sealed.SpendableAfter, sealed.CreditFloor, sealed.CreditLimit, sealed.Mode, sealed.SnapshotVersionBefore, sealed.SnapshotVersionAfter).Exec(ctx)
	if err != nil {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: insert trusted journal: %w", err)
	}
	if err := tx.NewRaw(`SELECT recorded_at FROM journal_transactions WHERE transaction_id = ?`, sealed.ID).Scan(ctx, &sealed.RecordedAt); err != nil {
		return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: read trusted journal timestamp: %w", err)
	}
	for ordinal, entry := range sealed.Entries {
		if _, err := tx.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`, sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(ctx); err != nil {
			return billing.JournalTransaction{}, false, fmt.Errorf("billingstore: insert trusted journal entry: %w", err)
		}
	}
	return sealed, false, nil
}
