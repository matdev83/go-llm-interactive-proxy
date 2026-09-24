package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Durable per-operation posting ownership pins for Task 17.3B1.
//
// One row per (store, operation kind, canonical operation key) pins exactly
// one posting owner (v1/v2) with the marker epoch/generation/version snapshot
// observed at acquisition, plus durable completion state. Acquisition validates
// the current marker allows the owner; exact replay is idempotent; owner/
// epoch/identity mismatch is conflict/fence. Completion records the posting
// outcome so waking workers distinguish unposted pinned work from already
// completed effects. All mutations are atomic CAS/insert inside bounded
// transactions; concurrent initializers converge via INSERT ... ON CONFLICT DO
// NOTHING / OR IGNORE plus an authoritative read. No claim/worker/admission
// wiring lives here; B2 consumes this narrow port.

type postingOwnershipRow struct {
	ID                      int64  `bun:"id"`
	StoreID                 string `bun:"store_id"`
	OperationKind           string `bun:"operation_kind"`
	OperationKey            string `bun:"operation_key"`
	AccountID               string `bun:"account_id"`
	CallID                  string `bun:"call_id"`
	BLegID                  string `bun:"b_leg_id"`
	ProviderChargeID        string `bun:"provider_charge_id"`
	HeadKey                 string `bun:"head_key"`
	SubjectKind             string `bun:"subject_kind"`
	SubjectJSON             string `bun:"subject_json"`
	Owner                   string `bun:"owner"`
	MarkerVersion           int64  `bun:"marker_version"`
	MarkerEpoch             int64  `bun:"marker_epoch"`
	MarkerGeneration        int    `bun:"marker_generation"`
	MarkerState             string `bun:"marker_state"`
	Status                  string `bun:"status"`
	CompletionOperationKey  string `bun:"completion_operation_key"`
	CompletionTransactionID string `bun:"completion_transaction_id"`
	CreatedAtUnix           int64  `bun:"created_at_unix"`
	UpdatedAtUnix           int64  `bun:"updated_at_unix"`
	CompletedAtUnix         int64  `bun:"completed_at_unix"`
}

const postingOwnershipSelect = `SELECT id, store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix FROM billing_posting_ownership_pins`

func postingOwnershipRowToPin(row postingOwnershipRow) (billing.PostingPin, error) {
	// B2b4 direct-adjustment pins carry no call lineage (empty call_id).
	// Selected-cost, cost-pass-through, customer and provider pins always
	// carry a valid call.
	var callID billing.BillingCallID
	if row.CallID == "" {
		if row.OperationKind != string(billing.PostingOperationFinancialAdjustment) || !billing.IsDirectAdjustmentPinKey(row.OperationKey) {
			return billing.PostingPin{}, fmt.Errorf("%w: pin call id: empty call requires direct adjustment pin", billing.ErrPostingOwnershipInvalid)
		}
	} else {
		parsed, err := billing.ParseBillingCallID(row.CallID)
		if err != nil {
			return billing.PostingPin{}, fmt.Errorf("%w: pin call id: %v", billing.ErrPostingOwnershipInvalid, err)
		}
		callID = parsed
	}
	var subject metering.SubjectRef
	if row.SubjectJSON != "" {
		if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
			return billing.PostingPin{}, fmt.Errorf("%w: pin subject decode: %v", billing.ErrPostingOwnershipInvalid, err)
		}
	}
	pin := billing.PostingPin{
		StoreID:                 row.StoreID,
		Kind:                    billing.PostingOperationKind(row.OperationKind),
		OperationKey:            row.OperationKey,
		AccountID:               row.AccountID,
		CallID:                  callID,
		BLegID:                  row.BLegID,
		ProviderChargeID:        row.ProviderChargeID,
		HeadKey:                 row.HeadKey,
		Subject:                 subject,
		Owner:                   row.Owner,
		MarkerVersion:           uint64(row.MarkerVersion),
		MarkerEpoch:             uint64(row.MarkerEpoch),
		MarkerGeneration:        row.MarkerGeneration,
		MarkerState:             billing.AccountingCutoverState(row.MarkerState),
		Status:                  billing.PostingPinStatus(row.Status),
		CompletionOperationKey:  row.CompletionOperationKey,
		CompletionTransactionID: row.CompletionTransactionID,
		CreatedAtUnix:           row.CreatedAtUnix,
		UpdatedAtUnix:           row.UpdatedAtUnix,
		CompletedAtUnix:         row.CompletedAtUnix,
	}
	if row.MarkerVersion <= 0 || row.MarkerEpoch <= 0 {
		return billing.PostingPin{}, fmt.Errorf("%w: stored pin marker version/epoch out of range", billing.ErrPostingOwnershipInvalid)
	}
	if err := pin.Validate(); err != nil {
		return billing.PostingPin{}, err
	}
	return pin, nil
}

func (s *DurableStore) loadPostingOwnershipPin(ctx context.Context, q bun.IDB, kind billing.PostingOperationKind, operationKey string) (postingOwnershipRow, bool, error) {
	var row postingOwnershipRow
	err := q.NewRaw(postingOwnershipSelect+` WHERE store_id = ? AND operation_kind = ? AND operation_key = ? LIMIT 1`, s.storeID, string(kind), operationKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return postingOwnershipRow{}, false, nil
	}
	if err != nil {
		return postingOwnershipRow{}, false, fmt.Errorf("billingstore: load posting ownership pin: %w", err)
	}
	return row, true, nil
}

func derivePostingOwnershipKey(storeID string, req billing.AcquirePostingPinRequest) (operationKey, bLegID, providerChargeID, headKey, subjectKind, subjectJSON string, subject metering.SubjectRef, err error) {
	switch req.Kind {
	case billing.PostingOperationCustomerSettlement:
		key, kerr := billing.CustomerPostingOperationKey(req.AccountID, req.CallID)
		if kerr != nil {
			return "", "", "", "", "", "", metering.SubjectRef{}, kerr
		}
		return key, "", "", "", "", "", metering.SubjectRef{}, nil
	case billing.PostingOperationProviderCharge:
		key, kerr := billing.ProviderPostingOperationKey(storeID, req.AccountID, req.CallID, req.Subject)
		if kerr != nil {
			return "", "", "", "", "", "", metering.SubjectRef{}, kerr
		}
		payload, merr := json.Marshal(req.Subject)
		if merr != nil {
			return "", "", "", "", "", "", metering.SubjectRef{}, fmt.Errorf("%w: pin subject encode: %v", billing.ErrPostingOwnershipInvalid, merr)
		}
		return key, req.Subject.BLegID, req.Subject.ProviderChargeID, "", string(req.Subject.Kind), string(payload), req.Subject, nil
	case billing.PostingOperationFinancialAdjustment:
		key, kerr := billing.FinancialAdjustmentPostingOperationKey(storeID, req.AccountID, req.CallID, req.HeadKey, req.Subject)
		if kerr != nil {
			return "", "", "", "", "", "", metering.SubjectRef{}, kerr
		}
		payload, merr := json.Marshal(req.Subject)
		if merr != nil {
			return "", "", "", "", "", "", metering.SubjectRef{}, fmt.Errorf("%w: pin subject encode: %v", billing.ErrPostingOwnershipInvalid, merr)
		}
		return key, req.Subject.BLegID, req.Subject.ProviderChargeID, req.HeadKey, string(req.Subject.Kind), string(payload), req.Subject, nil
	default:
		return "", "", "", "", "", "", metering.SubjectRef{}, fmt.Errorf("%w: unknown operation kind %q", billing.ErrPostingOwnershipInvalid, string(req.Kind))
	}
}

func derivePostingOwnershipKeyForComplete(storeID string, req billing.CompletePostingPinRequest) (operationKey, bLegID, providerChargeID, headKey, subjectKind, subjectJSON string, subject metering.SubjectRef, err error) {
	acquire := billing.AcquirePostingPinRequest{Kind: req.Kind, AccountID: req.AccountID, CallID: req.CallID, Subject: req.Subject, HeadKey: req.HeadKey, Owner: req.Owner, ExpectedMarkerVersion: req.ExpectedMarkerVersion, ExpectedMarkerEpoch: req.ExpectedMarkerEpoch}
	return derivePostingOwnershipKey(storeID, acquire)
}

// GetPostingPin reads the durable pin for one canonical operation without
// creating it. Fresh operations report ErrPostingOwnershipNotFound so callers
// can Acquire the explicit pin. Completed pins remain readable as history.
func (s *DurableStore) GetPostingPin(ctx context.Context, kind billing.PostingOperationKind, operationKey string) (billing.PostingPin, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.PostingPin{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.PostingPin{}, err
	}
	if !kind.Valid() {
		return billing.PostingPin{}, fmt.Errorf("%w: unknown operation kind %q", billing.ErrPostingOwnershipInvalid, string(kind))
	}
	if operationKey == "" {
		return billing.PostingPin{}, fmt.Errorf("%w: operation key is required", billing.ErrPostingOwnershipInvalid)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, s.db, kind, operationKey)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !found {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q has no pin", billing.ErrPostingOwnershipNotFound, string(kind), operationKey)
	}
	return postingOwnershipRowToPin(row)
}

// AcquirePostingPin pins exactly one owner to one logical financial operation.
// Exact replay (same kind/key/owner) is idempotent; owner mismatch is
// conflict; stale expected marker or state-forbidden owner is fence.
func (s *DurableStore) AcquirePostingPin(ctx context.Context, req billing.AcquirePostingPinRequest) (billing.PostingPin, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.PostingPin{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.PostingPin{}, err
	}
	if err := req.Validate(); err != nil {
		return billing.PostingPin{}, err
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.PostingPin, error) {
		return s.acquirePostingPinAttempt(ctx, req)
	})
}

func (s *DurableStore) acquirePostingPinAttempt(ctx context.Context, req billing.AcquirePostingPinRequest) (billing.PostingPin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: begin posting ownership acquire: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker so pin acquisition serializes with
	// activation. PostgreSQL SELECT FOR UPDATE; SQLite write upgrade.
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !markerFound {
		return billing.PostingPin{}, fmt.Errorf("%w: store %q has no cutover marker", billing.ErrAccountingCutoverNotFound, s.storeID)
	}
	currentMarker, err := accountingCutoverRowToMarker(markerRow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if req.ExpectedMarkerVersion != currentMarker.Version || req.ExpectedMarkerEpoch != currentMarker.Epoch {
		return billing.PostingPin{}, fmt.Errorf("%w: expected marker version/epoch %d/%d, current %d/%d",
			billing.ErrPostingOwnershipFence, req.ExpectedMarkerVersion, req.ExpectedMarkerEpoch, currentMarker.Version, currentMarker.Epoch)
	}
	operationKey, bLegID, providerChargeID, headKey, subjectKind, subjectJSON, subject, err := derivePostingOwnershipKey(s.storeID, req)
	if err != nil {
		return billing.PostingPin{}, err
	}
	// Subject store scope is part of the canonical identity; fail closed when
	// the caller supplies a foreign store.
	if req.Kind != billing.PostingOperationCustomerSettlement && subject.StoreID != s.storeID {
		return billing.PostingPin{}, fmt.Errorf("%w: subject store %q differs from pin store %q", billing.ErrPostingOwnershipInvalid, subject.StoreID, s.storeID)
	}
	existingRow, found, err := s.loadPostingOwnershipPin(ctx, tx, req.Kind, operationKey)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if found {
		existing, err := postingOwnershipRowToPin(existingRow)
		if err != nil {
			return billing.PostingPin{}, err
		}
		if existing.Owner != req.Owner {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey, existing.Owner, req.Owner)
		}
		if !billing.IsPostingPinAcquireReplayAllowed(currentMarker.State, existing) {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q replay not allowed in %q", billing.ErrPostingOwnershipFence, string(req.Kind), operationKey, string(currentMarker.State))
		}
		if !billing.IsPostingPinReplay(existing, req, operationKey) {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q replay identity mismatch", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey)
		}
		if err := tx.Commit(); err != nil {
			return billing.PostingPin{}, fmt.Errorf("billingstore: commit posting ownership replay: %w", err)
		}
		return existing, nil
	}
	// New pin: marker must permit this owner.
	if !billing.IsPostingOwnerAllowedForNew(currentMarker.State, req.Owner) {
		return billing.PostingPin{}, fmt.Errorf("%w: %s owner %q not allowed for new operations in %q", billing.ErrPostingOwnershipFence, string(req.Kind), req.Owner, string(currentMarker.State))
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(req.Kind), operationKey, req.AccountID, req.CallID.String(), bLegID, providerChargeID, headKey, subjectKind, subjectJSON, req.Owner, int64(currentMarker.Version), int64(currentMarker.Epoch), currentMarker.Generation, string(currentMarker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: insert posting ownership pin: %w", err)
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, req.Kind, operationKey)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !found {
		return billing.PostingPin{}, fmt.Errorf("%w: pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return billing.PostingPin{}, err
	}
	// Lost a concurrent insert race: classify as replay vs conflict.
	if pin.Owner != req.Owner {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey, pin.Owner, req.Owner)
	}
	if !billing.IsPostingPinReplay(pin, req, operationKey) {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q replay identity mismatch", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey)
	}
	// Concurrent replay during a state that forbids it must still fence.
	if !billing.IsPostingPinAcquireReplayAllowed(currentMarker.State, pin) {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q replay not allowed in %q", billing.ErrPostingOwnershipFence, string(req.Kind), operationKey, string(currentMarker.State))
	}
	_ = subject
	if err := tx.Commit(); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: commit posting ownership acquire: %w", err)
	}
	return pin, nil
}

// CompletePostingPin records the posting outcome for a pinned operation.
// Exact replay with the identical outcome is idempotent; a different outcome
// under the same pin is a conflict. Completing an unpinned operation reports
// NotFound. State-forbidden completions (for example V1 after v2_active) are
// fenced; exact completed-history replays bypass the allowance check.
func (s *DurableStore) CompletePostingPin(ctx context.Context, req billing.CompletePostingPinRequest) (billing.PostingPin, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.PostingPin{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.PostingPin{}, err
	}
	if err := req.Validate(); err != nil {
		return billing.PostingPin{}, err
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.PostingPin, error) {
		return s.completePostingPinAttempt(ctx, req)
	})
}

func (s *DurableStore) completePostingPinAttempt(ctx context.Context, req billing.CompletePostingPinRequest) (billing.PostingPin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: begin posting ownership complete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !markerFound {
		return billing.PostingPin{}, fmt.Errorf("%w: store %q has no cutover marker", billing.ErrAccountingCutoverNotFound, s.storeID)
	}
	currentMarker, err := accountingCutoverRowToMarker(markerRow)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if req.ExpectedMarkerVersion != currentMarker.Version || req.ExpectedMarkerEpoch != currentMarker.Epoch {
		return billing.PostingPin{}, fmt.Errorf("%w: expected marker version/epoch %d/%d, current %d/%d",
			billing.ErrPostingOwnershipFence, req.ExpectedMarkerVersion, req.ExpectedMarkerEpoch, currentMarker.Version, currentMarker.Epoch)
	}
	operationKey, _, _, _, _, _, _, err := derivePostingOwnershipKeyForComplete(s.storeID, req)
	if err != nil {
		return billing.PostingPin{}, err
	}
	row, found, err := s.loadPostingOwnershipPin(ctx, tx, req.Kind, operationKey)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !found {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q has no pin", billing.ErrPostingOwnershipNotFound, string(req.Kind), operationKey)
	}
	pin, err := postingOwnershipRowToPin(row)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if pin.Owner != req.Owner {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey, pin.Owner, req.Owner)
	}
	if pin.Status == billing.PostingPinCompleted {
		if pin.CompletionOperationKey != req.CompletionOperationKey || pin.CompletionTransactionID != req.CompletionTransactionID {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q completion mismatch", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey)
		}
		if err := tx.Commit(); err != nil {
			return billing.PostingPin{}, fmt.Errorf("billingstore: commit posting ownership completion replay: %w", err)
		}
		return pin, nil
	}
	if !billing.IsPostingPinCompleteAllowed(currentMarker.State, pin) {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q completion not allowed in %q", billing.ErrPostingOwnershipFence, string(req.Kind), operationKey, string(currentMarker.State))
	}
	now := time.Now().UTC().UnixNano()
	if now < pin.CreatedAtUnix {
		now = pin.CreatedAtUnix
	}
	res, err := tx.NewRaw(`UPDATE billing_posting_ownership_pins SET status = ?, completion_operation_key = ?, completion_transaction_id = ?, updated_at_unix = ?, completed_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ?`,
		string(billing.PostingPinCompleted), req.CompletionOperationKey, req.CompletionTransactionID, now, now,
		s.storeID, string(req.Kind), operationKey, string(billing.PostingPinPinned)).Exec(ctx)
	if err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: complete posting ownership pin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: posting ownership rows affected: %w", err)
	}
	if affected != 1 {
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, req.Kind, operationKey)
		if rerr != nil {
			return billing.PostingPin{}, rerr
		}
		if !refound {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q missing after race", billing.ErrPostingOwnershipNotFound, string(req.Kind), operationKey)
		}
		remarker, merr := postingOwnershipRowToPin(rerow)
		if merr != nil {
			return billing.PostingPin{}, merr
		}
		if remarker.Status == billing.PostingPinCompleted && remarker.CompletionOperationKey == req.CompletionOperationKey && remarker.CompletionTransactionID == req.CompletionTransactionID && remarker.Owner == req.Owner {
			if err := tx.Commit(); err != nil {
				return billing.PostingPin{}, fmt.Errorf("billingstore: commit posting ownership race replay: %w", err)
			}
			return remarker, nil
		}
		if remarker.Status == billing.PostingPinCompleted {
			return billing.PostingPin{}, fmt.Errorf("%w: %s %q completion mismatch", billing.ErrPostingOwnershipConflict, string(req.Kind), operationKey)
		}
		return billing.PostingPin{}, fmt.Errorf("%w: concurrent completion for %s %q", billing.ErrPostingOwnershipFence, string(req.Kind), operationKey)
	}
	updated, found, err := s.loadPostingOwnershipPin(ctx, tx, req.Kind, operationKey)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if !found {
		return billing.PostingPin{}, fmt.Errorf("%w: %s %q missing after complete", billing.ErrPostingOwnershipNotFound, string(req.Kind), operationKey)
	}
	completed, err := postingOwnershipRowToPin(updated)
	if err != nil {
		return billing.PostingPin{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.PostingPin{}, fmt.Errorf("billingstore: commit posting ownership complete: %w", err)
	}
	return completed, nil
}

// txForMarker returns a FOR UPDATE-capable handle for the marker load. The
// accounting cutover loader already appends FOR UPDATE on PG when asked; pins
// reuse the same transaction so marker and pin reads serialize. SQLite relies
// on its writer transaction plus bounded retry.
func txForMarker(tx bun.Tx, db *bun.DB) bun.IDB {
	if db != nil && db.Dialect().Name() == dialect.PG {
		// Use the transaction directly; the cutover loader adds FOR UPDATE
		// when forUpdate is true. Pins deliberately use the non-locking read
		// here and rely on the pin UNIQUE plus CAS for convergence, while the
		// marker CAS in TransitionAccountingCutover serializes cutover races.
		// Keeping this helper documents the intended lock scope for B2.
		return tx
	}
	return tx
}
