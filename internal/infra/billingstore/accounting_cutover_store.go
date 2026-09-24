package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Durable per-store accounting cutover marker for Task 17.3A (Migration
// Strategy step 6 foundation).
//
// One row per configured deployment/store boundary (store_id primary key)
// carries the explicit accounting generation, monotonic version/epoch,
// active posting owner (V1/V2 writer lineage), and compatibility floor for
// later claim fencing (17.3B) and rollback checks (17.4). All transitions use
// monotonic compare-and-swap on (version, epoch); stale writers fail with
// billing.ErrAccountingCutoverFence, conflicting exact-replay payloads fail
// with billing.ErrAccountingCutoverConflict, and malformed targets fail with
// billing.ErrAccountingCutoverInvalid.
//
// Crash/restart safe: rows are durable, migrations are idempotent, and exact
// replay of the same expected version/epoch plus target state plus transition
// identity is a no-op success. Legacy stores with no row converge on a safe
// explicit V1 default via EnsureAccountingCutover; concurrent initializers use
// INSERT ... ON CONFLICT DO NOTHING / OR IGNORE so exactly one default wins.
//
// This file exposes only the narrow Get/Ensure/Transition port later 17.3B
// consumes. It does not touch terminal claims, economic workers, exposure
// admission, journals, balances, or payables.

type accountingCutoverRow struct {
	StoreID            string `bun:"store_id"`
	Generation         int    `bun:"generation"`
	State              string `bun:"state"`
	ActivePostingOwner string `bun:"active_posting_owner"`
	CompatibilityFloor string `bun:"compatibility_floor"`
	Version            int64  `bun:"version"`
	Epoch              int64  `bun:"epoch"`
	TransitionID       string `bun:"transition_id"`
	CreatedAtUnix      int64  `bun:"created_at_unix"`
	UpdatedAtUnix      int64  `bun:"updated_at_unix"`
}

const accountingCutoverSelect = `SELECT store_id, generation, state, active_posting_owner, compatibility_floor, version, epoch, transition_id, created_at_unix, updated_at_unix FROM billing_accounting_cutover`

func accountingCutoverRowToMarker(row accountingCutoverRow) (billing.AccountingCutoverMarker, error) {
	marker := billing.AccountingCutoverMarker{
		StoreID:            row.StoreID,
		Generation:         row.Generation,
		State:              billing.AccountingCutoverState(row.State),
		ActivePostingOwner: row.ActivePostingOwner,
		CompatibilityFloor: row.CompatibilityFloor,
		Version:            uint64(row.Version),
		Epoch:              uint64(row.Epoch),
		TransitionID:       row.TransitionID,
		CreatedAtUnix:      row.CreatedAtUnix,
		UpdatedAtUnix:      row.UpdatedAtUnix,
	}
	if row.Version <= 0 || row.Epoch <= 0 {
		return billing.AccountingCutoverMarker{}, fmt.Errorf("%w: stored cutover version/epoch out of range", billing.ErrAccountingCutoverInvalid)
	}
	if err := marker.Validate(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	return marker, nil
}

func (s *DurableStore) loadAccountingCutover(ctx context.Context, q bun.IDB) (accountingCutoverRow, bool, error) {
	var row accountingCutoverRow
	err := q.NewRaw(accountingCutoverSelect+` WHERE store_id = ? LIMIT 1`, s.storeID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return accountingCutoverRow{}, false, nil
	}
	if err != nil {
		return accountingCutoverRow{}, false, fmt.Errorf("billingstore: load accounting cutover: %w", err)
	}
	return row, true, nil
}

// GetAccountingCutover reads the durable marker for this store boundary
// without creating it. Fresh legacy stores report
// billing.ErrAccountingCutoverNotFound so callers can Ensure the explicit V1
// default.
func (s *DurableStore) GetAccountingCutover(ctx context.Context) (billing.AccountingCutoverMarker, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	row, found, err := s.loadAccountingCutover(ctx, s.db)
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if !found {
		return billing.AccountingCutoverMarker{}, fmt.Errorf("%w: store %q has no cutover marker", billing.ErrAccountingCutoverNotFound, s.storeID)
	}
	return accountingCutoverRowToMarker(row)
}

// EnsureAccountingCutover returns the durable marker, creating the safe
// legacy default (V1 active, generation 1, version/epoch 1, V1 posting owner)
// when no row exists. Concurrent initializers converge via an idempotent
// insert-or-ignore followed by a single authoritative read.
func (s *DurableStore) EnsureAccountingCutover(ctx context.Context) (billing.AccountingCutoverMarker, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if row, found, err := s.loadAccountingCutover(ctx, s.db); err != nil {
		return billing.AccountingCutoverMarker{}, err
	} else if found {
		return accountingCutoverRowToMarker(row)
	}
	now := time.Now().UTC().UnixNano()
	def, err := billing.DefaultAccountingCutoverMarker(s.storeID, now)
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	insert := `INSERT INTO billing_accounting_cutover (store_id, generation, state, active_posting_owner, compatibility_floor, version, epoch, transition_id, created_at_unix, updated_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_accounting_cutover (store_id, generation, state, active_posting_owner, compatibility_floor, version, epoch, transition_id, created_at_unix, updated_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := s.db.ExecContext(ctx, insert, def.StoreID, def.Generation, string(def.State), def.ActivePostingOwner, def.CompatibilityFloor, int64(def.Version), int64(def.Epoch), def.TransitionID, def.CreatedAtUnix, def.UpdatedAtUnix); err != nil {
		return billing.AccountingCutoverMarker{}, fmt.Errorf("billingstore: ensure accounting cutover: %w", err)
	}
	row, found, err := s.loadAccountingCutover(ctx, s.db)
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if !found {
		return billing.AccountingCutoverMarker{}, fmt.Errorf("%w: store %q cutover unavailable after ensure", billing.ErrAccountingCutoverNotFound, s.storeID)
	}
	return accountingCutoverRowToMarker(row)
}

// TransitionAccountingCutover advances the marker by exactly one monotonic
// step using compare-and-swap on (version, epoch). Exact replay of the same
// expected version/epoch, target state, and transition identity returns the
// durable row without a second write. Stale CAS fails with
// billing.ErrAccountingCutoverFence; same-version advances with a different
// payload fail with billing.ErrAccountingCutoverConflict; malformed or
// non-monotonic targets fail with billing.ErrAccountingCutoverInvalid.
func (s *DurableStore) TransitionAccountingCutover(ctx context.Context, req billing.AccountingCutoverTransition) (billing.AccountingCutoverMarker, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := req.Validate(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	var result billing.AccountingCutoverMarker
	err := withAccountTxErr(ctx, accountTxRetry{Attempts: 40}, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("billingstore: begin accounting cutover transition: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		// F1: lock the per-store marker row so concurrent monetary
		// transactions and cutover transitions serialize. PostgreSQL uses
		// SELECT FOR UPDATE; SQLite upgrades to a write via dummy UPDATE.
		row, found, err := s.loadAccountingCutoverLocked(ctx, tx)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: store %q has no cutover marker", billing.ErrAccountingCutoverNotFound, s.storeID)
		}
		current, err := accountingCutoverRowToMarker(row)
		if err != nil {
			return err
		}
		// Exact replay: already advanced by this exact request.
		if billing.IsAccountingCutoverReplay(current, req) {
			result = current
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("billingstore: commit accounting cutover replay: %w", err)
			}
			return nil
		}
		// Same-version advance with a different payload is a conflict, not a
		// silent fence: the caller replayed the wrong identity.
		if current.Version == req.ExpectedVersion+1 && current.Epoch == req.ExpectedEpoch+1 {
			return fmt.Errorf("%w: cutover replay identity mismatch for store %q", billing.ErrAccountingCutoverConflict, s.storeID)
		}
		if req.ExpectedVersion != current.Version || req.ExpectedEpoch != current.Epoch {
			return fmt.Errorf("%w: expected version/epoch %d/%d, current %d/%d",
				billing.ErrAccountingCutoverFence, req.ExpectedVersion, req.ExpectedEpoch, current.Version, current.Epoch)
		}
		now := time.Now().UTC().UnixNano()
		next, err := billing.ValidateAccountingCutoverTransition(current, req, now)
		if err != nil {
			return err
		}
		res, err := tx.NewRaw(`UPDATE billing_accounting_cutover SET
				generation = ?, state = ?, active_posting_owner = ?, compatibility_floor = ?,
				version = ?, epoch = ?, transition_id = ?, updated_at_unix = ?
			WHERE store_id = ? AND version = ? AND epoch = ?`,
			next.Generation, string(next.State), next.ActivePostingOwner, next.CompatibilityFloor,
			int64(next.Version), int64(next.Epoch), next.TransitionID, next.UpdatedAtUnix,
			s.storeID, int64(current.Version), int64(current.Epoch)).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: advance accounting cutover: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("billingstore: accounting cutover rows affected: %w", err)
		}
		if affected != 1 {
			// Lost a concurrent CAS race; re-read to classify replay vs fence.
			rerow, refound, rerr := s.loadAccountingCutoverLocked(ctx, tx)
			if rerr != nil {
				return rerr
			}
			if !refound {
				return fmt.Errorf("%w: store %q cutover missing after race", billing.ErrAccountingCutoverNotFound, s.storeID)
			}
			remarker, merr := accountingCutoverRowToMarker(rerow)
			if merr != nil {
				return merr
			}
			if billing.IsAccountingCutoverReplay(remarker, req) {
				result = remarker
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("billingstore: commit accounting cutover replay: %w", err)
				}
				return nil
			}
			if remarker.Version == req.ExpectedVersion+1 && remarker.Epoch == req.ExpectedEpoch+1 {
				return fmt.Errorf("%w: cutover replay identity mismatch for store %q", billing.ErrAccountingCutoverConflict, s.storeID)
			}
			return fmt.Errorf("%w: concurrent cutover advance for store %q", billing.ErrAccountingCutoverFence, s.storeID)
		}
		result = next
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit accounting cutover transition: %w", err)
		}
		return nil
	})
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	return result, nil
}
