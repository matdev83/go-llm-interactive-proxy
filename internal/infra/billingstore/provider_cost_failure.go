package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
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
		if err := lockAccount(ctx, tx, s.db.Dialect().Name(), input.AccountID); err != nil {
			return err
		}
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
