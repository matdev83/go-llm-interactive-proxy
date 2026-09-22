package billingstore

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

// Customer settlement B2b1 posting-time ownership fence (store side).
//
// Scope: customer_call_settlement only. Provider/adjustment later. Pin
// completion and journal/balance/unit/exposure/terminal outcome commit in the
// same DB transaction; exact retry returns the existing outcome with a
// completed pin; conflicting replay fails. No public money option/global/
// second writer lives here.

func (s *DurableStore) b2b1Fault(point string) error {
	if s == nil {
		return nil
	}
	if s.settlementFaultHook != nil {
		if err := s.settlementFaultHook(point); err != nil {
			return err
		}
	}
	return nil
}

func b2b1CustomerStateForMissingMarker() billing.AccountingCutoverState {
	return billing.AccountingCutoverV1Active
}

// b2b1CompleteCustomerPinTx marks the customer pin completed with the real
// settlement outcome in the caller's transaction.
func b2b1CompleteCustomerPinTx(ctx context.Context, tx bun.Tx, storeID, operationKey, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix,
		storeID, string(billing.PostingOperationCustomerSettlement), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b1 complete customer pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b1 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: customer pin completion race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}
