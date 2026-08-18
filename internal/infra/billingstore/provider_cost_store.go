package billingstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

var _ billing.ProviderCostStore = (*DurableStore)(nil)

func (s *DurableStore) ApplyProviderCost(ctx context.Context, input billing.ApplyProviderCostInput) (billing.Posting, error) {
	if s == nil || s.db == nil {
		return billing.Posting{}, fmt.Errorf("billingstore: nil store")
	}
	leg, err := input.Leg.Seal()
	if err != nil {
		return billing.Posting{}, err
	}
	if err := input.CallID.Validate(); err != nil {
		return billing.Posting{}, err
	}
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || leg.CallID != input.CallID || input.Result.LURKey != leg.Key {
		return billing.Posting{}, fmt.Errorf("%w: provider-cost identity mismatch", billing.ErrSettlementInvalid)
	}
	if input.Result.Amount.Nano < 0 || strings.TrimSpace(input.Result.Amount.Currency) == "" || !input.Result.AmountPresent || !input.Result.Reconciled {
		return billing.Posting{}, fmt.Errorf("%w: provider cost must be reconciled and present", billing.ErrUnreconciledCost)
	}
	if input.Result.Amount.Currency != input.Leg.Evidence.Cost.Currency && input.Leg.Evidence.Cost.Present && input.Leg.Evidence.Cost.Currency != "" {
		return billing.Posting{}, billing.ErrRatingCurrencyMismatch
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40}, func() (billing.Posting, error) {
		return s.applyProviderCostAttempt(ctx, accountID, input.CallID, leg, input.Result)
	})
}

func markProviderCostWorkProcessed(ctx context.Context, tx bun.Tx, legKey string) error {
	result, err := tx.NewRaw(`UPDATE provider_cost_work SET status = 'processed', updated_at = ? WHERE usage_leg_key = ? AND status = 'pending'`, time.Now().UTC(), legKey).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: mark provider cost work processed: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: provider cost work rows affected: %w", err)
	} else if count > 1 {
		return fmt.Errorf("billingstore: provider cost work updated %d rows", count)
	}
	return nil
}

func (s *DurableStore) applyProviderCostAttempt(ctx context.Context, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, result billing.OperatorCostResult) (billing.Posting, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: begin provider cost: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Provider COGS is not a customer balance mutation. In particular, do not
	// lock billing_accounts here: customer exposure admission must remain
	// independent while provider work is slow or backlogged.
	operationKey, err := billing.ProviderCostSourceKey(leg.Key)
	if err != nil {
		return billing.Posting{}, err
	}
	fingerprint, err := result.SemanticFingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, accountID, "provider_call_cogs", leg.Key); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if existing.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, err
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
	}
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return billing.Posting{}, err
	}
	if account.Currency != result.Amount.Currency {
		return billing.Posting{}, billing.ErrMoneyCurrencyMismatch
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.Posting{}, err
	}
	posting := billing.Posting{OperationKey: operationKey, Before: before, After: before}
	if result.Amount.Nano > 0 {
		journal := billing.JournalTransaction{
			ID: operationKey, Book: billing.JournalBookFinancial, Currency: result.Amount.Currency,
			SourceKey: operationKey, AccountID: accountID, TurnID: callID.String(),
			ALegID: leg.ALegID, BLegID: leg.BLegID, OperationKind: "provider_call_cogs",
			BalanceBefore: before.BalanceNano, BalanceAfter: before.BalanceNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: before.SpendableNano,
			CreditFloor: before.CreditFloorNano, CreditLimit: before.CreditLimitNano,
			Mode: string(before.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: before.Version,
			Entries: []billing.JournalEntry{
				{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: result.Amount},
				{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: result.Amount},
			},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil {
			return billing.Posting{}, postErr
		}
		if replayed {
			return billing.Posting{}, ErrOperationConflict
		}
		posting.Transaction = posted
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: operationKey, AccountID: accountID, OperationKind: "provider_call_cogs",
		SourceKey: leg.Key, Fingerprint: fingerprint, Before: before, After: before,
		SequenceStart: posting.Transaction.AccountSequence, SequenceEnd: posting.Transaction.AccountSequence,
	}); err != nil {
		return billing.Posting{}, err
	}
	if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
		return billing.Posting{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost: %w", err)
	}
	return posting, nil
}
