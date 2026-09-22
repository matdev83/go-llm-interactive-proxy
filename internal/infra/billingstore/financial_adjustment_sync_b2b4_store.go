package billingstore

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Financial adjustment B2b4 posting-time ownership fence for synchronous
// writers (store side).
//
// Scope: financial_adjustment only, remaining synchronous monetary adjustment
// writers not covered by B2b3:
//   - cost pass-through revisions (ApplyCostPassThroughRevision/Adjustment):
//     per-head pin CostPassThroughFinancialAdjustmentPostingOperationKey
//     (store/account/call/head), one authority for the replacement chain.
//   - direct adjustments (PostAdjustment): per-source pin
//     DirectFinancialAdjustmentPostingOperationKey(store/account/source),
//     head_key carries the caller source so the hash stays recomputable
//     without fake call/head/B-leg lineage.
//
// Pin completion and balance/journal/head effects commit in the same DB
// transaction; exact retry backfills/completes the same owner pin with zero
// new money; conflicting owner/epoch/amount/currency/source fails before
// effects. Funding/payment/policy and unit ledger are not cutover adjustment
// writers (see inventory guard disposition) and keep existing behavior.
//
// Fault points reuse the B2b3 adjustment hook with b2b4- prefix:
// b2b4-enter, b2b4-pin-acquire, b2b4-before-effects, b2b4-before-journal,
// b2b4-before-head, b2b4-before-pin-complete, b2b4-before-commit,
// b2b4-replay-pin.

func (s *DurableStore) b2b4Fault(point string) error {
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

func b2b4CostPassStateForMissingMarker() billing.AccountingCutoverState {
	return billing.AccountingCutoverV1Active
}

func b2b4InsertCostPassThroughPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, nowUnix int64) (billing.PostingPin, error) {
	headKey := billing.CostPassThroughHeadKey(accountID, callID)
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, callID.String(), "", "", headKey, "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: b2b4 acquire cost pass-through pin: %w", err)
	}
	rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, operationKey)
	if rerr != nil {
		return billing.PostingPin{}, rerr
	}
	if !refound {
		return billing.PostingPin{}, fmt.Errorf("%w: cost pass-through pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	repin, err := postingOwnershipRowToPin(rerow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if repin.Owner != owner {
		return billing.PostingPin{}, fmt.Errorf("%w: cost pass-through pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repin.Owner, owner)
	}
	return repin, nil
}

func b2b4BackfillCostPassThroughPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID string, callID billing.BillingCallID, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, completionOpKey, completionTxID string, nowUnix int64) error {
	headKey := billing.CostPassThroughHeadKey(accountID, callID)
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, callID.String(), "", "", headKey, "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: b2b4 backfill cost pass-through pin: %w", err)
	}
	return nil
}

func b2b4CompleteCostPassThroughPinTx(ctx context.Context, tx bun.Tx, storeID, operationKey, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix,
		storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 complete cost pass-through pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: cost pass-through pin completion race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

func b2b4UpdateCostPassThroughPinCompletionTx(ctx context.Context, tx bun.Tx, storeID, operationKey, owner, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ? AND owner = ?`,
		completionOpKey, completionTxID, nowUnix,
		storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, string(billing.PostingPinCompleted), owner).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 update cost pass-through pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: cost pass-through pin update race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

func b2b4InsertDirectPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID, sourceKey, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, nowUnix int64) (billing.PostingPin, error) {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	// Direct pins carry no call lineage (empty call_id) and reuse head_key
	// for the caller source so the hash stays recomputable.
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, "", "", "", sourceKey, "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: b2b4 acquire direct pin: %w", err)
	}
	rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, operationKey)
	if rerr != nil {
		return billing.PostingPin{}, rerr
	}
	if !refound {
		return billing.PostingPin{}, fmt.Errorf("%w: direct pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	repin, err := postingOwnershipRowToPin(rerow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if repin.Owner != owner {
		return billing.PostingPin{}, fmt.Errorf("%w: direct pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repin.Owner, owner)
	}
	return repin, nil
}

func b2b4BackfillDirectPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, accountID, sourceKey, operationKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, completionOpKey, completionTxID string, nowUnix int64) error {
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, accountID, "", "", "", sourceKey, "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: b2b4 backfill direct pin: %w", err)
	}
	return nil
}

func b2b4CompleteDirectPinTx(ctx context.Context, tx bun.Tx, storeID, operationKey, completionOpKey, completionTxID string, nowUnix int64) error {
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix,
		storeID, string(billing.PostingOperationFinancialAdjustment), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 complete direct pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: b2b4 pin rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: direct pin completion race for %q", billing.ErrPostingOwnershipFence, operationKey)
	}
	return nil
}

// b2b4EnsureCostPassThroughReplayPin backfills/completes the per-head pin for
// an exact replay with zero new money. If the pin advanced to a later
// replacement (different completion), the older replay stays stable without
// mutation. Callers hold the tx and commit after.
func (s *DurableStore) b2b4EnsureCostPassThroughReplayPin(ctx context.Context, tx bun.Tx, accountID string, callID billing.BillingCallID, pinKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, pin billing.PostingPin, pinFound bool, sourceKey string) error {
	// Determine the real outcome transaction: journal ID equals sourceKey when
	// a delta journal exists, else empty (zero-delta head advance).
	completionTxID := ""
	if _, found, jerr := lookupJournalBySource(ctx, tx, accountID, billing.JournalBookFinancial, sourceKey); jerr != nil {
		return jerr
	} else if found {
		completionTxID = sourceKey
	}
	if !pinFound {
		nowUnix := nowUnixNano()
		if err := b2b4BackfillCostPassThroughPinTx(ctx, tx, s, accountID, callID, pinKey, owner, curVersion, curEpoch, curGeneration, curState, sourceKey, completionTxID, nowUnix); err != nil {
			return err
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
		if rerr != nil {
			return rerr
		}
		if !refound {
			return fmt.Errorf("%w: cost pass-through pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
		}
		repinned, err := postingOwnershipRowToPin(rerow)
		if err != nil {
			return err
		}
		if repinned.Owner != owner {
			return fmt.Errorf("%w: cost pass-through pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
		}
		if repinned.Status == billing.PostingPinPinned {
			nowUnix := nowUnixNano()
			if nowUnix < repinned.CreatedAtUnix {
				nowUnix = repinned.CreatedAtUnix
			}
			if err := b2b4CompleteCostPassThroughPinTx(ctx, tx, s.storeID, pinKey, sourceKey, completionTxID, nowUnix); err != nil {
				// Concurrent completer won: verify exact outcome.
				rerow2, refound2, rerr2 := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
				if rerr2 != nil {
					return rerr2
				}
				if !refound2 {
					return fmt.Errorf("%w: cost pass-through pin missing after race", billing.ErrPostingOwnershipNotFound)
				}
				remarker, merr := postingOwnershipRowToPin(rerow2)
				if merr != nil {
					return merr
				}
				if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == sourceKey && remarker.CompletionTransactionID == completionTxID && remarker.Owner == owner {
					return nil
				}
				return fmt.Errorf("%w: cost pass-through pin completion race for %q", billing.ErrPostingOwnershipConflict, pinKey)
			}
		} else if repinned.CompletionOperationKey != sourceKey || repinned.CompletionTransactionID != completionTxID {
			// Pin advanced to a later replacement; older exact replay stays
			// stable without touching newer authority.
			return nil
		}
		return nil
	}
	if pin.Status == billing.PostingPinCompleted {
		if pin.CompletionOperationKey != sourceKey || pin.CompletionTransactionID != completionTxID {
			// Replacement advanced the head pin; exact older replay stays
			// stable with zero pin mutation.
			return nil
		}
		return nil
	}
	nowUnix := nowUnixNano()
	if nowUnix < pin.CreatedAtUnix {
		nowUnix = pin.CreatedAtUnix
	}
	if err := b2b4CompleteCostPassThroughPinTx(ctx, tx, s.storeID, pinKey, sourceKey, completionTxID, nowUnix); err != nil {
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
		if rerr != nil {
			return rerr
		}
		if !refound {
			return fmt.Errorf("%w: cost pass-through pin missing after race", billing.ErrPostingOwnershipNotFound)
		}
		remarker, merr := postingOwnershipRowToPin(rerow)
		if merr != nil {
			return merr
		}
		if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == sourceKey && remarker.CompletionTransactionID == completionTxID && remarker.Owner == owner {
			return nil
		}
		return fmt.Errorf("%w: cost pass-through pin completion race for %q", billing.ErrPostingOwnershipConflict, pinKey)
	}
	return nil
}

// b2b4CompleteCostPassThroughPin records the new revision outcome atomically:
// pinned pins complete, completed pins with the same owner advance to the
// latest outcome (one head authority for the replacement chain).
func (s *DurableStore) b2b4CompleteCostPassThroughPin(ctx context.Context, tx bun.Tx, pinKey, owner, completionOpKey, completionTxID string, pin billing.PostingPin, pinFound bool) error {
	if err := s.b2b4Fault("b2b4-before-pin-complete"); err != nil {
		return err
	}
	nowUnix := nowUnixNano()
	if pinFound && nowUnix < pin.CreatedAtUnix {
		nowUnix = pin.CreatedAtUnix
	}
	if !pinFound {
		return fmt.Errorf("%w: cost pass-through pin missing for completion", billing.ErrPostingOwnershipNotFound)
	}
	if pin.Status == billing.PostingPinPinned {
		return b2b4CompleteCostPassThroughPinTx(ctx, tx, s.storeID, pinKey, completionOpKey, completionTxID, nowUnix)
	}
	if pin.Owner != owner {
		return fmt.Errorf("%w: cost pass-through pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
	}
	return b2b4UpdateCostPassThroughPinCompletionTx(ctx, tx, s.storeID, pinKey, owner, completionOpKey, completionTxID, nowUnix)
}

// b2b4EnsureDirectReplayPin backfills/completes the per-source pin for an
// exact direct replay with zero new money.
func (s *DurableStore) b2b4EnsureDirectReplayPin(ctx context.Context, tx bun.Tx, accountID, sourceKey, pinKey, owner string, curVersion, curEpoch uint64, curGeneration int, curState billing.AccountingCutoverState, pin billing.PostingPin, pinFound bool, completionOpKey, completionTxID string) error {
	if !pinFound {
		nowUnix := nowUnixNano()
		if err := b2b4BackfillDirectPinTx(ctx, tx, s, accountID, sourceKey, pinKey, owner, curVersion, curEpoch, curGeneration, curState, completionOpKey, completionTxID, nowUnix); err != nil {
			return err
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
		if rerr != nil {
			return rerr
		}
		if !refound {
			return fmt.Errorf("%w: direct pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
		}
		repinned, err := postingOwnershipRowToPin(rerow)
		if err != nil {
			return err
		}
		if repinned.Owner != owner {
			return fmt.Errorf("%w: direct pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
		}
		if repinned.Status == billing.PostingPinPinned {
			nowUnix := nowUnixNano()
			if nowUnix < repinned.CreatedAtUnix {
				nowUnix = repinned.CreatedAtUnix
			}
			if err := b2b4CompleteDirectPinTx(ctx, tx, s.storeID, pinKey, completionOpKey, completionTxID, nowUnix); err != nil {
				rerow2, refound2, rerr2 := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
				if rerr2 != nil {
					return rerr2
				}
				if !refound2 {
					return fmt.Errorf("%w: direct pin missing after race", billing.ErrPostingOwnershipNotFound)
				}
				remarker, merr := postingOwnershipRowToPin(rerow2)
				if merr != nil {
					return merr
				}
				if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == completionOpKey && remarker.CompletionTransactionID == completionTxID && remarker.Owner == owner {
					return nil
				}
				return fmt.Errorf("%w: direct pin completion race for %q", billing.ErrPostingOwnershipConflict, pinKey)
			}
		} else if repinned.CompletionOperationKey != completionOpKey || repinned.CompletionTransactionID != completionTxID {
			return fmt.Errorf("%w: direct pin completion mismatch for %q", billing.ErrPostingOwnershipConflict, pinKey)
		}
		return nil
	}
	if pin.Status == billing.PostingPinCompleted {
		if pin.CompletionOperationKey != completionOpKey || pin.CompletionTransactionID != completionTxID {
			return fmt.Errorf("%w: direct pin completion mismatch for %q", billing.ErrPostingOwnershipConflict, pinKey)
		}
		return nil
	}
	nowUnix := nowUnixNano()
	if nowUnix < pin.CreatedAtUnix {
		nowUnix = pin.CreatedAtUnix
	}
	if err := b2b4CompleteDirectPinTx(ctx, tx, s.storeID, pinKey, completionOpKey, completionTxID, nowUnix); err != nil {
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
		if rerr != nil {
			return rerr
		}
		if !refound {
			return fmt.Errorf("%w: direct pin missing after race", billing.ErrPostingOwnershipNotFound)
		}
		remarker, merr := postingOwnershipRowToPin(rerow)
		if merr != nil {
			return merr
		}
		if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == completionOpKey && remarker.CompletionTransactionID == completionTxID && remarker.Owner == owner {
			return nil
		}
		return fmt.Errorf("%w: direct pin completion race for %q", billing.ErrPostingOwnershipConflict, pinKey)
	}
	return nil
}
