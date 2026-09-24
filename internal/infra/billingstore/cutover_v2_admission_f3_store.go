package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Phase 17.3 Finding F3: usable legal V2 admission/settlement pipeline after
// cutover.
//
// After coordinator-authorized v2_active on an empty/drained store, a fresh V2
// customer call admits through the actual production internal admission path
// with explicit V2 owner, captures call/leg terminal evidence under that
// admitted owner, enqueues/claims provider+customer work with owner V2, and
// settles/posts exactly once with V2 pins/tokens. Before v2_active the same
// explicit V2 admission is rejected; in active, legacy V1 admission remains
// fenced. No preloaded V1 exposure/usage, direct marker skipping, pin
// deletion, or test-only insertion occurs. Version authority is consumed by
// actual APIs via explicit version-aware methods; V1 defaults remain
// compatible pre-cutover.
//
// Owner is chosen from the durable marker per StoreID at admission (F1 lock
// held, same tx as exposure+pin) and retained through the call lifecycle via
// the durable exposure+pin record; terminal evidence validates against that
// admitted record, not a fresh global read that could reclassify. Provider
// leg first appears at terminal under the admitted V2 call and gets V2
// provider work/pin atomically. F1 marker serialization and F6 token claims
// are exercised throughout; no second call registry exists.
//
// This file owns the explicit version-aware admission/terminal methods.
// Legacy V1 methods (AdmitExposure, AppendCallUsage, AppendCallLegUsage)
// delegate V1/empty owner here only via the WithOwner wrappers below; their
// pre-cutover behavior is preserved by routing ""/V1 through the original V1
// gate+pin path.

// normalizeF3Owner maps empty to the legacy V1 default and validates explicit
// V1/V2 owners. Unknown owners fail closed.
func normalizeF3Owner(owner string) (string, error) {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" {
		return billing.PostingOwnerV1, nil
	}
	if trimmed != billing.PostingOwnerV1 && trimmed != billing.PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown posting owner %q", billing.ErrPostingOwnershipInvalid, billing.ErrInvalidRecord, owner)
	}
	return trimmed, nil
}

// AdmitExposureWithOwner is the explicit version-aware production admission
// path. Empty owner preserves the legacy V1 default (compatible pre-cutover).
// V1 is fenced in draining/active via the ordinary V1 gate. V2 requires
// v2_active authorization and pins V2 ownership at start in the same tx.
func (s *DurableStore) AdmitExposureWithOwner(ctx context.Context, input billing.AdmitExposureInput, owner string) (billing.CallExposure, error) {
	effective, err := normalizeF3Owner(owner)
	if err != nil {
		return billing.CallExposure{}, err
	}
	if effective == billing.PostingOwnerV1 {
		return s.AdmitExposure(ctx, input)
	}
	if s == nil || s.db == nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: nil store")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.CallExposure, error) {
		return s.admitExposureV2Attempt(ctx, input)
	})
}

func (s *DurableStore) admitExposureV2Attempt(ctx context.Context, input billing.AdmitExposureInput) (billing.CallExposure, error) {
	var zero billing.CallExposure
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("billingstore: begin V2 exposure admission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	accountID := strings.TrimSpace(input.AccountID)
	callIDStr := strings.TrimSpace(input.CallID)
	if accountID == "" || callIDStr == "" {
		return zero, fmt.Errorf("%w: account id and call id are required", billing.ErrExposureInvalid)
	}
	callID, err := billing.ParseBillingCallID(callIDStr)
	if err != nil {
		return zero, fmt.Errorf("%w: %w: %v", billing.ErrExposureInvalid, billing.ErrInvalidRecord, err)
	}
	// F1: lock the per-store marker before account/exposure effects so
	// activation serializes. Version authority comes from this locked
	// snapshot, not a fresh global read.
	admissionMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return zero, err
	}
	if !billing.IsV2NewWorkAuthorized(admissionMarker.State) {
		return zero, fmt.Errorf("%w: store %q is in %q, V2 requires v2_active",
			billing.ErrCutoverV2NotAuthorized, s.storeID, string(admissionMarker.State))
	}
	if !billing.IsPostingOwnerAllowedForNew(admissionMarker.State, billing.PostingOwnerV2) {
		return zero, fmt.Errorf("%w: customer owner %q not allowed for new in %q",
			billing.ErrPostingOwnershipFence, billing.PostingOwnerV2, string(admissionMarker.State))
	}
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return zero, err
	}
	var existing exposureRow
	err = tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND call_id = ?`, accountID, callIDStr).Scan(ctx, &existing)
	if err == nil {
		exposure, decodeErr := exposureFromRow(existing)
		if decodeErr != nil {
			return zero, decodeErr
		}
		if replayErr := billing.CheckExposureReplay(exposure, input); replayErr != nil {
			return zero, replayErr
		}
		// One-operation-one-owner: replaying a V1-admitted call as V2
		// conflicts; replaying V2 as V2 is idempotent.
		opKey, kerr := billing.CustomerPostingOperationKey(accountID, callID)
		if kerr != nil {
			return zero, kerr
		}
		if row, found, lerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey); lerr != nil {
			return zero, lerr
		} else if found {
			pin, perr := postingOwnershipRowToPin(row)
			if perr != nil {
				return zero, perr
			}
			if pin.Owner != billing.PostingOwnerV2 {
				return zero, fmt.Errorf("%w: customer pin for %q owned by %q, V2 replay requires V2",
					billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
			}
		} else {
			// Exposure exists without a pin (pre-pin legacy V1): V2 reuse
			// conflicts; the call is already bound to V1 admission.
			return zero, fmt.Errorf("%w: customer pin missing for %q, V2 replay requires V2 admission",
				billing.ErrPostingOwnershipConflict, opKey)
		}
		if err := tx.Commit(); err != nil {
			return zero, fmt.Errorf("billingstore: commit V2 exposure replay: %w", err)
		}
		return exposure, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("billingstore: lookup V2 exposure: %w", err)
	}
	// Cross-account isolation: same call bound to a different account fails.
	var otherAccounts []string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ?`, callIDStr).Scan(ctx, &otherAccounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("billingstore: cross-account V2 exposure check: %w", err)
	}
	for _, other := range otherAccounts {
		if strings.TrimSpace(other) != "" && strings.TrimSpace(other) != accountID {
			return zero, fmt.Errorf("%w: call %q bound to account %q, claimant %q",
				billing.ErrPostingOwnershipConflict, callIDStr, strings.TrimSpace(other), accountID)
		}
	}
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return zero, err
	}
	var rows []exposureRow
	if err := tx.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND status = 'open' ORDER BY call_id`, accountID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("billingstore: list open exposures: %w", err)
	}
	exposures := make([]billing.CallExposure, 0, len(rows))
	for _, row := range rows {
		decoded, decodeErr := exposureFromRow(row)
		if decodeErr != nil {
			return zero, decodeErr
		}
		exposures = append(exposures, decoded)
	}
	admitted, err := billing.EvaluateAdmit(account, exposures, input)
	if err != nil {
		return zero, err
	}
	pricingRef, err := json.Marshal(admitted.PricingRef)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode V2 exposure pricing ref: %w", err)
	}
	policyRef, err := json.Marshal(admitted.ChargePolicyRef)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode V2 exposure policy ref: %w", err)
	}
	routeTariffs, err := json.Marshal(admitted.RouteTariffs)
	if err != nil {
		return zero, fmt.Errorf("billingstore: encode V2 exposure route tariffs: %w", err)
	}
	_, err = tx.NewRaw(`INSERT INTO call_exposures(exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		exposureKey(admitted.AccountID, admitted.CallID), admitted.AccountID, admitted.CallID, admitted.Max.Nano, admitted.Max.Currency, string(pricingRef), string(policyRef), string(routeTariffs), admitted.Fingerprint,
		admitted.Basis.BalanceNano, admitted.Basis.CreditFloorNano, admitted.Basis.OpenExposureNano, admitted.Basis.SettledHeadroomNano, admitted.Basis.SafetyMarginBeforeNano, admitted.Basis.SafetyMarginAfterNano, string(admitted.Status), admitted.CreatedAt).Exec(ctx)
	if err != nil {
		return zero, fmt.Errorf("billingstore: insert V2 exposure: %w", err)
	}
	// Pin V2 ownership at start in the same tx with the current marker epoch.
	if err := s.insertAdmissionCustomerPinWithOwnerTx(ctx, tx, admissionMarker, admitted.AccountID, admitted.CallID, billing.PostingOwnerV2); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("billingstore: commit V2 exposure: %w", err)
	}
	return admitted, nil
}

// insertAdmissionCustomerPinWithOwnerTx durably pins explicit ownership at
// admission in the same tx as exposure insertion. Existing pins with the same
// owner are idempotent; conflicting ownership fails closed.
func (s *DurableStore) insertAdmissionCustomerPinWithOwnerTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, accountID, callIDStr, owner string) error {
	if strings.TrimSpace(s.storeID) == "" {
		return fmt.Errorf("%w: store scope is required for V2 admission", billing.ErrPostingOwnershipInvalid)
	}
	accountID = strings.TrimSpace(accountID)
	callIDStr = strings.TrimSpace(callIDStr)
	if accountID == "" || callIDStr == "" {
		return fmt.Errorf("%w: account id and call id are required", billing.ErrPostingOwnershipInvalid)
	}
	callID, err := billing.ParseBillingCallID(callIDStr)
	if err != nil {
		return fmt.Errorf("%w: %w: %v", billing.ErrPostingOwnershipInvalid, billing.ErrInvalidRecord, err)
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		return err
	}
	if existingRow, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey); err != nil {
		return err
	} else if found {
		existing, err := postingOwnershipRowToPin(existingRow)
		if err != nil {
			return err
		}
		if existing.Owner != owner {
			return fmt.Errorf("%w: customer pin for %q owned by %q, admission requires %q",
				billing.ErrPostingOwnershipConflict, opKey, existing.Owner, owner)
		}
		return nil
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, accountID, callID.String(), "", "", "", "", "", owner, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert V2 admission customer pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: admission customer pin unavailable after insert", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return err
	}
	if pin.Owner != owner {
		return fmt.Errorf("%w: customer pin for %q owned by %q, admission requires %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner, owner)
	}
	return nil
}

// AppendCallUsageWithOwner is the explicit version-aware terminal closure
// path. Empty owner preserves the legacy V1 default. V2 validates against
// the admitted V2 exposure+pin record (not a fresh global read) and requires
// v2_active authorization.
func (s *DurableStore) AppendCallUsageWithOwner(ctx context.Context, record billing.CallUsageRecord, owner string) error {
	effective, err := normalizeF3Owner(owner)
	if err != nil {
		return err
	}
	if effective == billing.PostingOwnerV1 {
		return s.AppendCallUsage(ctx, record)
	}
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() error {
		return s.appendCallUsageV2Attempt(ctx, record)
	})
}

func (s *DurableStore) appendCallUsageV2Attempt(ctx context.Context, record billing.CallUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin V2 call usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker so activation serializes with terminal
	// evidence. Version authority for V2 is v2_active; owner authority comes
	// from the admitted exposure+pin below.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("billingstore: lock V2 call usage cutover: %w", err)
	}
	if !billing.IsV2NewWorkAuthorized(lockedMarker.State) {
		return fmt.Errorf("%w: V2 terminal closure requires v2_active (store %q in %q)",
			billing.ErrCutoverV2NotAuthorized, s.storeID, string(lockedMarker.State))
	}
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_call_records WHERE usage_call_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing V2 call usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		// One-owner: replaying a V2 closure as V1 (or vice versa) conflicts
		// via the admitted pin check below.
		if allowed, herr := s.f3V2ClosureOwnerMatchesTx(ctx, tx, sealed); herr != nil {
			return herr
		} else if !allowed {
			return fmt.Errorf("%w: V2 closure replay owner mismatch for %q",
				billing.ErrPostingOwnershipConflict, sealed.CallID.String())
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit V2 call usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup V2 call usage: %w", err)
	}
	// New V2 closure: the admitted V2 exposure+pin proves pre-terminal
	// admission. No new V1 gate applies; V2 authorization above is the gate.
	if allowed, herr := s.f3V2TerminalClosureAllowedTx(ctx, tx, lockedMarker, sealed); herr != nil {
		return herr
	} else if !allowed {
		return fmt.Errorf("%w: V2 terminal closure without V2 admission for %q",
			billing.ErrPostingOwnershipFence, sealed.CallID.String())
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode V2 call usage: %w", err)
	}
	expectedJSON, err := json.Marshal(sealed.ExpectedBLegIDs)
	if err != nil {
		return fmt.Errorf("billingstore: encode V2 expected B-leg IDs: %w", err)
	}
	sealedAt := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO usage_call_records( usage_call_key, fingerprint, call_id, account_id, a_leg_id, session_id, started_at, finished_at, outcome, expected_b_leg_ids, payload_json, sealed_at, claim_status, claim_attempt_count, next_claim_at, last_claim_error ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.Key, sealed.Fingerprint, sealed.CallID.String(), sealed.AccountID, sealed.ALegID, sealed.SessionID,
		sealed.StartedAt, sealed.FinishedAt, string(sealed.Outcome), string(expectedJSON), string(payload), sealedAt,
		usageCallClaimPending, 0, sealedAt, "").Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert V2 call usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit V2 call usage: %w", err)
	}
	return nil
}

// f3V2TerminalClosureAllowedTx reports whether a new V2 closure may proceed:
// an open admitted V2 exposure plus a V2 customer pin for the same
// account/call must exist in the same tx. Cross-account reuse fails closed.
func (s *DurableStore) f3V2TerminalClosureAllowedTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, sealed billing.CallUsageRecord) (bool, error) {
	if marker.State != billing.AccountingCutoverV2Active {
		return false, nil
	}
	accountID := strings.TrimSpace(sealed.AccountID)
	if accountID == "" {
		return false, nil
	}
	if err := sealed.CallID.Validate(); err != nil {
		return false, nil
	}
	var otherAccounts []string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ?`, sealed.CallID.String()).Scan(ctx, &otherAccounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("billingstore: V2 cross-account exposure check: %w", err)
	}
	for _, other := range otherAccounts {
		if strings.TrimSpace(other) != "" && strings.TrimSpace(other) != accountID {
			return false, fmt.Errorf("%w: call %q bound to account %q, claimant %q",
				billing.ErrPostingOwnershipConflict, sealed.CallID.String(), strings.TrimSpace(other), accountID)
		}
	}
	var exposureStatus string
	if err := tx.NewRaw(`SELECT status FROM call_exposures WHERE account_id = ? AND call_id = ?`, accountID, sealed.CallID.String()).Scan(ctx, &exposureStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("billingstore: V2 terminal exposure lookup: %w", err)
	}
	if strings.TrimSpace(exposureStatus) != string(billing.ExposureOpen) {
		return false, nil
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, sealed.CallID)
	if err != nil {
		return false, nil
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return false, err
	}
	if pin.Owner != billing.PostingOwnerV2 {
		return false, fmt.Errorf("%w: customer pin for %q owned by %q, V2 terminal requires V2",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return true, nil
}

// f3V2ClosureOwnerMatchesTx verifies replay owner convergence for V2
// closures: the admitted pin must be V2-owned.
func (s *DurableStore) f3V2ClosureOwnerMatchesTx(ctx context.Context, tx bun.Tx, sealed billing.CallUsageRecord) (bool, error) {
	opKey, err := billing.CustomerPostingOperationKey(strings.TrimSpace(sealed.AccountID), sealed.CallID)
	if err != nil {
		return false, nil
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return false, err
	}
	if pin.Owner != billing.PostingOwnerV2 {
		return false, fmt.Errorf("%w: customer pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return true, nil
}

// AppendCallLegUsageWithOwner is the explicit version-aware terminal leg
// path. Empty owner preserves the legacy V1 default. V2 validates against
// the admitted V2 call record and acquires the V2 provider pin atomically
// with work enqueue in the same tx.
func (s *DurableStore) AppendCallLegUsageWithOwner(ctx context.Context, record billing.CallLegUsageRecord, owner string) error {
	effective, err := normalizeF3Owner(owner)
	if err != nil {
		return err
	}
	if effective == billing.PostingOwnerV1 {
		return s.AppendCallLegUsage(ctx, record)
	}
	return withAccountTxErr(ctx, accountTxRetry{
		Attempts: 20,
		Delay:    5 * time.Millisecond,
		Classify: func(err error) error {
			if errors.Is(err, ErrLegAttemptSequenceConflict) {
				return err
			}
			return nil
		},
	}, func() error {
		return s.appendCallLegUsageV2Attempt(ctx, record)
	})
}

func (s *DurableStore) appendCallLegUsageV2Attempt(ctx context.Context, record billing.CallLegUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin V2 call-leg usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker so activation serializes with provider
	// work enqueue+pin.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("billingstore: lock V2 call-leg cutover: %w", err)
	}
	if !billing.IsV2NewWorkAuthorized(lockedMarker.State) {
		return fmt.Errorf("%w: V2 terminal leg requires v2_active (store %q in %q)",
			billing.ErrCutoverV2NotAuthorized, s.storeID, string(lockedMarker.State))
	}
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallLegUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing V2 call-leg usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallLegUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit V2 call-leg usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup V2 call-leg usage: %w", err)
	}
	// New V2 leg first materializing at terminal is owned by the admitted V2
	// call. The admitted V2 exposure+customer pin proves the call owner;
	// provider work/pin inherits it (no new post-boundary V1 work).
	handoffAccount, allowed, herr := s.f3V2TerminalLegAllowedTx(ctx, tx, lockedMarker, sealed)
	if herr != nil {
		return herr
	}
	if !allowed {
		return fmt.Errorf("%w: V2 terminal leg without V2 admission for %q",
			billing.ErrPostingOwnershipFence, sealed.CallID.String())
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode V2 call-leg usage: %w", err)
	}
	sealedAt := time.Now().UTC()
	var attemptSeq any
	if sealed.AttemptSeq > 0 {
		attemptSeq = sealed.AttemptSeq
	}
	_, err = tx.NewRaw(`INSERT INTO usage_leg_records( usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, attempt_seq, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.Key, sealed.Fingerprint, sealed.CallID.String(), sealed.ALegID, sealed.BLegID, attemptSeq,
		sealed.BackendID, sealed.ProviderID, sealed.ModelID, sealed.StartedAt, sealed.FinishedAt,
		string(sealed.Outcome), string(sealed.Surfaced), string(payload), sealedAt).Exec(ctx)
	if err != nil {
		if isLegAttemptSeqConflict(err) {
			return fmt.Errorf("%w: %w", ErrLegAttemptSequenceConflict, err)
		}
		return fmt.Errorf("billingstore: insert V2 call-leg usage: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO provider_cost_work(usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at) VALUES (?, ?, 'pending', 0, ?, '', ?) ON CONFLICT(usage_leg_key) DO NOTHING`, sealed.Key, sealed.CallID.String(), sealedAt, sealedAt).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: enqueue V2 provider cost work: %w", err)
	}
	if err := s.f3AcquireV2ProviderPinTx(ctx, tx, lockedMarker, handoffAccount, sealed); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit V2 call-leg usage: %w", err)
	}
	return nil
}

// f3V2TerminalLegAllowedTx reports whether a new V2 provider leg may proceed:
// the admitted V2 exposure (open) or a V2-pinned closure must own the call.
// It returns the owning account for provider pin acquisition. Cross-account
// reuse fails closed.
func (s *DurableStore) f3V2TerminalLegAllowedTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, sealed billing.CallLegUsageRecord) (string, bool, error) {
	if marker.State != billing.AccountingCutoverV2Active {
		return "", false, nil
	}
	callID := sealed.CallID
	if err := callID.Validate(); err != nil {
		return "", false, nil
	}
	var exposureAccounts []string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &exposureAccounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("billingstore: V2 terminal leg exposure check: %w", err)
	}
	seen := map[string]struct{}{}
	for _, a := range exposureAccounts {
		trimmed := strings.TrimSpace(a)
		if trimmed == "" {
			continue
		}
		seen[trimmed] = struct{}{}
	}
	if len(seen) > 1 {
		return "", false, fmt.Errorf("%w: call %q bound to multiple accounts",
			billing.ErrPostingOwnershipConflict, callID.String())
	}
	owningAccount := ""
	for a := range seen {
		owningAccount = a
		break
	}
	if owningAccount != "" {
		var status string
		if err := tx.NewRaw(`SELECT status FROM call_exposures WHERE account_id = ? AND call_id = ?`, owningAccount, callID.String()).Scan(ctx, &status); err != nil {
			return "", false, fmt.Errorf("billingstore: V2 terminal leg exposure status: %w", err)
		}
		if strings.TrimSpace(status) != string(billing.ExposureOpen) {
			return "", false, nil
		}
		opKey, err := billing.CustomerPostingOperationKey(owningAccount, callID)
		if err != nil {
			return "", false, nil
		}
		row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
		if err != nil {
			return "", false, err
		}
		if !found {
			return "", false, nil
		}
		pin, err := postingOwnershipRowToPin(row)
		if err != nil {
			return "", false, err
		}
		if pin.Owner != billing.PostingOwnerV2 {
			return "", false, fmt.Errorf("%w: customer pin for %q owned by %q, V2 leg requires V2",
				billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
		}
		return owningAccount, true, nil
	}
	// Fallback: V2-pinned closure owns the call (exposure-less but pinned).
	var closureAccount string
	if err := tx.NewRaw(`SELECT account_id FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &closureAccount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("billingstore: V2 terminal leg closure lookup: %w", err)
	}
	closureAccount = strings.TrimSpace(closureAccount)
	if closureAccount == "" {
		return "", false, nil
	}
	opKey, err := billing.CustomerPostingOperationKey(closureAccount, callID)
	if err != nil {
		return "", false, nil
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return "", false, err
	}
	if pin.Owner != billing.PostingOwnerV2 {
		return "", false, fmt.Errorf("%w: customer pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return closureAccount, true, nil
}

// f3AcquireV2ProviderPinTx acquires the V2 provider pin for one leg atomically
// in the caller's tx (marker lock held, same commit as leg+work enqueue).
// Existing V2 pins are idempotent; conflicting ownership fails closed.
func (s *DurableStore) f3AcquireV2ProviderPinTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, accountID string, sealed billing.CallLegUsageRecord) error {
	if strings.TrimSpace(s.storeID) == "" {
		return fmt.Errorf("%w: store scope is required for V2 provider pin", billing.ErrPostingOwnershipInvalid)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("%w: provider pin account is required", billing.ErrPostingOwnershipInvalid)
	}
	if marker.State != billing.AccountingCutoverV2Active {
		return fmt.Errorf("%w: provider owner V2 not allowed for new in %q",
			billing.ErrPostingOwnershipFence, string(marker.State))
	}
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID,
		ALegID: sealed.ALegID, BillingCallID: sealed.CallID.String(), BLegID: sealed.BLegID,
	}
	opKey, err := billing.ProviderPostingOperationKey(s.storeID, accountID, sealed.CallID, subject)
	if err != nil {
		return err
	}
	if row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, opKey); err != nil {
		return err
	} else if found {
		pin, err := postingOwnershipRowToPin(row)
		if err != nil {
			return err
		}
		if pin.Owner != billing.PostingOwnerV2 {
			return fmt.Errorf("%w: provider pin for %q owned by %q, V2 requires V2",
				billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
		}
		return nil
	}
	payload, err := json.Marshal(subject)
	if err != nil {
		return fmt.Errorf("%w: pin subject encode: %v", billing.ErrPostingOwnershipInvalid, err)
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), opKey, accountID, sealed.CallID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), string(payload), billing.PostingOwnerV2, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: acquire V2 provider pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: V2 provider pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return err
	}
	if pin.Owner != billing.PostingOwnerV2 {
		return fmt.Errorf("%w: provider pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return nil
}
