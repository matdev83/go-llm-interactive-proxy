package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func (s *DurableStore) MarkProviderCostUnreconciled(ctx context.Context, input billing.ApplyProviderCostInput, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	leg, err := input.Leg.Seal()
	if err != nil {
		return err
	}
	if err := input.CallID.Validate(); err != nil {
		return err
	}
	if leg.CallID != input.CallID || strings.TrimSpace(input.AccountID) == "" {
		return fmt.Errorf("%w: provider-cost failure identity mismatch", billing.ErrSettlementInvalid)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "provider_cost_unavailable"
	}
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 40}, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		lineageKey, err := providerCostLineageKey(input.CallID, leg.BLegID)
		if err != nil {
			return err
		}
		executionLineageKey, err := providerCostExecutionLineageKey(input.CallID, leg.BLegID)
		if err != nil {
			return err
		}
		if fence, found, lookupErr := s.loadProviderCostExecutionFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true); lookupErr != nil {
			return lookupErr
		} else if found && fence.Authority == providerCostFenceAuthorityRevision {
			if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
				return err
			}
			return tx.Commit()
		}
		if fence, found, lookupErr := s.loadProviderCostPostingFence(ctx, tx, input.AccountID, input.CallID, lineageKey, true); lookupErr != nil {
			return lookupErr
		} else if found && fence.Authority == providerCostFenceAuthorityRevision {
			// A revision has already made the authoritative provider selection;
			// an old resolver failure is stale diagnostic work, not an open
			// unreconciled cost. Retire the queue item in the same transaction.
			if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
				return err
			}
			return tx.Commit()
		}
		if childFence, found, lookupErr := s.loadProviderCostProviderChargeFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true); lookupErr != nil {
			return lookupErr
		} else if found {
			if childFence.Authority != providerCostFenceAuthorityRevision {
				return fmt.Errorf("%w: provider charge fence authority", billing.ErrProviderCostRevisionFence)
			}
			if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineageKey, childFence, string(metering.SubjectProviderCharge)); err != nil {
				return err
			}
			if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
				return err
			}
			return tx.Commit()
		}
		// An unreconciled provider marker is diagnostic provider work, not a
		// customer balance mutation. It must not take the customer row lock.
		const kind = "provider_cost_unreconciled"
		fingerprint := "provider-cost-unreconciled:v1:" + reason
		if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, kind, leg.Key); lookupErr != nil {
			return lookupErr
		} else if found {
			if existing.Fingerprint != fingerprint {
				return ErrOperationConflict
			}
			return tx.Commit()
		}
		account, err := getAccountTx(ctx, tx, input.AccountID)
		if err != nil {
			return err
		}
		before, err := snapshotForAccount(account)
		if err != nil {
			return err
		}
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
			OperationKey: "provider-cost-unreconciled:" + leg.Key,
			AccountID:    input.AccountID, OperationKind: kind, SourceKey: leg.Key,
			Fingerprint: fingerprint, Before: before, After: before,
		}); err != nil {
			return err
		}
		return tx.Commit()
	})
}
