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
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	usageCallClaimPending           = "pending"
	usageCallClaimClaimed           = "claimed"
	usageCallClaimProcessed         = "processed"
	usageCallClaimReconcileRequired = "reconcile_required"
	completeCallClaimLease          = 2 * time.Minute
	completeCallIncompleteYield     = time.Second
	completeCallClaimBaseRetry      = time.Second
	completeCallClaimMaxRetry       = time.Hour
	completeCallClaimMaxAttempts    = 20
)

type rawQueryDB interface {
	NewRaw(query string, args ...any) *bun.RawQuery
}

// AppendCallUsage is the DurableStore's current-record persistence API. It backs
// billing.TerminalUsageSink (AppendCall) for the composed runtime as well as
// historical outbox drain/storage. Pre-active V1 and draining classified
// handoff keep their behavior; a freshly admitted V2 exposure+pin routes to
// the V2 owner in the same tx (R1), never reclassifying from a fresh global
// marker read.
func (s *DurableStore) AppendCallUsage(ctx context.Context, record billing.CallUsageRecord) error {
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() error {
		return s.appendCallUsageAttempt(ctx, record)
	})
}

func (s *DurableStore) appendCallUsageAttempt(ctx context.Context, record billing.CallUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin call usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker before gate/insert so activation
	// serializes with new work. Replay (existing row) returns without new
	// effects; new appends gate on the locked snapshot. F2A captures the
	// snapshot for terminal handoff (draining allows pre-boundary admitted
	// calls).
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("billingstore: lock call usage cutover: %w", err)
	}
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_call_records WHERE usage_call_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing call usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit call usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup call usage: %w", err)
	}
	// B2a new-work gate: ordinary V1 appends are fenced in draining/active.
	// Exact replay above remains allowed per B1. Uses the tx-scoped gate so
	// concurrent appends do not deadlock (C-6 certification).
	// F2A terminal handoff: draining rejects genuinely new calls, but terminal
	// AppendCallUsage for a pre-boundary admitted/pinned call is allowed.
	// v2_active never allows new V1 terminal handoff (only exact replay above).
	// R1: the production TerminalUsageSink (AppendCall) routes freshly admitted
	// V2 calls through this same path. When the V1 gate fences, a V2-admitted
	// exposure+pin proves the durable posting owner selected at admission; the
	// same-tx V2 insert below preserves that owner without reclassifying the
	// call from a fresh global marker read.
	if err := s.cutoverGateForNewV1Tx(ctx, tx); err != nil {
		gateErr := err
		if allowed, herr := s.f2aTerminalClosureAllowedTx(ctx, tx, lockedMarker, sealed); herr != nil {
			_ = tx.Rollback()
			return herr
		} else if allowed {
			// Allowed: pre-boundary admitted/pinned call terminalizes in draining.
			// Fall through to insert below (same tx, marker lock held, append race
			// with Begin serialized via F1).
		} else if v2allowed, verr := s.f3V2TerminalClosureAllowedTx(ctx, tx, lockedMarker, sealed); verr != nil {
			_ = tx.Rollback()
			return verr
		} else if v2allowed {
			// Allowed: freshly admitted V2 call terminalizes in v2_active via
			// its durable admitted exposure+pin. Fall through to the same
			// insert below; provider work/pin arrives atomically with the leg.
		} else {
			_ = tx.Rollback()
			return gateErr
		}
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode call usage: %w", err)
	}
	expectedJSON, err := json.Marshal(sealed.ExpectedBLegIDs)
	if err != nil {
		return fmt.Errorf("billingstore: encode expected B-leg IDs: %w", err)
	}
	sealedAt := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO usage_call_records( usage_call_key, fingerprint, call_id, account_id, a_leg_id, session_id, started_at, finished_at, outcome, expected_b_leg_ids, payload_json, sealed_at, claim_status, claim_attempt_count, next_claim_at, last_claim_error ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.Key, sealed.Fingerprint, sealed.CallID.String(), sealed.AccountID, sealed.ALegID, sealed.SessionID,
		sealed.StartedAt, sealed.FinishedAt, string(sealed.Outcome), string(expectedJSON), string(payload), sealedAt,
		usageCallClaimPending, 0, sealedAt, "").Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert call usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit call usage: %w", err)
	}
	return nil
}

// f2aTerminalClosureAllowedTx reports whether a new call-closure append may
// proceed as terminal handoff in draining for a pre-boundary admitted/pinned
// call. v2_active never allows new V1 handoff (only exact replay, handled by
// the caller before the gate). Draining allows only when an open admitted
// exposure exists for the same account/call (proves pre-boundary admission)
// and a V1 customer pin exists or is healed in the same tx (bypassing the
// new-work fence as pre-existing ownership, not new post-boundary work).
// Cross-account reuse (same call bound to a different account) fails closed
// with conflict. Calls/legs without pre-boundary admission/pin remain fenced
// (false, nil preserves the caller's gate error). Same tx observes the locked
// marker snapshot; healing pin + closure commit atomically.
func (s *DurableStore) f2aTerminalClosureAllowedTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, sealed billing.CallUsageRecord) (bool, error) {
	if marker.State != billing.AccountingCutoverV1Draining {
		return false, nil
	}
	accountID := strings.TrimSpace(sealed.AccountID)
	callID := sealed.CallID
	if accountID == "" {
		return false, nil
	}
	if err := callID.Validate(); err != nil {
		return false, nil
	}
	// Cross-account isolation: same call bound to a different account rejects.
	var otherAccounts []string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &otherAccounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("billingstore: cross-account exposure check: %w", err)
	}
	for _, other := range otherAccounts {
		if strings.TrimSpace(other) != "" && strings.TrimSpace(other) != accountID {
			return false, fmt.Errorf("%w: call %q bound to account %q, claimant %q",
				billing.ErrPostingOwnershipConflict, callID.String(), strings.TrimSpace(other), accountID)
		}
	}
	// Pre-boundary admission proof: open exposure for same account/call.
	var exposureStatus string
	if err := tx.NewRaw(`SELECT status FROM call_exposures WHERE account_id = ? AND call_id = ?`, accountID, callID.String()).Scan(ctx, &exposureStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("billingstore: terminal exposure lookup: %w", err)
	}
	if strings.TrimSpace(exposureStatus) != string(billing.ExposureOpen) {
		return false, nil
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		return false, nil
	}
	if row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey); err != nil {
		return false, err
	} else if found {
		pin, err := postingOwnershipRowToPin(row)
		if err != nil {
			return false, err
		}
		if pin.Owner != billing.PostingOwnerV1 {
			return false, fmt.Errorf("%w: customer pin for %q owned by %q",
				billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
		}
		return true, nil
	}
	// Heal legacy open exposure without pin: acquire V1 pinned pin with current
	// draining epoch in the same tx (pre-existing exposure proves admission).
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if strings.TrimSpace(s.storeID) == "" {
		return false, nil
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, accountID, callID.String(), "", "", "", "", "", billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return false, fmt.Errorf("billingstore: heal terminal customer pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("%w: terminal customer pin unavailable after heal", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return false, err
	}
	if pin.Owner != billing.PostingOwnerV1 {
		return false, fmt.Errorf("%w: customer pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return true, nil
}

func (s *DurableStore) ListCallUsage(ctx context.Context, accountID string) ([]billing.CallUsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	accountID = strings.TrimSpace(accountID)
	var payloads []string
	var err error
	if accountID == "" {
		err = s.db.NewRaw(`SELECT payload_json FROM usage_call_records ORDER BY sealed_at`).Scan(ctx, &payloads)
	} else {
		err = s.db.NewRaw(`SELECT payload_json FROM usage_call_records WHERE account_id = ? ORDER BY sealed_at`, accountID).Scan(ctx, &payloads)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list call usage: %w", err)
	}
	out := make([]billing.CallUsageRecord, 0, len(payloads))
	for _, payload := range payloads {
		var record billing.CallUsageRecord
		if err := json.Unmarshal([]byte(payload), &record); err != nil {
			return nil, fmt.Errorf("billingstore: decode call usage: %w", err)
		}
		if err := billing.CheckCallUsageReplay(record, record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *DurableStore) ListCallLegUsage(ctx context.Context, callID billing.BillingCallID) ([]billing.CallLegUsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return nil, err
	}
	return s.loadCallLegUsageByCall(ctx, s.db, callID)
}

func (s *DurableStore) GetCallUsage(ctx context.Context, callID billing.BillingCallID) (billing.CallUsageRecord, error) {
	if s == nil || s.db == nil {
		return billing.CallUsageRecord{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallUsageRecord{}, err
	}
	return s.loadCallUsage(ctx, s.db, callID)
}

func (s *DurableStore) ClaimCompleteCalls(ctx context.Context, limit int) ([]billing.CompleteCall, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	// B2a+F3 claim eligibility: v2_active withholds V1 but allows V2-pinned
	// work. Draining filters to V1-pinned work inside
	// claimCompleteCallsFromIDs (withhold unpinned). Pre-drain preserves
	// ordinary claims. The per-ID check below enforces owner fencing; no
	// blanket empty return here so V2 calls remain claimable in active.
	if ctx == nil {
		return nil, fmt.Errorf("billingstore: nil context")
	}
	if limit <= 0 {
		limit = 32
	}
	maxCandidates := max(limit*8, 256)
	out := make([]billing.CompleteCall, 0, limit)
	// Fair-progress scan: eligible rows order by (next_claim_at, sealed_at,
	// call_id) with a matching keyset cursor. Every incomplete row the worker
	// touches is durably deferred to now+1s, so it sorts behind never-deferred
	// rows on the next invocation. Untouched rows behind a >page incomplete
	// prefix therefore surface first on a later scan even when the previous
	// scan duration exceeded the 1s yield window. This matches the existing
	// (claim_status, next_claim_at, sealed_at, call_id) index on both
	// dialects; no schema change and no extended deadline.
	var afterNext time.Time
	var afterSealed time.Time
	var afterCallID string
	scanned := 0
	now := s.claimNow()
	staleBefore := now.Add(-completeCallClaimLease)
	for scanned < maxCandidates && len(out) < limit {
		pageSize := limit
		if remaining := maxCandidates - scanned; remaining < pageSize {
			pageSize = remaining
		}
		if pageSize <= 0 {
			break
		}
		type pendingCall struct {
			CallID      string
			SealedAt    time.Time
			NextClaimAt time.Time
		}
		var page []pendingCall
		var err error
		if afterCallID == "" {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at, next_claim_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
 ORDER BY next_claim_at, sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore, pageSize,
			).Scan(ctx, &page)
		} else {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at, next_claim_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
   AND (next_claim_at > ? OR (next_claim_at = ? AND sealed_at > ?) OR (next_claim_at = ? AND sealed_at = ? AND call_id > ?))
 ORDER BY next_claim_at, sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore,
				afterNext, afterNext, afterSealed, afterNext, afterSealed, afterCallID, pageSize,
			).Scan(ctx, &page)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, 0, len(page))
		for _, row := range page {
			ids = append(ids, row.CallID)
			afterNext = row.NextClaimAt
			afterSealed = row.SealedAt
			afterCallID = row.CallID
		}
		scanned += len(page)
		batch, err := s.claimCompleteCallsFromIDs(ctx, ids, limit-len(out), claimCompleteOpts{WorkerBatch: true, Now: now})
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

type claimCompleteOpts struct {
	WorkerBatch bool
	Now         time.Time
}

func (s *DurableStore) claimCompleteCallsFromIDs(ctx context.Context, ids []string, limit int, opts claimCompleteOpts) ([]billing.CompleteCall, error) {
	if limit <= 0 {
		return nil, nil
	}
	out := make([]billing.CompleteCall, 0, limit)
	for _, raw := range ids {
		if len(out) >= limit {
			break
		}
		callID, err := billing.ParseBillingCallID(raw)
		if err != nil {
			return nil, err
		}
		// B2a draining eligibility: withhold unpinned work (classified
		// before claim or withheld). No extra write occurs here.
		if allowed, err := s.drainingClaimEligible(ctx, callID); err != nil {
			return nil, err
		} else if !allowed {
			continue
		}
		complete, err := s.claimCompleteCallWithOpts(ctx, callID, opts)
		if err != nil {
			if errors.Is(err, billing.ErrCallIncomplete) {
				if opts.WorkerBatch {
					if deferErr := s.deferIncompleteCall(ctx, callID, opts.Now); deferErr != nil {
						return nil, errors.Join(err, deferErr)
					}
				}
				continue
			}
			if errors.Is(err, billing.ErrCallClaimConflict) {
				continue
			}
			return nil, err
		}
		out = append(out, complete)
	}
	return out, nil
}

func (s *DurableStore) deferIncompleteCall(ctx context.Context, callID billing.BillingCallID, now time.Time) error {
	if now.IsZero() {
		now = s.claimNow()
	}
	next := now.Add(completeCallIncompleteYield)
	_, err := s.db.NewRaw(
		`UPDATE usage_call_records SET next_claim_at = ? WHERE call_id = ? AND claim_status = ? AND next_claim_at <= ?`,
		next, callID.String(), usageCallClaimPending, now,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: defer incomplete call: %w", err)
	}
	return nil
}

func (s *DurableStore) GetCallExposure(ctx context.Context, callID billing.BillingCallID) (billing.CallExposure, error) {
	if s == nil || s.db == nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallExposure{}, err
	}
	var row exposureRow
	if err := s.db.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallExposure{}, billing.ErrExposureNotFound
		}
		return billing.CallExposure{}, err
	}
	return exposureFromRow(row)
}

func (s *DurableStore) RetryCompleteCall(ctx context.Context, callID billing.BillingCallID, code string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	now := time.Now().UTC()
	if code == "settlement_reconcile_required" {
		_, err := s.db.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = NULL, last_claim_error = ?, next_claim_at = ? WHERE call_id = ? AND claim_status = ?`,
			usageCallClaimReconcileRequired, code, now, callID.String(), usageCallClaimClaimed,
		).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: mark complete-call reconcile required: %w", err)
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin complete-call retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attempts int
	if err := tx.NewRaw(`UPDATE usage_call_records SET claim_attempt_count = claim_attempt_count + 1, last_claim_error = ?, claimed_at = NULL WHERE call_id = ? AND claim_status = ? RETURNING claim_attempt_count`, code, callID.String(), usageCallClaimClaimed).Scan(ctx, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("billingstore: retry complete-call claim: %w", err)
	}
	status := usageCallClaimPending
	nextAt := now.Add(retryBackoffDelay(completeCallClaimBaseRetry, completeCallClaimMaxRetry, attempts))
	if attempts >= completeCallClaimMaxAttempts {
		status = usageCallClaimReconcileRequired
		nextAt = now
	}
	if _, err := tx.NewRaw(`UPDATE usage_call_records SET claim_status = ?, next_claim_at = ? WHERE call_id = ? AND claim_attempt_count = ?`, status, nextAt, callID.String(), attempts).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: schedule complete-call retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit complete-call retry: %w", err)
	}
	return nil
}

func (s *DurableStore) ClaimCompleteCall(ctx context.Context, callID billing.BillingCallID) (billing.CompleteCall, error) {
	// B2a single-claim fence: draining requires a V1 pin, v2_active forbids
	// V1 claims. Pre-drain states preserve ordinary claims.
	if allowed, err := s.drainingClaimEligible(ctx, callID); err != nil {
		return billing.CompleteCall{}, err
	} else if !allowed {
		return billing.CompleteCall{}, fmt.Errorf("%w: V1 claim for %s not eligible under cutover",
			billing.ErrAccountingCutoverFence, callID.String())
	}
	return s.claimCompleteCallWithOpts(ctx, callID, claimCompleteOpts{Now: s.claimNow()})
}

// drainingClaimEligible reports whether a worker claim may proceed.
// Missing markers preserve legacy defaults (allowed). Draining requires a V1
// pin (withhold unpinned); v2_active forbids V1 claims but allows V2-pinned
// work (F3 usable V2 pipeline).
func (s *DurableStore) drainingClaimEligible(ctx context.Context, callID billing.BillingCallID) (bool, error) {
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// Load errors other than NotFound fail closed only for context;
		// otherwise preserve ordinary claims when the marker is unavailable.
		return true, nil
	}
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return true, nil
	case billing.AccountingCutoverV2Active:
		// F3: V2-pinned calls remain claimable; V1 and unpinned withhold.
		accountID := s.accountForCall(ctx, callID.String())
		if strings.TrimSpace(accountID) == "" {
			return false, nil
		}
		opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
		if err != nil {
			return false, nil
		}
		pin, err := s.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
		if err != nil {
			return false, nil
		}
		return pin.Owner == billing.PostingOwnerV2, nil
	case billing.AccountingCutoverV1Draining:
		accountID := s.accountForCall(ctx, callID.String())
		if strings.TrimSpace(accountID) == "" {
			return false, nil
		}
		allowed, err := s.isV1ClaimAllowedUnderMarker(ctx, accountID, callID)
		if err != nil {
			return false, nil
		}
		return allowed, nil
	default:
		return false, nil
	}
}

func (s *DurableStore) claimCompleteCallWithOpts(ctx context.Context, callID billing.BillingCallID, opts claimCompleteOpts) (billing.CompleteCall, error) {
	var zero billing.CompleteCall
	if s == nil || s.db == nil {
		return zero, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return zero, err
	}
	if opts.Now.IsZero() {
		opts.Now = s.claimNow()
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() (billing.CompleteCall, error) {
		return s.claimCompleteCallAttempt(ctx, callID, opts)
	})
}

func (s *DurableStore) claimCompleteCallAttempt(ctx context.Context, callID billing.BillingCallID, opts claimCompleteOpts) (billing.CompleteCall, error) {
	var zero billing.CompleteCall
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("billingstore: begin complete-call claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	closure, err := s.loadCallUsage(ctx, tx, callID)
	if errors.Is(err, ErrUsageRecordNotFound) {
		return zero, billing.ErrCallIncomplete
	}
	if err != nil {
		return zero, err
	}
	legs, err := s.loadCallLegUsageByCall(ctx, tx, callID)
	if err != nil {
		return zero, err
	}
	complete, err := billing.JoinCompleteCall(closure, legs)
	if err != nil {
		return zero, err
	}
	now := opts.Now
	staleBefore := now.Add(-completeCallClaimLease)
	var claimResult sql.Result
	if opts.WorkerBatch {
		claimResult, err = tx.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = ? WHERE call_id = ? AND ( (claim_status = ? AND next_claim_at <= ?) OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?) )`,
			usageCallClaimClaimed, now, callID.String(),
			usageCallClaimPending, now,
			usageCallClaimClaimed, staleBefore,
		).Exec(ctx)
	} else {
		claimResult, err = tx.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = ? WHERE call_id = ? AND ( claim_status = ? OR claim_status = ? OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?) )`,
			usageCallClaimClaimed, now, callID.String(),
			usageCallClaimPending,
			usageCallClaimReconcileRequired,
			usageCallClaimClaimed, staleBefore,
		).Exec(ctx)
	}
	if err != nil {
		return zero, fmt.Errorf("billingstore: mark complete call claimed: %w", err)
	}
	count, err := claimResult.RowsAffected()
	if err != nil {
		return zero, fmt.Errorf("billingstore: complete-call claim rows affected: %w", err)
	}
	if count != 1 {
		var status string
		if err := tx.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
			return zero, fmt.Errorf("billingstore: inspect complete-call claim: %w", err)
		}
		if status == usageCallClaimProcessed {
			if err := tx.Commit(); err != nil {
				return zero, fmt.Errorf("billingstore: commit processed-call replay: %w", err)
			}
			return complete, nil
		}
		return zero, billing.ErrCallClaimConflict
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("billingstore: commit complete-call claim: %w", err)
	}
	return complete, nil
}

func (s *DurableStore) loadCallUsage(ctx context.Context, q rawQueryDB, callID billing.BillingCallID) (billing.CallUsageRecord, error) {
	var payload string
	if err := q.NewRaw(`SELECT payload_json FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.CallUsageRecord{}, err
	}
	var record billing.CallUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.CallUsageRecord{}, fmt.Errorf("billingstore: decode call usage: %w", err)
	}
	if err := billing.CheckCallUsageReplay(record, record); err != nil {
		return billing.CallUsageRecord{}, err
	}
	return record, nil
}

func (s *DurableStore) loadCallLegUsageByCall(ctx context.Context, q rawQueryDB, callID billing.BillingCallID) ([]billing.CallLegUsageRecord, error) {
	var payloads []string
	err := q.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE call_id = ?`, callID.String()).Scan(ctx, &payloads)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list call-leg usage: %w", err)
	}
	out := make([]billing.CallLegUsageRecord, 0, len(payloads))
	for _, payload := range payloads {
		var record billing.CallLegUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(payload), &record); unmarshalErr != nil {
			return nil, fmt.Errorf("billingstore: decode call-leg usage: %w", unmarshalErr)
		}
		out = append(out, record)
	}
	return out, nil
}

// ClaimCompleteCallsWithCutover atomically claims complete calls and issues
// current-marker tokens as part of each claimed item. Claim status transition,
// pin acquisition/validation (including first-acquisition pin creation), and
// current-marker authority issuance occur in one transaction holding the
// per-store marker lock, so a marker transition between claim attempt and
// return cannot yield a token authorized for the wrong epoch/state. It never
// swallows operational, cancellation or malformed errors into nil/default
// tokens. Ineligible work (unpinned in draining, V1 in active) is withheld
// without error and without committing a claim; first-acquisition work with
// no pin yet acquires its pin atomically and returns a nonempty fully
// validated token. Draining pinned V1 and shadow V1 receive current-epoch
// renewal without rewriting owner.
func (s *DurableStore) ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]billing.ClaimedCompleteCall, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 32
	}
	// Enumerate candidates without claiming (bounded, deterministic). Each
	// candidate is then claimed atomically with its token below; ineligible
	// candidates are withheld without effects.
	candidates, err := s.listCompleteCallCandidates(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]billing.ClaimedCompleteCall, 0, len(candidates))
	for _, callID := range candidates {
		if len(out) >= limit {
			break
		}
		claimed, ok, deferIncomplete, derr := s.claimCompleteCallWithCutoverAtomic(ctx, callID)
		if derr != nil {
			return nil, derr
		}
		if deferIncomplete {
			_ = s.deferIncompleteCall(ctx, callID, s.claimNow())
			continue
		}
		if !ok {
			continue
		}
		out = append(out, claimed)
	}
	return out, nil
}

// listCompleteCallCandidates returns bounded pending/stale call IDs without
// mutating claim state. The atomic claim below revalidates eligibility under
// the marker lock. It uses the same fair (next_claim_at, sealed_at, call_id)
// ordering as ClaimCompleteCalls so deferred incomplete rows sort behind
// never-deferred rows on later scans.
func (s *DurableStore) listCompleteCallCandidates(ctx context.Context, limit int) ([]billing.BillingCallID, error) {
	maxCandidates := max(limit*8, 256)
	out := make([]billing.BillingCallID, 0, limit)
	var afterNext time.Time
	var afterSealed time.Time
	var afterCallID string
	scanned := 0
	now := s.claimNow()
	staleBefore := now.Add(-completeCallClaimLease)
	for scanned < maxCandidates && len(out) < limit {
		pageSize := limit
		if remaining := maxCandidates - scanned; remaining < pageSize {
			pageSize = remaining
		}
		if pageSize <= 0 {
			break
		}
		type pendingCall struct {
			CallID      string
			SealedAt    time.Time
			NextClaimAt time.Time
		}
		var page []pendingCall
		var err error
		if afterCallID == "" {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at, next_claim_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
 ORDER BY next_claim_at, sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore, pageSize,
			).Scan(ctx, &page)
		} else {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at, next_claim_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
   AND (next_claim_at > ? OR (next_claim_at = ? AND sealed_at > ?) OR (next_claim_at = ? AND sealed_at = ? AND call_id > ?))
 ORDER BY next_claim_at, sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore,
				afterNext, afterNext, afterSealed, afterNext, afterSealed, afterCallID, pageSize,
			).Scan(ctx, &page)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			afterNext = row.NextClaimAt
			afterSealed = row.SealedAt
			afterCallID = row.CallID
			callID, perr := billing.ParseBillingCallID(row.CallID)
			if perr != nil {
				return nil, perr
			}
			out = append(out, callID)
			if len(out) >= limit*2 {
				break
			}
		}
		scanned += len(page)
		if len(page) < pageSize {
			break
		}
	}
	if len(out) > limit*2 {
		out = out[:limit*2]
	}
	return out, nil
}

// claimCompleteCallWithCutoverAtomic claims one call and issues its
// current-marker token in a single transaction holding the marker lock.
// ok=false means withheld (ineligible) without effects. deferIncomplete
// signals an incomplete call the caller should defer without error.
func (s *DurableStore) claimCompleteCallWithCutoverAtomic(ctx context.Context, callID billing.BillingCallID) (billing.ClaimedCompleteCall, bool, bool, error) {
	var zero billing.ClaimedCompleteCall
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, false, fmt.Errorf("billingstore: begin complete-call cutover claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// R4 atomic: lock the marker first so eligibility, claim, pin and token
	// observe one snapshot; concurrent activation serializes here.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return zero, false, false, err
	}
	closure, err := s.loadCallUsage(ctx, tx, callID)
	if errors.Is(err, ErrUsageRecordNotFound) {
		_ = tx.Rollback()
		return zero, false, true, nil
	}
	if err != nil {
		return zero, false, false, err
	}
	legs, err := s.loadCallLegUsageByCall(ctx, tx, callID)
	if err != nil {
		return zero, false, false, err
	}
	complete, err := billing.JoinCompleteCall(closure, legs)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, billing.ErrCallIncomplete) {
			return zero, false, true, nil
		}
		return zero, false, false, err
	}
	opKey, err := billing.CustomerPostingOperationKey(closure.AccountID, closure.CallID)
	if err != nil {
		return zero, false, false, err
	}
	// Pin handling under the same lock: existing pins renew to the locked
	// marker; fresh pins are acquired atomically when the locked state
	// permits new work; otherwise withhold without claiming.
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return zero, false, false, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return zero, false, false, err
		}
		if !isCustomerPinEligibleForClaim(lockedMarker, pin) {
			_ = tx.Rollback()
			return zero, false, false, nil
		}
	} else {
		owner := lockedMarker.ActivePostingOwner
		if !billing.IsPostingOwnerAllowedForNew(lockedMarker.State, owner) {
			_ = tx.Rollback()
			return zero, false, false, nil
		}
		if err := insertCustomerPinTx(ctx, tx, s, lockedMarker, closure.AccountID, closure.CallID, opKey, owner); err != nil {
			return zero, false, false, err
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey)
		if rerr != nil {
			return zero, false, false, rerr
		}
		if !refound {
			return zero, false, false, fmt.Errorf("%w: customer pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
		}
		pin, err = postingOwnershipRowToPin(rerow)
		if err != nil {
			return zero, false, false, err
		}
		if pin.Owner != owner {
			_ = tx.Rollback()
			return zero, false, false, nil
		}
	}
	// Claim the call row in the same tx.
	now := s.claimNow()
	staleBefore := now.Add(-completeCallClaimLease)
	claimResult, err := tx.NewRaw(
		`UPDATE usage_call_records SET claim_status = ?, claimed_at = ? WHERE call_id = ? AND ( (claim_status = ? AND next_claim_at <= ?) OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?) )`,
		usageCallClaimClaimed, now, callID.String(),
		usageCallClaimPending, now,
		usageCallClaimClaimed, staleBefore,
	).Exec(ctx)
	if err != nil {
		return zero, false, false, fmt.Errorf("billingstore: mark complete call claimed: %w", err)
	}
	count, err := claimResult.RowsAffected()
	if err != nil {
		return zero, false, false, fmt.Errorf("billingstore: complete-call claim rows affected: %w", err)
	}
	if count != 1 {
		var status string
		if serr := tx.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &status); serr != nil {
			return zero, false, false, fmt.Errorf("billingstore: inspect complete-call claim: %w", serr)
		}
		if status == usageCallClaimProcessed {
			// Already processed: return replay with current token (no new
			// effects beyond the read-only commit).
			tok, terr := billing.CutoverClaimTokenForPinAtMarker(pin, lockedMarker)
			if terr != nil {
				return zero, false, false, terr
			}
			if verr := billing.ValidateCustomerSettlementClaim(tok, closure.AccountID, closure.CallID); verr != nil {
				return zero, false, false, verr
			}
			if err := tx.Commit(); err != nil {
				return zero, false, false, fmt.Errorf("billingstore: commit processed-call cutover replay: %w", err)
			}
			return billing.ClaimedCompleteCall{Call: complete, Claim: tok}, true, false, nil
		}
		_ = tx.Rollback()
		return zero, false, false, nil
	}
	// Issue the current-marker token from the same locked snapshot.
	tok, err := billing.CutoverClaimTokenForPinAtMarker(pin, lockedMarker)
	if err != nil {
		return zero, false, false, err
	}
	if verr := billing.ValidateCustomerSettlementClaim(tok, closure.AccountID, closure.CallID); verr != nil {
		return zero, false, false, verr
	}
	claimed := billing.ClaimedCompleteCall{Call: complete, Claim: tok}
	if verr := claimed.Validate(); verr != nil {
		return zero, false, false, verr
	}
	if err := tx.Commit(); err != nil {
		return zero, false, false, fmt.Errorf("billingstore: commit complete-call cutover claim: %w", err)
	}
	return claimed, true, false, nil
}

func isCustomerPinEligibleForClaim(marker billing.AccountingCutoverMarker, pin billing.PostingPin) bool {
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return pin.Owner == billing.PostingOwnerV1
	case billing.AccountingCutoverV1Draining:
		return pin.Owner == billing.PostingOwnerV1
	case billing.AccountingCutoverV2Active:
		return pin.Owner == billing.PostingOwnerV2
	default:
		return false
	}
}

func insertCustomerPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID, opKey, owner string) error {
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, accountID, callID.String(), "", "", "", "", "", owner, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: acquire customer cutover pin: %w", err)
	}
	return nil
}
