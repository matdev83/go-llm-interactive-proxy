package billingstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Financial adjustment B2b3 posting-time ownership fence (store side).
//
// Scope: financial_adjustment only (selected-cost corrections via
// ApplySelectedCostAdjustment). Customer/provider already fenced B2b1/B2b2.
// Pin completion and adjustment journal/head/linkage effects commit in the
// same DB transaction; exact retry returns the existing outcome with a
// completed pin; conflicting replay fails. Replacement revisions share one
// head pin authority: each revision keeps its own canonical operation/link
// identity while the pin completion advances to the latest outcome under the
// same owner. Exact NoOp exclusions also complete the pin. No public money
// option/global/second writer lives here.

func (s *DurableStore) b2b3Fault(point string) error {
	if s == nil {
		return nil
	}
	if s.adjustmentFaultHook != nil {
		if err := s.adjustmentFaultHook(point); err != nil {
			return err
		}
	}
	return nil
}

// SetAdjustmentFaultHook installs the B2b3 crash hook. Nil clears it.
func (s *DurableStore) SetAdjustmentFaultHook(hook func(string) error) {
	if s != nil {
		s.adjustmentFaultHook = hook
	}
}

func b2b3AdjustmentStateForMissingMarker() billing.AccountingCutoverState {
	return billing.AccountingCutoverV1Active
}

func adjustmentSubjectJSON(subject metering.SubjectRef) (string, error) {
	payload, err := json.Marshal(subject)
	if err != nil {
		return "", fmt.Errorf("billingstore: b2b3 encode adjustment subject: %w", err)
	}
	return string(payload), nil
}

// b2b3InsertAdjustmentPinTx acquires a fresh V1/V2 head pin in the caller's
// transaction. Callers hold the marker snapshot and have already enforced the
// state machine; this helper only performs the INSERT ... OR IGNORE plus
// authoritative reload with owner convergence.
func b2b3InsertAdjustmentPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, headKey, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, nowUnix int64) (billing.PostingPin, error) {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	subjectJSON, err := adjustmentSubjectJSON(subject)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, headKey, string(subject.Kind), subjectJSON, owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: b2b3 acquire adjustment pin: %w", err)
	}
	rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, operationKey)
	if rerr != nil {
		return billing.PostingPin{}, rerr
	}
	if !refound {
		return billing.PostingPin{}, fmt.Errorf("%w: adjustment pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	repin, err := postingOwnershipRowToPin(rerow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if repin.Owner != owner {
		return billing.PostingPin{}, fmt.Errorf("%w: adjustment pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repin.Owner, owner)
	}
	return repin, nil
}

// b2b3BackfillAdjustmentPinTx inserts a completed V1/V2 pin for pre-B2b3 money
// (crash-safe: same tx, no new money, just ownership completion).
func b2b3BackfillAdjustmentPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, headKey, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, completionOpKey, completionTxID string, nowUnix int64) error {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	subjectJSON, err := adjustmentSubjectJSON(subject)
	if err != nil {
		return err
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, headKey, string(subject.Kind), subjectJSON, owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: b2b3 backfill adjustment pin: %w", err)
	}
	return nil
}

// b2b3CompleteAdjustmentPinTx marks the head pin completed with the real
// adjustment outcome in the caller's transaction.
func b2b3CompleteAdjustmentPinTx(ctx context.Context, tx bun.Tx, storeID, operationKey, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix,
		storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b3 complete adjustment pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b3 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: adjustment pin completion race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

// b2b3UpdateAdjustmentPinCompletionTx refreshes a completed head pin's outcome
// to the latest revision effect under the same owner authority (replacement
// revisions retain one authority). It fails closed on owner mismatch.
func b2b3UpdateAdjustmentPinCompletionTx(ctx context.Context, tx bun.Tx, storeID, operationKey, owner, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ? AND owner = ?`,
		completionOpKey, completionTxID, nowUnix,
		storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, string(billing.PostingPinCompleted), owner).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b3 update adjustment pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b3 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: adjustment pin update race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}
