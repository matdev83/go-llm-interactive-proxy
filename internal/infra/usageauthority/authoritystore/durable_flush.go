package authoritystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for durable authority stores
)

func (s *DurableStore) seedAndFlush(ctx context.Context) error {
	log := newRecordingMutationLog()
	for key, row := range s.c.limits {
		cp := *row
		log.limitUpdates[key] = &cp
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Establish the stable coordination row before inserting possibly
		// absent limit rows. A concurrent mutation can therefore wait on this
		// row and reload committed counters instead of racing the seed inserts.
		readinessBytes, err := json.Marshal(s.c.readiness())
		if err != nil {
			return fmt.Errorf("authoritystore seed readiness encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_state(store_id, readiness_json, next_decision_seq)
			VALUES(?,?,?)
			ON CONFLICT(store_id) DO NOTHING`, s.c.storeID, string(readinessBytes), s.c.nextDecision); err != nil {
			return storeUnavailableError("seed coordination row", err)
		}
		if err := s.lockStateTx(ctx, tx, s.c); err != nil {
			return err
		}
		return s.flushInTx(ctx, tx, s.c, log, nil, nil, nil, true)
	})
}

// reconcileAndFlush applies the current configured row templates to an
// existing durable projection. The locked read preserves the latest counters
// before the conditional writes, so a restart cannot replace current limits
// with stale persisted configuration or lose a concurrent reservation.
func (s *DurableStore) reconcileAndFlush(ctx context.Context) error {
	err := s.reconcileAndFlushOnce(ctx)
	if !errors.Is(err, errConcurrentLimitRowCreate) {
		return err
	}
	return s.reconcileAndFlushOnce(ctx)
}

func (s *DurableStore) reconcileAndFlushOnce(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storeUnavailableError("reconcile begin", err)
	}
	core := s.newMutationCore()
	keys := make(map[string]struct{})
	for _, templates := range core.limitTemplates {
		for _, template := range templates {
			keys[limitRowKey(template)] = struct{}{}
		}
	}
	preImage, err := s.lockLimitRowsByKeyTx(ctx, tx, core, keys)
	if err != nil {
		_ = tx.Rollback()
		return storeUnavailableError("reconcile load", err)
	}
	core.limits = core.reconcileLimitRows(core.limits)
	log := newRecordingMutationLog()
	for _, templates := range core.limitTemplates {
		for _, template := range templates {
			key := limitRowKey(template)
			if row := core.limits[key]; row != nil {
				cp := *row
				log.limitUpdates[key] = &cp
			}
		}
	}
	if log.isEmpty() {
		_ = tx.Rollback()
		return nil
	}
	if err := s.flushInTx(ctx, tx, core, log, preImage, nil, nil, false); err != nil {
		_ = tx.Rollback()
		return s.markFlushFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return s.markFlushFailed(storeUnavailableError("reconcile commit", err))
	}
	return nil
}

// flushInTx applies a single transactional batch of targeted writes. When seed
// is false, limit rows use a conditional UPDATE on the pre-image row_json (or
// INSERT ... ON CONFLICT DO NOTHING for new rows) so a lost update is detected
// and reported as app.ErrReservationConflict via errDurableFlushFailed. When
// seed is true, state and limit rows use conflict-safe inserts so a concurrent
// initializer cannot overwrite live counters. The state row is otherwise
// upserted whenever decisions were appended.
func (s *DurableStore) flushInTx(ctx context.Context, tx bun.Tx, core *storeCore, log *recordingMutationLog, limitPreImage, reservationPreImage, factPreImage map[string]string, seed bool) error {
	if log.isEmpty() && !seed {
		return nil
	}
	forceStateUpsert := seed || len(log.decisionsAppended) > 0
	if forceStateUpsert {
		readinessBytes, err := json.Marshal(core.readiness())
		if err != nil {
			return fmt.Errorf("authoritystore flush readiness encode: %w", err)
		}
		stateSQL := `INSERT INTO usage_authority_state(store_id, readiness_json, next_decision_seq) VALUES(?,?,?)`
		if seed {
			stateSQL += ` ON CONFLICT(store_id) DO NOTHING`
		} else {
			stateSQL += ` ON CONFLICT(store_id) DO UPDATE SET readiness_json = EXCLUDED.readiness_json, next_decision_seq = EXCLUDED.next_decision_seq`
		}
		if _, err := tx.ExecContext(ctx, stateSQL, core.storeID, string(readinessBytes), core.nextDecision); err != nil {
			return storeUnavailableError("flush state", err)
		}
	}

	for key, row := range log.limitUpdates {
		rawBytes, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("authoritystore flush limit encode: %w", err)
		}
		raw := string(rawBytes)
		if seed {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json)
				VALUES(?,?,?)
				ON CONFLICT(store_id, row_key) DO NOTHING`,
				core.storeID, key, raw); err != nil {
				return storeUnavailableError("flush limit row", err)
			}
			if err := s.replaceLimitFiltersTx(ctx, tx, core.storeID, key, *row); err != nil {
				return err
			}
			continue
		}
		if pre, ok := limitPreImage[key]; ok {
			// Existing row: conditional UPDATE on the pre-image row_json so a
			// concurrent writer that changed the row is detected as a lost update.
			res, err := tx.ExecContext(ctx, `
				UPDATE usage_authority_limit_rows SET row_json = ?
				WHERE store_id = ? AND row_key = ? AND row_json = ?`,
				raw, core.storeID, key, pre)
			if err != nil {
				return storeUnavailableError("flush limit row", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return storeUnavailableError("flush limit rows affected", err)
			}
			if n == 0 {
				return fmt.Errorf("%w: stale limit row %q: %w", errDurableFlushFailed, key, app.ErrReservationConflict)
			}
			if err := s.replaceLimitFiltersTx(ctx, tx, core.storeID, key, *row); err != nil {
				return err
			}
			continue
		}
		// New row (e.g. window rollover): insert, and treat a concurrent insert
		// of the same key as a lost-update conflict.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json)
			VALUES(?,?,?)
			ON CONFLICT(store_id, row_key) DO NOTHING`,
			core.storeID, key, raw)
		if err != nil {
			return storeUnavailableError("flush limit row insert", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return storeUnavailableError("flush limit insert rows affected", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: limit row %q: %w", errConcurrentLimitRowCreate, key, app.ErrReservationConflict)
		}
		if err := s.replaceLimitFiltersTx(ctx, tx, core.storeID, key, *row); err != nil {
			return err
		}
	}

	for key, rec := range log.reservationUpserts {
		rawBytes, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("authoritystore flush reservation encode: %w", err)
		}
		raw := string(rawBytes)
		if pre, ok := reservationPreImage[key]; ok {
			res, err := tx.ExecContext(ctx, `
				UPDATE usage_authority_reservations SET source_key = ?, record_json = ?
				WHERE store_id = ? AND reservation_key = ? AND record_json = ?`,
				rec.SourceKey, raw, core.storeID, key, pre)
			if err != nil {
				return storeUnavailableError("flush reservation row", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return storeUnavailableError("flush reservation rows affected", err)
			}
			if n == 0 {
				return fmt.Errorf("%w: stale reservation %q: %w", errDurableFlushFailed, key, app.ErrReservationConflict)
			}
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_reservations(store_id, reservation_key, source_key, record_json)
			VALUES(?,?,?,?)
			ON CONFLICT DO NOTHING`, core.storeID, key, rec.SourceKey, raw)
		if err != nil {
			return storeUnavailableError("flush reservation row", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return storeUnavailableError("flush reservation rows affected", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: reservation %q: %w", errConcurrentReservationCreate, key, app.ErrReservationConflict)
		}
	}

	for _, rec := range log.decisionsAppended {
		rawBytes, err := json.Marshal(rec.Row)
		if err != nil {
			return fmt.Errorf("authoritystore flush decision encode: %w", err)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_decisions(store_id, decision_seq, source_key, row_json)
			VALUES(?,?,?,?)
			ON CONFLICT(store_id, source_key) DO NOTHING`,
			core.storeID, rec.Seq, rec.SourceKey, string(rawBytes))
		if err != nil {
			return storeUnavailableError("flush decision row", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return storeUnavailableError("flush decision rows affected", err)
		} else if n == 0 {
			// The logical source already exists. The transaction-level replay
			// fence normally catches this before mutation; keeping the insert
			// defensive makes a unique-key race idempotent as well.
			if _, found, qerr := s.decisionBySourceTx(ctx, tx, rec.SourceKey); qerr != nil {
				return qerr
			} else if !found {
				return fmt.Errorf("%w: decision source %q", errDurableFlushFailed, rec.SourceKey)
			}
		}
		if err := s.replaceDecisionFiltersTx(ctx, tx, core.storeID, rec); err != nil {
			return err
		}
	}
	for key, fact := range log.unreservedFacts {
		rawBytes, err := json.Marshal(fact)
		if err != nil {
			return fmt.Errorf("authoritystore flush unreserved usage fact encode: %w", err)
		}
		raw := string(rawBytes)
		if pre, ok := factPreImage[key]; ok {
			res, err := tx.ExecContext(ctx, `
				UPDATE usage_authority_unreserved_usage_facts SET record_json = ?
				WHERE store_id = ? AND fact_key = ? AND record_json = ?`, raw, core.storeID, key, pre)
			if err != nil {
				return storeUnavailableError("flush unreserved usage fact", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return storeUnavailableError("flush usage fact rows affected", err)
			}
			if n == 0 {
				return fmt.Errorf("%w: stale fact %q: %w", errConcurrentUsageFactMutation, key, app.ErrReservationConflict)
			}
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_unreserved_usage_facts(store_id, fact_key, record_json)
			VALUES(?,?,?)
			ON CONFLICT DO NOTHING`, core.storeID, key, raw)
		if err != nil {
			return storeUnavailableError("flush unreserved usage fact", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return storeUnavailableError("flush usage fact rows affected", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: concurrent fact %q: %w", errConcurrentUsageFactMutation, key, app.ErrReservationConflict)
		}
	}
	return nil
}
