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

// ErrLegAttemptSequenceConflict identifies duplicate positive sequences within one BillingCallID; legacy NULL values remain outside it.
var ErrLegAttemptSequenceConflict = errors.New("billingstore: call-leg attempt sequence conflict")

// AppendCallLegUsage is the DurableStore's current-record persistence API. It
// backs billing.TerminalUsageSink (AppendLeg) for the composed runtime as well
// as historical outbox drain/storage. Pre-active V1 and draining classified
// handoff keep their behavior; a freshly admitted V2 exposure+pin routes to
// the V2 owner with atomic provider work/pin in the same tx (R1).
func (s *DurableStore) AppendCallLegUsage(ctx context.Context, record billing.CallLegUsageRecord) error {
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
		return s.appendCallLegUsageAttempt(ctx, record)
	})
}

func (s *DurableStore) appendCallLegUsageAttempt(ctx context.Context, record billing.CallLegUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin call-leg usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker before gate/enqueue so activation
	// serializes with new provider work. F2A captures the snapshot for
	// terminal handoff and atomic provider pin acquisition.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("billingstore: lock call-leg cutover: %w", err)
	}
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallLegUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing call-leg usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallLegUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit call-leg usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup call-leg usage: %w", err)
	}
	// B2a new-work gate: ordinary V1 leg appends (and their provider-cost
	// work enqueue) are fenced in draining/active. Exact replay above
	// remains allowed per B1. Uses the tx-scoped gate so concurrent appends
	// do not deadlock holding a write tx while querying on another pool
	// connection (C-6 crash/concurrency certification).
	// F2A terminal handoff: a provider leg first materializing at terminal is
	// owned by the pre-boundary admitted V1 call; enqueue/pin provider work
	// without treating it as new post-boundary work. v2_active never allows
	// new V1 handoff (only replay above).
	// R1: the production TerminalUsageSink (AppendLeg) routes freshly admitted
	// V2 legs through this same path. When the V1 gate fences, the durable
	// admitted V2 exposure+pin selects the V2 owner (never a fresh global
	// reclassification); the same-tx insert+enqueue+pin below preserves it.
	handoffAccount := ""
	handoffOwner := ""
	if err := s.cutoverGateForNewV1Tx(ctx, tx); err != nil {
		gateErr := err
		account, allowed, herr := s.f2aTerminalLegAllowedTx(ctx, tx, lockedMarker, sealed)
		if herr != nil {
			_ = tx.Rollback()
			return herr
		}
		if allowed {
			handoffAccount = account
			handoffOwner = billing.PostingOwnerV1
			// Fall through to insert+enqueue+pin below (same tx, marker lock held).
		} else if v2account, v2allowed, verr := s.f3V2TerminalLegAllowedTx(ctx, tx, lockedMarker, sealed); verr != nil {
			_ = tx.Rollback()
			return verr
		} else if v2allowed {
			handoffAccount = v2account
			handoffOwner = billing.PostingOwnerV2
			// Fall through to insert+enqueue+V2-pin below (same tx).
		} else {
			_ = tx.Rollback()
			return gateErr
		}
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode call-leg usage: %w", err)
	}
	sealedAt := time.Now().UTC()
	// Pre-fix legacy rows have no sequence (attempt_seq NULL); corrected
	// records persist the exact positive attempt sequence explicitly.
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
		return fmt.Errorf("billingstore: insert call-leg usage: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO provider_cost_work(usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at) VALUES (?, ?, 'pending', 0, ?, '', ?) ON CONFLICT(usage_leg_key) DO NOTHING`, sealed.Key, sealed.CallID.String(), sealedAt, sealedAt).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: enqueue provider cost work: %w", err)
	}
	// F2A: provider pin acquisition atomic with work enqueue and marker lock
	// in the same tx; exact replay safe (replay returns before gate above).
	// Handoff path requires the pin (terminal leg owned by admitted V1 call,
	// or R1 admitted V2 call with a V2 pin); pre-drain path acquires
	// best-effort when the owning account is already derivable (leg-first
	// appends without exposure/closure leave pinning to drain classification).
	if handoffAccount != "" {
		if handoffOwner == billing.PostingOwnerV2 {
			if err := s.f3AcquireV2ProviderPinTx(ctx, tx, lockedMarker, handoffAccount, sealed); err != nil {
				return err
			}
		} else if err := s.f2aAcquireProviderPinTx(ctx, tx, lockedMarker, handoffAccount, sealed); err != nil {
			return err
		}
	} else if lockedMarker.State == billing.AccountingCutoverV1Active || lockedMarker.State == billing.AccountingCutoverV2Shadow {
		if account := s.f2aLegOwningAccountBestEffortTx(ctx, tx, sealed.CallID.String()); strings.TrimSpace(account) != "" {
			// Best-effort: owner conflicts fail closed; missing lineage skips
			// (classification pins after closure/exposure lands).
			if err := s.f2aAcquireProviderPinTx(ctx, tx, lockedMarker, account, sealed); err != nil {
				// Cross-owner conflict fails the append; unclassifiable lineage
				// (invalid key) skips to preserve leg-first ordering.
				if errors.Is(err, billing.ErrPostingOwnershipConflict) || errors.Is(err, billing.ErrPostingOwnershipFence) {
					return err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit call-leg usage: %w", err)
	}
	return nil
}

// f2aTerminalLegAllowedTx reports whether a new provider leg may proceed as
// terminal handoff in draining for a pre-boundary admitted/pinned call. It
// returns the owning account for provider pin acquisition. v2_active never
// allows new V1 handoff. Draining allows only when an open admitted exposure
// (preferred, proves admission before stream) or a classified closure+pin
// owns the call; provider leg first materializing at terminal is then owned
// by that admitted V1 call. Cross-account reuse fails closed with conflict.
// Without pre-boundary admission/pin it returns false (preserves gate fence).
func (s *DurableStore) f2aTerminalLegAllowedTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, sealed billing.CallLegUsageRecord) (string, bool, error) {
	if marker.State != billing.AccountingCutoverV1Draining {
		return "", false, nil
	}
	callID := sealed.CallID
	if err := callID.Validate(); err != nil {
		return "", false, nil
	}
	// Cross-call isolation: same call bound to multiple accounts rejects.
	var exposureAccounts []string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &exposureAccounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("billingstore: terminal leg exposure check: %w", err)
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
		// Admitted path: require open exposure (terminal before settlement).
		var status string
		if err := tx.NewRaw(`SELECT status FROM call_exposures WHERE account_id = ? AND call_id = ?`, owningAccount, callID.String()).Scan(ctx, &status); err != nil {
			return "", false, fmt.Errorf("billingstore: terminal leg exposure status: %w", err)
		}
		if strings.TrimSpace(status) != string(billing.ExposureOpen) {
			return "", false, nil
		}
		// Verify or heal V1 customer pin (pre-boundary admission proof).
		opKey, err := billing.CustomerPostingOperationKey(owningAccount, callID)
		if err != nil {
			return "", false, nil
		}
		if row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationCustomerSettlement, opKey); err != nil {
			return "", false, err
		} else if found {
			pin, err := postingOwnershipRowToPin(row)
			if err != nil {
				return "", false, err
			}
			if pin.Owner != billing.PostingOwnerV1 {
				return "", false, fmt.Errorf("%w: customer pin for %q owned by %q",
					billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
			}
			return owningAccount, true, nil
		}
		// Heal legacy admission without pin in same tx.
		if strings.TrimSpace(s.storeID) == "" {
			return "", false, nil
		}
		now := time.Now().UTC().UnixNano()
		insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
		if s.db.Dialect().Name() == dialect.SQLite {
			insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		}
		if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, owningAccount, callID.String(), "", "", "", "", "", billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
			return "", false, fmt.Errorf("billingstore: heal terminal leg customer pin: %w", err)
		}
		return owningAccount, true, nil
	}
	// Fallback: classified closure owns the call (exposure-less but pinned).
	var closureAccount string
	if err := tx.NewRaw(`SELECT account_id FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &closureAccount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("billingstore: terminal leg closure lookup: %w", err)
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
	if pin.Owner != billing.PostingOwnerV1 {
		return "", false, fmt.Errorf("%w: customer pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return closureAccount, true, nil
}

// f2aLegOwningAccountBestEffortTx derives the owning account for pre-drain
// provider pin acquisition from exposure (preferred) or closure. Empty when
// neither exists yet (leg-first ordering leaves pinning to classification).
func (s *DurableStore) f2aLegOwningAccountBestEffortTx(ctx context.Context, tx bun.Tx, callIDStr string) string {
	var exposureAccount string
	if err := tx.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ? LIMIT 1`, callIDStr).Scan(ctx, &exposureAccount); err == nil && strings.TrimSpace(exposureAccount) != "" {
		return strings.TrimSpace(exposureAccount)
	}
	var closureAccount string
	if err := tx.NewRaw(`SELECT account_id FROM usage_call_records WHERE call_id = ?`, callIDStr).Scan(ctx, &closureAccount); err == nil && strings.TrimSpace(closureAccount) != "" {
		return strings.TrimSpace(closureAccount)
	}
	return ""
}

// f2aAcquireProviderPinTx acquires the V1 provider pin for one leg atomically
// in the caller's tx (marker lock held, same commit as leg+work enqueue).
// Exact replay safe: existing V1 pins are idempotent; conflicting ownership
// fails closed. Invalid lineage returns the derivation error for the caller
// to classify as skip vs fence.
func (s *DurableStore) f2aAcquireProviderPinTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, accountID string, sealed billing.CallLegUsageRecord) error {
	if strings.TrimSpace(s.storeID) == "" {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("%w: provider pin account is required", billing.ErrPostingOwnershipInvalid)
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
		if pin.Owner != billing.PostingOwnerV1 {
			return fmt.Errorf("%w: provider pin for %q owned by %q",
				billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
		}
		// Draining handoff with V1 owner replays idempotently even when the
		// stored epoch predates the locked marker (stable owner, renewable
		// authority refreshed by drain classification).
		if marker.State == billing.AccountingCutoverV1Draining {
			return nil
		}
		// Pre-drain replay converges without epoch rewrite.
		return nil
	}
	// New pin: marker must permit V1 new, except draining handoff bypasses the
	// B1 new-work fence because the admitted customer pin proves pre-boundary
	// ownership (caller verified). v2_active never reaches here (fenced).
	if marker.State != billing.AccountingCutoverV1Active && marker.State != billing.AccountingCutoverV2Shadow && marker.State != billing.AccountingCutoverV1Draining {
		return fmt.Errorf("%w: provider owner V1 not allowed for new in %q",
			billing.ErrPostingOwnershipFence, string(marker.State))
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
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), opKey, accountID, sealed.CallID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), string(payload), billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: acquire terminal provider pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: terminal provider pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return err
	}
	if pin.Owner != billing.PostingOwnerV1 {
		return fmt.Errorf("%w: provider pin for %q owned by %q",
			billing.ErrPostingOwnershipConflict, opKey, pin.Owner)
	}
	return nil
}

func (s *DurableStore) ListPendingProviderCostWork(ctx context.Context, limit int) ([]billing.ProviderCostWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	// B2a+F3 claim eligibility: v2_active withholds V1 provider work but
	// allows V2-pinned work. The per-item check below enforces owner fencing;
	// no blanket empty return here so V2 legs remain claimable in active.
	if ctx == nil {
		return nil, fmt.Errorf("billingstore: nil context")
	}
	if limit <= 0 {
		limit = 32
	}
	if err := s.pruneProcessedProviderCostWork(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		return nil, err
	}
	type workRow struct {
		CallID    string         `bun:"call_id"`
		AccountID sql.NullString `bun:"account_id"`
		Payload   string         `bun:"payload_json"`
	}
	var rows []workRow
	if err := s.db.NewRaw(`
SELECT w.call_id, c.account_id, l.payload_json
FROM provider_cost_work w
JOIN usage_leg_records l ON l.usage_leg_key = w.usage_leg_key
LEFT JOIN usage_call_records c ON c.call_id = w.call_id
WHERE w.status = 'pending' AND w.next_attempt_at <= ?
ORDER BY w.next_attempt_at, w.updated_at, w.usage_leg_key
LIMIT ?`, time.Now().UTC(), limit).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list pending provider cost work: %w", err)
	}
	out := make([]billing.ProviderCostWork, 0, len(rows))
	for _, row := range rows {
		callID, err := billing.ParseBillingCallID(row.CallID)
		if err != nil {
			return nil, err
		}
		var leg billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &leg); err != nil {
			return nil, fmt.Errorf("billingstore: decode pending provider cost leg: %w", err)
		}
		if err := billing.CheckCallLegUsageReplay(leg, leg); err != nil {
			return nil, err
		}
		accountID := ""
		if row.AccountID.Valid {
			accountID = strings.TrimSpace(row.AccountID.String)
		}
		// F2A fallback: terminal leg may precede closure; derive owning
		// account from admitted exposure when the closure JOIN misses.
		if accountID == "" {
			accountID = s.accountForCall(ctx, row.CallID)
		}
		// B2a draining eligibility: withhold unpinned provider work.
		// Pre-drain states preserve ordinary claims.
		if allowed, err := s.isProviderClaimAllowedUnderMarker(ctx, accountID, callID, leg.BLegID, leg.ALegID); err != nil {
			return nil, err
		} else if !allowed {
			continue
		}
		out = append(out, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
	}
	return out, nil
}

func (s *DurableStore) GetCallLegUsage(ctx context.Context, key string) (billing.CallLegUsageRecord, error) {
	if s == nil || s.db == nil {
		return billing.CallLegUsageRecord{}, fmt.Errorf("billingstore: nil store")
	}
	var payload string
	if err := s.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, strings.TrimSpace(key)).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallLegUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.CallLegUsageRecord{}, err
	}
	var record billing.CallLegUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.CallLegUsageRecord{}, fmt.Errorf("billingstore: decode call-leg usage: %w", err)
	}
	if err := billing.CheckCallLegUsageReplay(record, record); err != nil {
		return billing.CallLegUsageRecord{}, err
	}
	return record, nil
}

// ClaimProviderCostWorkWithCutover atomically lists eligible pending B-legs
// and issues current-marker tokens as part of each claimed item. Pin
// validation/acquisition (including first-acquisition) and current-marker
// authority issuance occur in one transaction holding the per-store marker
// lock, so a marker transition between list and return cannot yield a token
// authorized for the wrong epoch/state. It never swallows operational,
// cancellation or malformed errors. Ineligible work (unpinned in draining,
// V1 in active) is withheld without error; first-acquisition work acquires
// its pin atomically and returns a nonempty fully validated token.
func (s *DurableStore) ClaimProviderCostWorkWithCutover(ctx context.Context, limit int) ([]billing.ClaimedProviderCostWork, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 32
	}
	work, err := s.ListPendingProviderCostWork(ctx, limit*2)
	if err != nil {
		return nil, err
	}
	out := make([]billing.ClaimedProviderCostWork, 0, len(work))
	for _, w := range work {
		if len(out) >= limit {
			break
		}
		claimed, ok, cerr := s.claimProviderWorkWithCutoverAtomic(ctx, w)
		if cerr != nil {
			return nil, cerr
		}
		if !ok {
			continue
		}
		out = append(out, claimed)
	}
	return out, nil
}

// claimProviderWorkWithCutoverAtomic issues the current-marker token for one
// pending B-leg in a single transaction holding the marker lock. Fresh work
// acquires its pin atomically when the locked state permits new work;
// otherwise the item is withheld without effects.
func (s *DurableStore) claimProviderWorkWithCutoverAtomic(ctx context.Context, w billing.ProviderCostWork) (billing.ClaimedProviderCostWork, bool, error) {
	var zero billing.ClaimedProviderCostWork
	sealed, err := w.Leg.Seal()
	if err != nil {
		return zero, false, err
	}
	opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		return zero, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, fmt.Errorf("billingstore: begin provider cutover claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return zero, false, err
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		return zero, false, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return zero, false, err
		}
		if !isProviderPinEligibleForClaim(lockedMarker, pin) {
			_ = tx.Rollback()
			return zero, false, nil
		}
	} else {
		owner := lockedMarker.ActivePostingOwner
		if !billing.IsPostingOwnerAllowedForNew(lockedMarker.State, owner) {
			_ = tx.Rollback()
			return zero, false, nil
		}
		subject := metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: w.AccountID,
			ALegID: sealed.ALegID, BillingCallID: sealed.CallID.String(), BLegID: sealed.BLegID,
		}
		if err := insertProviderPinTx(ctx, tx, s, lockedMarker, w.AccountID, sealed.CallID, subject, opKey, owner); err != nil {
			return zero, false, err
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, opKey)
		if rerr != nil {
			return zero, false, rerr
		}
		if !refound {
			return zero, false, fmt.Errorf("%w: provider pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
		}
		pin, err = postingOwnershipRowToPin(rerow)
		if err != nil {
			return zero, false, err
		}
		if pin.Owner != owner {
			_ = tx.Rollback()
			return zero, false, nil
		}
	}
	tok, err := billing.CutoverClaimTokenForPinAtMarker(pin, lockedMarker)
	if err != nil {
		return zero, false, err
	}
	if verr := billing.ValidateProviderCostClaim(tok, w.AccountID, w.CallID, sealed.Key); verr != nil {
		return zero, false, verr
	}
	claimed := billing.ClaimedProviderCostWork{Work: w, Claim: tok}
	if verr := claimed.Validate(); verr != nil {
		return zero, false, verr
	}
	if err := tx.Commit(); err != nil {
		return zero, false, fmt.Errorf("billingstore: commit provider cutover claim: %w", err)
	}
	return claimed, true, nil
}

func isProviderPinEligibleForClaim(marker billing.AccountingCutoverMarker, pin billing.PostingPin) bool {
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

func insertProviderPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, opKey, owner string) error {
	payload, err := json.Marshal(subject)
	if err != nil {
		return fmt.Errorf("%w: pin subject encode: %v", billing.ErrPostingOwnershipInvalid, err)
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), opKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), string(payload), owner, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: acquire provider cutover pin: %w", err)
	}
	return nil
}
