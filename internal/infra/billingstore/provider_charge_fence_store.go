package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Provider charge B2b2 posting-time ownership fence (store side).
//
// Scope: provider_charge only. Pin completion and provider journal/head/
// exclusion/fence/work effects commit in the same DB transaction; exact retry
// returns the existing outcome with a completed pin; conflicting replay fails.
// No public money option/global/second writer lives here.

func (s *DurableStore) b2b2Fault(point string) error {
	if s == nil {
		return nil
	}
	if s.providerFaultHook != nil {
		if err := s.providerFaultHook(point); err != nil {
			return err
		}
	}
	return nil
}

func b2b2ProviderStateForMissingMarker() billing.AccountingCutoverState {
	return billing.AccountingCutoverV1Active
}

func nowUnixNano() int64 {
	return time.Now().UTC().UnixNano()
}

// legSubjectJSONForPin builds the canonical B-leg subject JSON for a legacy
// provider pin. The pin key itself (ProviderCostSourceKey of the leg key) is
// store-independent; the subject records the owning store lineage.
func legSubjectJSONForPin(storeID, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord) (string, error) {
	subject := metering.SubjectRef{
		Kind:          metering.SubjectBLeg,
		StoreID:       storeID,
		AccountID:     accountID,
		ALegID:        leg.ALegID,
		BillingCallID: callID.String(),
		BLegID:        leg.BLegID,
	}
	payload, err := json.Marshal(subject)
	if err != nil {
		return "", fmt.Errorf("billingstore: b2b2 encode provider pin subject: %w", err)
	}
	return string(payload), nil
}

// b2b2BackfillProviderPinTx inserts a completed V1/V2 pin for pre-B2b2 money
// (crash-safe: same tx, no new money, just ownership completion).
func b2b2BackfillProviderPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, completionOpKey, completionTxID string, nowUnix int64) error {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	subjectJSON, _ := legSubjectJSONForPin(s.storeID, accountID, callID, leg)
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), operationKey, accountID, callID.String(), leg.BLegID, "", "", string("b_leg"), subjectJSON, owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: b2b2 backfill provider pin: %w", err)
	}
	return nil
}

// b2b2CompleteProviderPinTx marks the provider pin completed with the real
// posting outcome in the caller's transaction.
func b2b2CompleteProviderPinTx(ctx context.Context, tx bun.Tx, storeID, operationKey, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix,
		storeID, string(billing.PostingOperationProviderCharge), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b2 complete provider pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b2 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: provider pin completion race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

// b2b2UpdateProviderPinCompletionTx refreshes a completed pin's outcome to the
// latest revision effect under the same owner authority (replacement
// revisions retain one authority). It fails closed on owner mismatch.
func b2b2UpdateProviderPinCompletionTx(ctx context.Context, tx bun.Tx, storeID, operationKey, owner, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ? AND owner = ?`,
		completionOpKey, completionTxID, nowUnix,
		storeID, string(billing.PostingOperationProviderCharge), operationKey, string(billing.PostingPinCompleted), owner).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b2 update provider pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b2 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: provider pin update race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

// b2b2InsertProviderPinTx acquires a fresh V1/V2 pin in the caller's
// transaction. Callers hold the marker snapshot and have already enforced the
// state machine; this helper only performs the INSERT ... OR IGNORE plus
// authoritative reload with owner convergence.
func b2b2InsertProviderPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, subjectStoreID, bLegID, providerChargeID, subjectKind, subjectJSON string, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, nowUnix int64) (billing.PostingPin, error) {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	_ = subjectStoreID
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), operationKey, accountID, callID.String(), bLegID, providerChargeID, "", subjectKind, subjectJSON, owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: b2b2 acquire provider pin: %w", err)
	}
	rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
	if rerr != nil {
		return billing.PostingPin{}, rerr
	}
	if !refound {
		return billing.PostingPin{}, fmt.Errorf("%w: provider pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	repin, err := postingOwnershipRowToPin(rerow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if repin.Owner != owner {
		return billing.PostingPin{}, fmt.Errorf("%w: provider pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repin.Owner, owner)
	}
	return repin, nil
}
