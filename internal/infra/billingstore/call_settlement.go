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
	submissionClaim, hasSubmissionClaim, err := billing.SubmissionFeeClaimForValuation(call, input.Result.CustomerValuation)
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("%w: %w", billing.ErrSettlementInvalid, err)
	}
	if hasSubmissionClaim && (submissionClaim.Amount.Currency != input.Result.CustomerCharge.Currency || submissionClaim.Amount.Nano > input.Result.CustomerCharge.Nano) {
		return billing.CallSettlement{}, fmt.Errorf("%w: submission fee exceeds customer charge", billing.ErrSettlementInvalid)
	}
	if input.Result.CostPassThrough != nil {
		state := input.Result.CostPassThrough
		if err := state.Validate(input.Result.CustomerCharge.Currency); err != nil {
			return billing.CallSettlement{}, err
		}
		if state.PolicyRef != call.ChargePolicyRef || state.PostedAmount != input.Result.CustomerCharge {
			return billing.CallSettlement{}, fmt.Errorf("%w: cost pass-through state does not match call settlement", billing.ErrSettlementInvalid)
		}
	}
	if input.Result.CustomerUnitOperation == nil && input.Result.CustomerUnitFallbackCharge != nil {
		return billing.CallSettlement{}, fmt.Errorf("%w: customer-unit fallback charge has no operation", billing.ErrSettlementInvalid)
	}
	if input.Result.CustomerUnitOperation != nil {
		if err := input.Result.CustomerUnitOperation.Validate(); err != nil {
			return billing.CallSettlement{}, err
		}
		if input.Result.CustomerUnitOperation.Key.AccountID != call.AccountID {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer-unit account differs from settled call", billing.ErrSettlementInvalid)
		}
		if input.Result.CustomerUnitFallbackCharge != nil {
			if err := input.Result.CustomerUnitFallbackCharge.Validate(); err != nil {
				return billing.CallSettlement{}, err
			}
		}
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.CallSettlement, error) {
		return s.applyCallBillingAttempt(ctx, call, input, submissionClaim, hasSubmissionClaim)
	})
}

func (s *DurableStore) applyCallBillingAttempt(ctx context.Context, call billing.CallUsageRecord, input billing.ApplyCallBillingInput, submissionClaim billing.SubmissionFeeClaim, hasSubmissionClaim bool) (billing.CallSettlement, error) {
	expected := input.Exposure
	result := input.Result
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: begin call settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Customer settlement is a call-closure operation. Requiring the immutable
	// closure row here prevents a caller from turning a merely constructed
	// CallUsageRecord into a speculative customer debit. The complete-call
	// worker normally supplies this proof before reaching the settlement seam;
	// retain the fence at the store boundary for direct/replayed calls too.
	durableCall, err := s.loadCallUsage(ctx, tx, call.CallID)
	if errors.Is(err, ErrUsageRecordNotFound) {
		return billing.CallSettlement{}, fmt.Errorf("%w: durable call closure is required before customer settlement", billing.ErrCallIncomplete)
	}
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: load durable call closure: %w", err)
	}
	if err := billing.CheckCallUsageReplay(durableCall, call); err != nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: durable call closure identity: %w", err)
	}
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), call.AccountID); err != nil {
		return billing.CallSettlement{}, err
	}
	operationKind := input.OperationKind
	if operationKind == "" {
		operationKind = "customer_call_settlement"
	}
	settlementFingerprint := result.Fingerprint
	if result.CostPassThrough != nil {
		costFingerprint, fingerprintErr := result.CostPassThrough.SemanticFingerprint()
		if fingerprintErr != nil {
			return billing.CallSettlement{}, fingerprintErr
		}
		settlementFingerprint += ":cost-pass-through:" + costFingerprint
	}
	if result.CustomerUnitOperation != nil {
		unitFingerprint, fingerprintErr := result.CustomerUnitOperation.SemanticFingerprint()
		if fingerprintErr != nil {
			return billing.CallSettlement{}, fingerprintErr
		}
		settlementFingerprint += ":customer-unit:" + unitFingerprint
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(call.AccountID, call.CallID)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	var existingSubmissionClaim submissionFeeClaimRow
	var submissionClaimFound bool
	if hasSubmissionClaim {
		var lookupErr error
		existingSubmissionClaim, submissionClaimFound, lookupErr = loadSubmissionFeeClaim(ctx, tx, s.storeID, call.AccountID, submissionClaim.SubmissionID)
		if lookupErr != nil {
			return billing.CallSettlement{}, lookupErr
		}
		if submissionClaimFound && !submissionFeeClaimMatches(existingSubmissionClaim, submissionClaim) {
			return billing.CallSettlement{}, ErrOperationConflict
		}
	}
	var row exposureRow
	if err := tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND call_id = ?`, call.AccountID, call.CallID.String()).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallSettlement{}, billing.ErrExposureNotFound
		}
		return billing.CallSettlement{}, err
	}
	exposure, err := exposureFromRow(row)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	effectiveCharge := result.CustomerCharge
	if submissionClaimFound {
		effectiveCharge, err = result.CustomerCharge.Sub(submissionClaim.Amount)
		if err != nil {
			return billing.CallSettlement{}, fmt.Errorf("%w: submission fee deduction: %v", billing.ErrSettlementInvalid, err)
		}
	}
	if effectiveCharge.Nano < 0 {
		return billing.CallSettlement{}, billing.ErrSettlementInvalid
	}
	if effectiveCharge.Currency != exposure.Max.Currency {
		return billing.CallSettlement{}, billing.ErrMoneyCurrencyMismatch
	}
	// Actual incurred cost is truth: an overrun settles under the account
	// policy with explicit breach state instead of discarding the actual.
	// The breach marker binds actual versus max into the idempotency
	// fingerprint, so redelivery replays while a different actual conflicts.
	breached := effectiveCharge.Nano > exposure.Max.Nano
	var overrunNano int64
	if breached {
		overrunNano = effectiveCharge.Nano - exposure.Max.Nano
		settlementFingerprint += fmt.Sprintf(":breach:overrun=%d", overrunNano)
	}
	// The route tariffs actually used to rate this call must resolve to the
	// admitted frozen binding. This fails closed before any journal, balance,
	// exposure, or claim transition on version, content, route, or
	// missing-binding mismatch.
	if err := billing.CheckSettledRouteTariffs(exposure.RouteTariffs, result.RouteTariffs, effectiveCharge); err != nil {
		return billing.CallSettlement{}, err
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, call.AccountID, operationKind, call.CallID.String()); lookupErr != nil {
		return billing.CallSettlement{}, lookupErr
	} else if found {
		if existing.Fingerprint != settlementFingerprint {
			return billing.CallSettlement{}, ErrOperationConflict
		}
		if err := tx.Commit(); err != nil {
			return billing.CallSettlement{}, err
		}
		return billing.CallSettlement{CallID: call.CallID, Replayed: true, Breached: breached, OverrunNano: overrunNano}, nil
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
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	after := account
	if effectiveCharge.Nano > 0 {
		after, err = account.ApplyBalanceDelta(billing.Money{Nano: -effectiveCharge.Nano, Currency: account.Currency})
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
	var customerUnitResult *billing.CustomerUnitOperationResult
	if result.CustomerUnitOperation != nil {
		unitResult, unitErr := s.applyCustomerUnitOperationTx(ctx, tx, *result.CustomerUnitOperation)
		if unitErr != nil {
			return billing.CallSettlement{}, unitErr
		}
		if unitResult.FallbackRequired {
			if result.CustomerUnitFallbackCharge == nil || unitResult.FallbackBound == nil {
				return billing.CallSettlement{}, fmt.Errorf("%w: customer-unit fallback charge is required", billing.ErrSettlementInvalid)
			}
			if err := billing.ValidateCustomerMonetaryFallback(*result.CustomerUnitFallbackCharge, *unitResult.FallbackBound); err != nil {
				return billing.CallSettlement{}, err
			}
		} else if result.CustomerUnitFallbackCharge != nil {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer-unit fallback charge is not authorized", billing.ErrSettlementInvalid)
		}
		customerUnitResult = &unitResult
	}
	if hasSubmissionClaim && !submissionClaimFound {
		if err := insertSubmissionFeeClaim(ctx, tx, s.storeID, call.AccountID, call.CallID.String(), submissionClaim); err != nil {
			return billing.CallSettlement{}, err
		}
	}
	afterSnapshot, err := snapshotForAccount(after)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	var posting billing.Posting
	if effectiveCharge.Nano > 0 {
		journal := billing.JournalTransaction{
			ID: sourceKey, Book: billing.JournalBookFinancial, Currency: account.Currency, SourceKey: sourceKey,
			AccountID: call.AccountID, TurnID: call.CallID.String(), ALegID: call.ALegID, OperationKind: operationKind,
			BalanceBefore: before.BalanceNano, BalanceAfter: afterSnapshot.BalanceNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: afterSnapshot.SpendableNano, CreditFloor: afterSnapshot.CreditFloorNano, CreditLimit: afterSnapshot.CreditLimitNano,
			Mode: string(afterSnapshot.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: afterSnapshot.Version,
			Entries: []billing.JournalEntry{{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: effectiveCharge}, {LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: effectiveCharge}},
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
	if effectiveCharge.Nano > 0 {
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
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: sourceKey + ":" + operationKind, AccountID: call.AccountID, OperationKind: operationKind, SourceKey: call.CallID.String(), Fingerprint: settlementFingerprint, Before: before, After: afterSnapshot}); err != nil {
		return billing.CallSettlement{}, err
	}
	if result.CostPassThrough != nil {
		state := result.CostPassThrough.Clone()
		// The durable settlement transaction is the sole authority for this
		// linkage; never persist a caller-supplied original transaction ID.
		state.OriginalTransactionID = posting.Transaction.ID
		if err := persistCostPassThroughHeadInTx(ctx, tx, call, sourceKey, state, settlementFingerprint); err != nil {
			return billing.CallSettlement{}, err
		}
	}
	if _, err := tx.NewRaw(`UPDATE usage_call_records SET claim_status = 'processed' WHERE call_id = ? AND claim_status IN ('pending','claimed','reconcile_required')`, call.CallID.String()).Exec(ctx); err != nil {
		return billing.CallSettlement{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.CallSettlement{}, err
	}
	var state *billing.CostPassThroughSettlement
	if result.CostPassThrough != nil {
		copy := result.CostPassThrough.Clone()
		copy.OriginalTransactionID = posting.Transaction.ID
		state = &copy
	}
	return billing.CallSettlement{CallID: call.CallID, Customer: posting, CustomerUnitResult: customerUnitResult, CostPassThrough: state, Breached: breached, OverrunNano: overrunNano}, nil
}
