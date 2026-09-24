package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun/dialect"
)

var _ billing.CallSettlementStore = (*DurableStore)(nil)

func (s *DurableStore) ApplyCallBillingResult(ctx context.Context, input billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	if s == nil || s.db == nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: nil store")
	}
	if err := billing.ValidateCustomerSettlementOperationKind(input.OperationKind); err != nil {
		return billing.CallSettlement{}, err
	}
	owner, err := billing.ResolveCustomerSettlementOwner(input)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	// Generic authoritative V2 valuation fence: V2-owned money requires a
	// complete bound component CustomerValuation (subject/scope, currency,
	// amount, and result identity bound to the settled call/exposure) or an
	// explicit cost-pass-through under its complete contract, as defined by
	// ValidateCallRatingResultForSettlement. This runs before any journal,
	// balance, exposure, or pin effect so direct V2 scalar or ID-only input
	// fails transactionally with zero effects, regardless of resolver
	// implementation. V1 drain and legacy empty owners preserve historical
	// scalar replay.
	if err := billing.ValidateCallRatingResultForSettlement(input.Result, input.Call, input.Exposure, owner); err != nil {
		return billing.CallSettlement{}, err
	}
	call, err := input.Call.Seal()
	if err != nil {
		return billing.CallSettlement{}, err
	}
	if input.Claim != nil {
		if err := billing.ValidateCustomerSettlementClaim(*input.Claim, call.AccountID, call.CallID); err != nil {
			return billing.CallSettlement{}, err
		}
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
	// F1: lock the per-store marker before account/pin effects so activation
	// serializes. The later marker load reuses the same tx snapshot.
	if _, err := s.ensureAndLockAccountingCutoverTx(ctx, tx); err != nil {
		return billing.CallSettlement{}, err
	}
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
	// B2b1 posting-time ownership fence (customer_call_settlement only).
	// Bind canonical sourceKey to exactly one owner/marker epoch via B1 pins
	// before any customer monetary effect. Pin + money commit atomically in
	// this transaction; exact retry returns the existing outcome with a
	// completed pin.
	owner, err := billing.ResolveCustomerSettlementOwner(input)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	// Defense in depth: re-validate the rating result for its settlement
	// binding at the transaction entry, before any pin/journal/balance/
	// exposure mutation. Direct V2 scalar or ID-only input fails here with
	// zero effects even if a caller bypassed the worker fence.
	if err := billing.ValidateCallRatingResultForSettlement(result, call, expected, owner); err != nil {
		return billing.CallSettlement{}, err
	}
	if err := s.b2b1Fault("b2b1-enter"); err != nil {
		return billing.CallSettlement{}, err
	}
	// F1: already holds the per-store marker lock from tx entry; re-lock
	// here keeps the ordinary SELECT out of monetary paths.
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	var marker billing.AccountingCutoverMarker
	var curState billing.AccountingCutoverState
	var curVersion, curEpoch uint64
	var curGeneration int
	if markerFound {
		marker, err = accountingCutoverRowToMarker(markerRow)
		if err != nil {
			return billing.CallSettlement{}, err
		}
		curState = marker.State
		curVersion = marker.Version
		curEpoch = marker.Epoch
		curGeneration = marker.Generation
	} else {
		curState = b2b1CustomerStateForMissingMarker()
		curVersion, curEpoch = 1, 1
		curGeneration = billing.AccountingCutoverGenerationV1
	}
	// Claim TOCTOU: when the worker passes B2a claim metadata, it must match
	// canonical identity (already validated outside tx) and its epoch must
	// equal the current marker epoch for new postings. Replay of already
	// completed money allows stale claims (no new effect).
	if input.Claim != nil {
		if input.Claim.OperationKey != sourceKey {
			return billing.CallSettlement{}, fmt.Errorf("%w: claim key %q != canonical %q", billing.ErrPostingOwnershipConflict, input.Claim.OperationKey, sourceKey)
		}
		if input.Claim.Owner != owner {
			return billing.CallSettlement{}, fmt.Errorf("%w: claim owner %q != posting owner %q", billing.ErrPostingOwnershipConflict, input.Claim.Owner, owner)
		}
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return billing.CallSettlement{}, err
		}
		if pin.Kind != billing.PostingOperationCustomerSettlement || pin.OperationKey != sourceKey || pin.AccountID != call.AccountID || pin.CallID != call.CallID {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin identity mismatch for %q", billing.ErrPostingOwnershipConflict, sourceKey)
		}
		if pin.Owner != owner {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
		}
	}
	expectedTxID := ""
	if effectiveCharge.Nano > 0 {
		expectedTxID = sourceKey
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, call.AccountID, operationKind, call.CallID.String()); lookupErr != nil {
		return billing.CallSettlement{}, lookupErr
	} else if found {
		if existing.Fingerprint != settlementFingerprint {
			return billing.CallSettlement{}, ErrOperationConflict
		}
		// Exact retry: no new money. Ensure the pin is completed with the
		// real outcome in the same transaction, then return replay. This
		// allows V1 completed-history replay in v2_active without new effects.
		if err := s.b2b1Fault("b2b1-replay-pin"); err != nil {
			return billing.CallSettlement{}, err
		}
		if !pinFound {
			// Backfill a completed V1 pin for pre-B2b1 money (crash-safe:
			// same tx, no new money, just ownership completion).
			nowUnix := time.Now().UTC().UnixNano()
			insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
			if s.db.Dialect().Name() == dialect.SQLite {
				insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			}
			if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), sourceKey, call.AccountID, call.CallID.String(), "", "", "", "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinCompleted), sourceKey, expectedTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
				return billing.CallSettlement{}, fmt.Errorf("billingstore: b2b1 backfill customer pin: %w", err)
			}
			// Converge concurrent backfills: reload and verify completion.
			rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, sourceKey)
			if rerr != nil {
				return billing.CallSettlement{}, rerr
			}
			if !refound {
				return billing.CallSettlement{}, fmt.Errorf("%w: customer pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
			}
			repinned, err := postingOwnershipRowToPin(rerow)
			if err != nil {
				return billing.CallSettlement{}, err
			}
			if repinned.Owner != owner {
				return billing.CallSettlement{}, fmt.Errorf("%w: customer pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
			}
			if repinned.Status == billing.PostingPinPinned {
				// Lost insert race left it pinned; complete it below.
				pin = repinned
				pinFound = true
			} else {
				if repinned.CompletionOperationKey != sourceKey || repinned.CompletionTransactionID != expectedTxID {
					return billing.CallSettlement{}, fmt.Errorf("%w: customer pin completion mismatch for %q", billing.ErrPostingOwnershipConflict, sourceKey)
				}
				if err := tx.Commit(); err != nil {
					return billing.CallSettlement{}, err
				}
				return billing.CallSettlement{CallID: call.CallID, Replayed: true, Breached: breached, OverrunNano: overrunNano}, nil
			}
		}
		if pinFound {
			if pin.Status == billing.PostingPinCompleted {
				if pin.CompletionOperationKey != sourceKey || pin.CompletionTransactionID != expectedTxID {
					return billing.CallSettlement{}, fmt.Errorf("%w: customer pin completion mismatch for %q", billing.ErrPostingOwnershipConflict, sourceKey)
				}
				if err := tx.Commit(); err != nil {
					return billing.CallSettlement{}, err
				}
				return billing.CallSettlement{CallID: call.CallID, Replayed: true, Breached: breached, OverrunNano: overrunNano}, nil
			}
			// Pinned but money already posted (crash between money and pin
			// completion in pre-B2b1 code, or concurrent replay): complete now.
			nowUnix := max(time.Now().UTC().UnixNano(), pin.CreatedAtUnix)
			if err := b2b1CompleteCustomerPinTx(ctx, tx, s.storeID, sourceKey, sourceKey, expectedTxID, nowUnix); err != nil {
				// Concurrent completer won: reload to classify replay vs conflict.
				rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, sourceKey)
				if rerr != nil {
					return billing.CallSettlement{}, rerr
				}
				if !refound {
					return billing.CallSettlement{}, fmt.Errorf("%w: customer pin missing after race", billing.ErrPostingOwnershipNotFound)
				}
				remarker, merr := postingOwnershipRowToPin(rerow)
				if merr != nil {
					return billing.CallSettlement{}, merr
				}
				if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == sourceKey && remarker.CompletionTransactionID == expectedTxID && remarker.Owner == owner {
					if err := tx.Commit(); err != nil {
						return billing.CallSettlement{}, err
					}
					return billing.CallSettlement{CallID: call.CallID, Replayed: true, Breached: breached, OverrunNano: overrunNano}, nil
				}
				return billing.CallSettlement{}, fmt.Errorf("%w: customer pin completion race for %q", billing.ErrPostingOwnershipConflict, sourceKey)
			}
			if err := tx.Commit(); err != nil {
				return billing.CallSettlement{}, err
			}
			return billing.CallSettlement{CallID: call.CallID, Replayed: true, Breached: breached, OverrunNano: overrunNano}, nil
		}
	}
	// New posting (no snapshot): enforce one-owner fence before any
	// journal/balance/unit/exposure mutation. Pin owner is stable history;
	// the token carries current-marker lease authority (F6). Only
	// token==current is required; pin acquisition epoch is never compared.
	if input.Claim != nil {
		// Stale worker (lease waking after epoch change) fails closed before money.
		if input.Claim.MarkerVersion != curVersion || input.Claim.MarkerEpoch != curEpoch || input.Claim.MarkerState != curState {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer claim %d/%d/%q != current %d/%d/%q",
				billing.ErrPostingOwnershipFence, input.Claim.MarkerVersion, input.Claim.MarkerEpoch, string(input.Claim.MarkerState), curVersion, curEpoch, string(curState))
		}
	}
	if !pinFound {
		if !billing.IsPostingOwnerAllowedForNew(curState, owner) {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer owner %q not allowed for new in %q", billing.ErrPostingOwnershipFence, owner, string(curState))
		}
		if input.Claim != nil {
			// New work with a claim but no pin is unpinned (never classified).
			return billing.CallSettlement{}, fmt.Errorf("%w: customer claim without classified pin for %q", billing.ErrPostingOwnershipFence, sourceKey)
		}
		if curState == billing.AccountingCutoverV1Draining {
			return billing.CallSettlement{}, fmt.Errorf("%w: draining forbids new customer settlement for %q", billing.ErrPostingOwnershipFence, sourceKey)
		}
		if owner == billing.PostingOwnerV2 && !billing.IsV2NewWorkAuthorized(curState) {
			return billing.CallSettlement{}, fmt.Errorf("%w: V2 customer settlement requires v2_active", billing.ErrCutoverV2NotAuthorized)
		}
		if err := s.b2b1Fault("b2b1-pin-acquire"); err != nil {
			return billing.CallSettlement{}, err
		}
		nowUnix := time.Now().UTC().UnixNano()
		insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
		if s.db.Dialect().Name() == dialect.SQLite {
			insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		}
		if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), sourceKey, call.AccountID, call.CallID.String(), "", "", "", "", "", owner, int64(curVersion), int64(curEpoch), curGeneration, string(curState), string(billing.PostingPinPinned), "", "", nowUnix, nowUnix, 0).Exec(ctx); err != nil {
			return billing.CallSettlement{}, fmt.Errorf("billingstore: b2b1 acquire customer pin: %w", err)
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, sourceKey)
		if rerr != nil {
			return billing.CallSettlement{}, rerr
		}
		if !refound {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
		}
		repin, err := postingOwnershipRowToPin(rerow)
		if err != nil {
			return billing.CallSettlement{}, err
		}
		if repin.Owner != owner {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repin.Owner, owner)
		}
		pin = repin
		pinFound = true
		// A concurrent V2 contender may have won the insert race in v2_active;
		// owner mismatch above already conflicts. A concurrent same-owner
		// replay converges here.
		if !billing.IsPostingPinReplay(pin, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: call.AccountID, CallID: call.CallID, Owner: owner, ExpectedMarkerVersion: curVersion, ExpectedMarkerEpoch: curEpoch}, sourceKey) {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin replay identity mismatch", billing.ErrPostingOwnershipConflict)
		}
	} else {
		// Pinned path: only correctly pinned work may complete. Draining
		// requires the worker's matching current-marker token (no optional
		// bypass); without it the posting fences even though a pin exists.
		if curState == billing.AccountingCutoverV1Draining && input.Claim == nil {
			return billing.CallSettlement{}, fmt.Errorf("%w: draining customer settlement requires claim metadata for %q", billing.ErrPostingOwnershipFence, sourceKey)
		}
		if !billing.IsPostingPinAcquireReplayAllowed(curState, pin) {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin replay not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
		}
		if !billing.IsPostingPinCompleteAllowed(curState, pin) {
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin completion not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
		}
		if pin.Status == billing.PostingPinCompleted {
			// Completed without snapshot (manual completion): never invent money.
			return billing.CallSettlement{}, fmt.Errorf("%w: customer pin already completed for %q", billing.ErrPostingOwnershipConflict, sourceKey)
		}
	}
	if err := s.b2b1Fault("b2b1-before-effects"); err != nil {
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
		if err := s.b2b1Fault("b2b1-before-journal"); err != nil {
			return billing.CallSettlement{}, err
		}
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
	// B2b1 atomic completion: pin + journal/balance/unit/exposure/terminal
	// commit in the same transaction. No window where money is posted but the
	// pin remains reusable.
	if err := s.b2b1Fault("b2b1-before-pin-complete"); err != nil {
		return billing.CallSettlement{}, err
	}
	pinCompletionTxID := posting.Transaction.ID
	if effectiveCharge.Nano == 0 {
		pinCompletionTxID = ""
	}
	pinNow := time.Now().UTC().UnixNano()
	if pinFound && pinNow < pin.CreatedAtUnix {
		pinNow = pin.CreatedAtUnix
	}
	if err := b2b1CompleteCustomerPinTx(ctx, tx, s.storeID, sourceKey, sourceKey, pinCompletionTxID, pinNow); err != nil {
		return billing.CallSettlement{}, err
	}
	if err := s.b2b1Fault("b2b1-before-commit"); err != nil {
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
