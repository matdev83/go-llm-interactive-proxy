package billingstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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
	if err := billing.ValidateProviderCostAuthority(leg, input.Result); err != nil {
		// Validate before opening the monetary transaction: an estimate or local
		// fallback may be retained as advisory work, but it must never create a
		// provider_call_cogs journal, head, or posting fence.
		return billing.Posting{}, err
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
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return billing.Posting{}, err
	}
	if account.Currency != result.Amount.Currency {
		return billing.Posting{}, billing.ErrMoneyCurrencyMismatch
	}
	operationKey, err := billing.ProviderCostSourceKey(leg.Key)
	if err != nil {
		return billing.Posting{}, err
	}
	fingerprint, err := result.SemanticFingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	lineageKey, err := providerCostLineageKey(callID, leg.BLegID)
	if err != nil {
		return billing.Posting{}, err
	}
	executionLineageKey, err := providerCostExecutionLineageKey(callID, leg.BLegID)
	if err != nil {
		return billing.Posting{}, err
	}
	if executionFence, executionFound, lookupErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if executionFound && executionFence.Authority == providerCostFenceAuthorityRevision {
		// A V2 revision owns the whole B-leg execution, including when its
		// current selected amount is unavailable. The legacy aggregate resolver
		// must not make a second monetary decision after that cutover.
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost execution fence replay: %w", err)
		}
		return billing.Posting{OperationKey: executionFence.LastOperationKey, Replayed: true}, nil
	}
	if fence, found, lookupErr := s.loadProviderCostPostingFence(ctx, tx, accountID, callID, lineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
			return billing.Posting{}, executionErr
		} else if !executionFound {
			if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, fence, string(metering.SubjectBLeg)); err != nil {
				return billing.Posting{}, err
			}
		}
		// A revision-authoritative fence supersedes any pending legacy LUR work.
		// The old worker may still be running after a restart, but it must not
		// create a second provider journal or roll a corrected head backward.
		if fence.Authority == providerCostFenceAuthorityRevision {
			if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
				return billing.Posting{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost fence replay: %w", err)
			}
			return billing.Posting{OperationKey: fence.LastOperationKey, Replayed: true}, nil
		}
		if fence.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost replay: %w", err)
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
	}
	if childFence, found, lookupErr := s.loadProviderCostProviderChargeFence(ctx, tx, accountID, callID, executionLineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if childFence.Authority != providerCostFenceAuthorityRevision {
			return billing.Posting{}, fmt.Errorf("%w: provider charge fence authority", billing.ErrProviderCostRevisionFence)
		}
		if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, childFence, string(metering.SubjectProviderCharge)); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider charge fence recovery: %w", err)
		}
		return billing.Posting{OperationKey: childFence.LastOperationKey, Replayed: true}, nil
	}
	// Upgrade/restart recovery: a legacy journal committed before the durable
	// fence migration is imported into the same fence before this attempt can
	// proceed. The import is in this transaction, so it cannot race a revision
	// writer that claims the same lineage.
	if recovered, found, recoveryErr := s.recoverLegacyProviderCostFence(ctx, tx, accountID, callID, lineageKey, account.Currency); recoveryErr != nil {
		return billing.Posting{}, recoveryErr
	} else if found {
		if recovered.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, accountID, callID, lineageKey,
			recovered.Authority, recovered.HeadKey, recovered.EvidenceRevision, recovered.InputSetHash,
			recovered.Fingerprint, providerCostFenceAmount(recovered), recovered.OriginalTransactionID, recovered.LastOperationKey, recovered.LastTransactionID); err != nil {
			return billing.Posting{}, err
		}
		if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
			return billing.Posting{}, executionErr
		} else if !executionFound {
			if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, recovered, string(metering.SubjectBLeg)); err != nil {
				return billing.Posting{}, err
			}
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost legacy recovery: %w", err)
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
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
			CorrectionGroupID: leg.Key,
			BalanceBefore:     before.BalanceNano, BalanceAfter: before.BalanceNano,
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
	if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
		return billing.Posting{}, executionErr
	} else if !executionFound {
		if err := s.insertProviderCostExecutionFenceInTx(ctx, tx, accountID, callID, executionLineageKey,
			providerCostFenceAuthorityLegacy, string(metering.SubjectBLeg), leg.Key, 1,
			legacyProviderCostFenceInputHash(lineageKey), fingerprint, operationKey, posting.Transaction.ID); err != nil {
			return billing.Posting{}, err
		}
	}
	if err := s.insertProviderCostPostingFenceInTx(ctx, tx, accountID, callID, lineageKey,
		providerCostFenceAuthorityLegacy, leg.Key, 1, legacyProviderCostFenceInputHash(lineageKey),
		fingerprint, result.Amount, posting.Transaction.ID, operationKey, posting.Transaction.ID); err != nil {
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
