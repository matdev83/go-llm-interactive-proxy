package authoritystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

// DurableStore persists the same store core to a relational database using
// row-targeted writes recorded per call. No shadow state; each Reserve,
// Settle, and Release persists only the rows it actually mutated, and
// idempotent re-runs (no mutation) skip the SQL transaction entirely.
type DurableStore struct {
	mu     sync.Mutex
	closed bool
	db     *sql.DB
	c      *storeCore
}

// NewDurable returns a durable store backed by db.
func NewDurable(ctx context.Context, db *sql.DB, cfg Config) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("authoritystore: nil db")
	}
	if err := Migrate(ctx, db); err != nil {
		return nil, err
	}
	st := &DurableStore{
		db: db,
		c:  newStoreCore(cfg),
	}
	loaded, err := st.load(ctx)
	if err != nil {
		return nil, err
	}
	if !loaded {
		if err := st.seedAndFlush(ctx); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// Close closes the underlying database connection.
func (s *DurableStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	db := s.db
	s.closed = true
	s.mu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

// Migrate creates the durable authority-store tables.
func Migrate(ctx context.Context, db *sql.DB) error {
	if ctx == nil {
		return fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return fmt.Errorf("authoritystore: nil db")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usage_authority_state (
			store_id TEXT NOT NULL PRIMARY KEY,
			readiness_json TEXT NOT NULL,
			next_decision_seq INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_limit_rows (
			store_id TEXT NOT NULL,
			row_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, row_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_decisions (
			store_id TEXT NOT NULL,
			decision_seq INTEGER NOT NULL,
			source_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, decision_seq),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_reservations (
			store_id TEXT NOT NULL,
			reservation_key TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (store_id, reservation_key),
			UNIQUE (store_id, source_key)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return storeUnavailableError("migrate", err)
		}
	}
	return nil
}

// CheckReadiness returns the current posture and surfaces backing failures without leaking driver details.
func (s *DurableStore) CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.AuthorityStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		status := domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
		s.c.state = status
		return status, fmt.Errorf("authoritystore readiness: %w", app.ErrUnavailable)
	}
	if err := s.db.PingContext(ctx); err != nil {
		status := domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
		s.c.state = status
		return status, fmt.Errorf("authoritystore readiness: %w", app.ErrUnavailable)
	}
	return s.c.readiness(), nil
}

// Reserve atomically records a reservation and persists only the rows this call mutated.
func (s *DurableStore) Reserve(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReserveResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return app.ReserveResult{}, unavailableError("reserve")
	}
	log := newRecordingMutationLog()
	out, err := s.c.reserve(cmd, log)
	if err != nil {
		return app.ReserveResult{}, err
	}
	if err := s.flush(ctx, log, false); err != nil {
		return app.ReserveResult{}, err
	}
	return out, nil
}

// Settle reconciles usage and persists only the rows this call mutated.
func (s *DurableStore) Settle(ctx context.Context, cmd app.SettleCommand) (app.SettleResult, error) {
	if err := ctx.Err(); err != nil {
		return app.SettleResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return app.SettleResult{}, unavailableError("settle")
	}
	log := newRecordingMutationLog()
	out, err := s.c.settle(cmd, log)
	if err != nil {
		return app.SettleResult{}, err
	}
	if err := s.flush(ctx, log, false); err != nil {
		return app.SettleResult{}, err
	}
	return out, nil
}

// Release releases reservation capacity and persists only the rows this call mutated.
func (s *DurableStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return app.ReleaseResult{}, unavailableError("release")
	}
	log := newRecordingMutationLog()
	out, err := s.c.release(cmd, log)
	if err != nil {
		return app.ReleaseResult{}, err
	}
	if err := s.flush(ctx, log, false); err != nil {
		return app.ReleaseResult{}, err
	}
	return out, nil
}

// LimitStatus returns bounded live limit rows from the in-memory projection.
func (s *DurableStore) LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, unavailableError("limit_status")
	}
	return s.c.limitStatus(q)
}

// DecisionHistory returns bounded decision rows from the in-memory projection.
func (s *DurableStore) DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, unavailableError("decision_history")
	}
	return s.c.decisionHistory(q)
}

// seedAndFlush writes the initial seeded state (limit rows + readiness) when
// the DB has no rows for this store yet.
func (s *DurableStore) seedAndFlush(ctx context.Context) error {
	log := newRecordingMutationLog()
	for key, row := range s.c.limits {
		cp := *row
		log.limitUpdates[key] = &cp
	}
	return s.flush(ctx, log, true)
}

// flush applies a single transactional batch of targeted UPSERT/INSERT rows.
// When log is empty (the *Existing idempotent paths), no transaction is opened.
// Pass forceStateUpsert=true for the initial seed so the state row is written
// even though no decisions have been appended yet.
func (s *DurableStore) flush(ctx context.Context, log *recordingMutationLog, forceStateUpsert bool) error {
	if log.isEmpty() {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storeUnavailableError("flush begin", err)
	}
	defer func() { _ = tx.Rollback() }()

	// State row is upserted whenever decisions were appended (so c.nextDecision
	// hydrates correctly on next load) or when seeding forces an initial write.
	if forceStateUpsert || len(log.decisionsAppended) > 0 {
		readinessBytes, err := json.Marshal(s.c.readiness())
		if err != nil {
			return fmt.Errorf("authoritystore flush readiness encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_state(store_id, readiness_json, next_decision_seq)
			VALUES(?,?,?)
			ON CONFLICT(store_id) DO UPDATE SET
				readiness_json = EXCLUDED.readiness_json,
				next_decision_seq = EXCLUDED.next_decision_seq`,
			s.c.storeID, string(readinessBytes), s.c.nextDecision); err != nil {
			return storeUnavailableError("flush state", err)
		}
	}

	for key, row := range log.limitUpdates {
		rawBytes, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("authoritystore flush limit encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json)
			VALUES(?,?,?)
			ON CONFLICT(store_id, row_key) DO UPDATE SET
				row_json = EXCLUDED.row_json`,
			s.c.storeID, key, string(rawBytes)); err != nil {
			return storeUnavailableError("flush limit row", err)
		}
	}

	for key, rec := range log.reservationUpserts {
		rawBytes, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("authoritystore flush reservation encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_reservations(store_id, reservation_key, source_key, record_json)
			VALUES(?,?,?,?)
			ON CONFLICT(store_id, reservation_key) DO UPDATE SET
				source_key = EXCLUDED.source_key,
				record_json = EXCLUDED.record_json`,
			s.c.storeID, key, rec.SourceKey, string(rawBytes)); err != nil {
			return storeUnavailableError("flush reservation row", err)
		}
	}

	for _, rec := range log.decisionsAppended {
		rawBytes, err := json.Marshal(rec.Row)
		if err != nil {
			return fmt.Errorf("authoritystore flush decision encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_authority_decisions(store_id, decision_seq, source_key, row_json)
			VALUES(?,?,?,?)
			ON CONFLICT(store_id, decision_seq) DO UPDATE SET
				source_key = EXCLUDED.source_key,
				row_json = EXCLUDED.row_json`,
			s.c.storeID, rec.Seq, rec.SourceKey, string(rawBytes)); err != nil {
			return storeUnavailableError("flush decision row", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return storeUnavailableError("flush commit", err)
	}
	return nil
}

func (s *DurableStore) load(ctx context.Context) (bool, error) {
	var readinessJSON string
	var nextSeq int64
	err := s.db.QueryRowContext(ctx, `SELECT readiness_json, next_decision_seq FROM usage_authority_state WHERE store_id = ?`, s.c.storeID).Scan(&readinessJSON, &nextSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeUnavailableError("load state", err)
	}
	if err := json.Unmarshal([]byte(readinessJSON), &s.c.state); err != nil {
		return false, fmt.Errorf("authoritystore load readiness: %w", err)
	}
	s.c.nextDecision = nextSeq

	limitRows, err := s.loadLimitRows(ctx)
	if err != nil {
		return false, err
	}
	s.c.limits = limitRows

	reservations, resBySource, settleBySrc, releaseBySrc, err := s.loadReservations(ctx)
	if err != nil {
		return false, err
	}
	s.c.reservations = reservations
	s.c.resBySource = resBySource
	s.c.settleBySrc = settleBySrc
	s.c.releaseBySrc = releaseBySrc

	decisions, err := s.loadDecisions(ctx)
	if err != nil {
		return false, err
	}
	s.c.decisions = decisions
	if s.c.nextDecision == 0 {
		s.c.nextDecision = 1
	}
	return true, nil
}

func (s *DurableStore) loadLimitRows(ctx context.Context) (map[string]*controlplane.AccountingLimitStatusRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT row_key, row_json FROM usage_authority_limit_rows WHERE store_id = ?`, s.c.storeID)
	if err != nil {
		return nil, storeUnavailableError("load limit rows", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*controlplane.AccountingLimitStatusRow{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load limit rows scan: %w", err)
		}
		var row controlplane.AccountingLimitStatusRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("authoritystore load limit rows decode: %w", err)
		}
		cp := row
		out[key] = &cp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authoritystore load limit rows iter: %w", err)
	}
	return out, nil
}

func (s *DurableStore) loadReservations(ctx context.Context) (map[string]*reservationRecord, map[string]string, map[string]string, map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT reservation_key, source_key, record_json FROM usage_authority_reservations WHERE store_id = ?`, s.c.storeID)
	if err != nil {
		return nil, nil, nil, nil, storeUnavailableError("load reservations", err)
	}
	defer func() { _ = rows.Close() }()
	reservations := map[string]*reservationRecord{}
	resBySource := map[string]string{}
	settleBySrc := map[string]string{}
	releaseBySrc := map[string]string{}
	for rows.Next() {
		var key, source, raw string
		if err := rows.Scan(&key, &source, &raw); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations scan: %w", err)
		}
		var rec reservationRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations decode: %w", err)
		}
		cp := rec
		reservations[key] = &cp
		if source != "" {
			resBySource[source] = key
		}
		if cp.Settled && source != "" {
			settleBySrc[source] = key
		}
		if cp.Released && source != "" {
			releaseBySrc[source] = key
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations iter: %w", err)
	}
	return reservations, resBySource, settleBySrc, releaseBySrc, nil
}

func (s *DurableStore) loadDecisions(ctx context.Context) ([]decisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT decision_seq, source_key, row_json FROM usage_authority_decisions WHERE store_id = ? ORDER BY decision_seq ASC`, s.c.storeID)
	if err != nil {
		return nil, storeUnavailableError("load decisions", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]decisionRecord, 0, 8)
	for rows.Next() {
		var seq int64
		var source, raw string
		if err := rows.Scan(&seq, &source, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load decisions scan: %w", err)
		}
		var row controlplane.AccountingDecisionRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("authoritystore load decisions decode: %w", err)
		}
		out = append(out, decisionRecord{Seq: seq, SourceKey: source, Row: row})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authoritystore load decisions iter: %w", err)
	}
	return out, nil
}

func (s *DurableStore) String() string {
	if s == nil || s.c == nil {
		return defaultStoreID
	}
	return strings.TrimSpace(s.c.storeID)
}

func unavailableError(op string) error {
	return fmt.Errorf("authoritystore %s: %w", op, app.ErrUnavailable)
}

func storeUnavailableError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("authoritystore %s: %w: %w", op, app.ErrUnavailable, err)
}
