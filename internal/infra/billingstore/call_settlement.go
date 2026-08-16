package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

var _ billing.CallSettlementStore = (*DurableStore)(nil)

func (s *DurableStore) ApplyCallBillingResult(ctx context.Context, input billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	if s == nil || s.db == nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: nil store")
	}
	call, err := input.Call.Seal()
	if err != nil {
		return billing.CallSettlement{}, err
	}
	if input.Result.CallID != call.CallID || input.Exposure.CallID != call.CallID.String() || input.Exposure.AccountID != call.AccountID {
		return billing.CallSettlement{}, fmt.Errorf("%w: call/exposure identity mismatch", billing.ErrSettlementInvalid)
	}
	if input.Result.CustomerCharge.Nano < 0 || input.Result.CustomerCharge.Currency == "" {
		return billing.CallSettlement{}, billing.ErrSettlementInvalid
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.CallSettlement, error) {
		return s.applyCallBillingAttempt(ctx, call, input)
	})
}

func (s *DurableStore) applyCallBillingAttempt(ctx context.Context, call billing.CallUsageRecord, input billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	expected := input.Exposure
	result := input.Result
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: begin call settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), call.AccountID); err != nil {
		return billing.CallSettlement{}, err
	}
	operationKind := input.OperationKind
	if operationKind == "" {
		operationKind = "customer_call_settlement"
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(call.AccountID, call.CallID)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, call.AccountID, operationKind, call.CallID.String()); lookupErr != nil {
		return billing.CallSettlement{}, lookupErr
	} else if found {
		if existing.Fingerprint != result.Fingerprint {
			return billing.CallSettlement{}, ErrOperationConflict
		}
		if err := tx.Commit(); err != nil {
			return billing.CallSettlement{}, err
		}
		return billing.CallSettlement{CallID: call.CallID, Replayed: true}, nil
	}
	var row exposureRow
	if err := tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND call_id = ?`, call.AccountID, call.CallID.String()).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallSettlement{}, billing.ErrExposureNotFound
		}
		return billing.CallSettlement{}, err
	}
	exposure, err := exposureFromRow(row)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	if exposure.Fingerprint != expected.Fingerprint || !exposure.IsOpen() {
		return billing.CallSettlement{}, billing.ErrSettlementConflict
	}
	account, err := getAccountTx(ctx, tx, call.AccountID)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	if account.State != billing.AccountReady {
		return billing.CallSettlement{}, billing.ErrAccountNotReady
	}
	if account.Currency != result.CustomerCharge.Currency || account.Currency != exposure.Max.Currency {
		return billing.CallSettlement{}, billing.ErrMoneyCurrencyMismatch
	}
	if result.CustomerCharge.Nano > exposure.Max.Nano {
		if err := setReconcileRequiredTx(ctx, tx, call.AccountID); err != nil {
			return billing.CallSettlement{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.CallSettlement{}, err
		}
		return billing.CallSettlement{}, fmt.Errorf("%w: %w", billing.ErrSettlementReconcileRequired, billing.ErrExposureActualExceedsMax)
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	after := account
	if result.CustomerCharge.Nano > 0 {
		after, err = account.ApplyBalanceDelta(billing.Money{Nano: -result.CustomerCharge.Nano, Currency: account.Currency})
		if err != nil {
			if errors.Is(err, billing.ErrInsufficientSpendable) {
				if markErr := setReconcileRequiredTx(ctx, tx, call.AccountID); markErr != nil {
					return billing.CallSettlement{}, markErr
				}
				if commitErr := tx.Commit(); commitErr != nil {
					return billing.CallSettlement{}, commitErr
				}
				return billing.CallSettlement{}, fmt.Errorf("%w: %w", billing.ErrSettlementReconcileRequired, err)
			}
			return billing.CallSettlement{}, err
		}
		if account.Version == ^uint64(0) {
			return billing.CallSettlement{}, billing.ErrSettlementInvalid
		}
		after.Version = account.Version + 1
	}
	afterSnapshot, err := snapshotForAccount(after)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	var posting billing.Posting
	if result.CustomerCharge.Nano > 0 {
		journal := billing.JournalTransaction{
			ID: sourceKey, Book: billing.JournalBookFinancial, Currency: account.Currency, SourceKey: sourceKey,
			AccountID: call.AccountID, TurnID: call.CallID.String(), ALegID: call.ALegID, OperationKind: operationKind,
			BalanceBefore: before.BalanceNano, BalanceAfter: afterSnapshot.BalanceNano, ReservedBefore: before.ReservedNano, ReservedAfter: afterSnapshot.ReservedNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: afterSnapshot.SpendableNano, CreditFloor: afterSnapshot.CreditFloorNano, CreditLimit: afterSnapshot.CreditLimitNano,
			Mode: string(afterSnapshot.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: afterSnapshot.Version,
			Entries: []billing.JournalEntry{{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: result.CustomerCharge}, {LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: result.CustomerCharge}},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil || replayed {
			if postErr != nil {
				return billing.CallSettlement{}, postErr
			}
			return billing.CallSettlement{}, ErrOperationConflict
		}
		posting = billing.Posting{OperationKey: sourceKey, Transaction: posted, Before: before, After: afterSnapshot}
	} else {
		posting = billing.Posting{OperationKey: sourceKey, Before: before, After: afterSnapshot}
	}
	now := time.Now().UTC()
	if _, err := tx.NewRaw(`UPDATE call_exposures SET status = 'closed', closed_at = ? WHERE account_id = ? AND call_id = ? AND status = 'open'`, now, call.AccountID, call.CallID.String()).Exec(ctx); err != nil {
		return billing.CallSettlement{}, err
	}
	if result.CustomerCharge.Nano > 0 {
		accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET balance_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, afterSnapshot.BalanceNano, afterSnapshot.Version, now, call.AccountID, before.Version).Exec(ctx)
		if err != nil {
			return billing.CallSettlement{}, err
		}
		if count, err := accountResult.RowsAffected(); err != nil || count != 1 {
			if err != nil {
				return billing.CallSettlement{}, err
			}
			return billing.CallSettlement{}, billing.ErrSettlementConflict
		}
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: sourceKey + ":" + operationKind, AccountID: call.AccountID, OperationKind: operationKind, SourceKey: call.CallID.String(), Fingerprint: result.Fingerprint, Before: before, After: afterSnapshot}); err != nil {
		return billing.CallSettlement{}, err
	}
	if _, err := tx.NewRaw(`UPDATE usage_call_records SET claim_status = 'processed' WHERE call_id = ? AND claim_status IN ('pending','claimed','reconcile_required')`, call.CallID.String()).Exec(ctx); err != nil {
		return billing.CallSettlement{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.CallSettlement{}, err
	}
	return billing.CallSettlement{CallID: call.CallID, Customer: posting}, nil
}
