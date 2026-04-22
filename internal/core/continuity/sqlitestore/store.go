// Package sqlitestore implements b2bua.Store on SQLite for durable continuity and attempt lineage.
// The blank import of modernc.org/sqlite in this file is intentional: it registers the "sqlite" driver
// for database/sql; see the import group comment. Linking this package (not only cmd/…)
// is enough to load the driver.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	// Blank import registers the "sqlite" driver for database/sql. It lives in this package
	// (not only in cmd/main) so any binary that links sqlitestore.Open gets a working driver
	// without an extra import at the composition root; documented exception to the usual
	// rule of keeping side-effect imports in main or tests only.
	_ "modernc.org/sqlite" // register "sqlite" driver name
)

// opErr wraps a database or I/O error with stable sqlitestore operation context.
func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlitestore: %s: %w", op, err)
}

// Open opens (creating if needed) a SQLite-backed store at path.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlitestore: empty path")
	}
	dsn, err := sqliteFileDSN(path)
	if err != nil {
		return nil, opErr("open dsn", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s, err := New(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// sqliteFileDSN builds a modernc.org/sqlite driver URI with pragma query parameters.
// path must not contain NUL, '?', '#', or '&' (validated at config load); backslashes become slashes.
func sqliteFileDSN(path string) (string, error) {
	p := strings.ReplaceAll(strings.TrimSpace(path), `\`, `/`)
	if strings.ContainsAny(p, "\x00?#&") {
		return "", fmt.Errorf("sqlitestore: sqlite path contains invalid character")
	}
	return "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", nil
}

// New returns a Store backed by db after applying schema migration. Closing the store closes db.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlitestore: nil db")
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, opErr("new", err)
	}
	return s, nil
}

// Store persists A-leg rows, B-leg allocations, and attempt lineage.
type Store struct {
	db *sql.DB
}

var _ b2bua.Store = (*Store)(nil)

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS a_legs (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			continuity_key TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			last_seen_at_unix INTEGER NOT NULL,
			weighted_first_consumed INTEGER NOT NULL DEFAULT 0,
			next_b_seq INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_a_legs_continuity
			ON a_legs(continuity_key) WHERE continuity_key != ''`,
		`CREATE TABLE IF NOT EXISTS b_legs (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS attempts (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			effective_model TEXT NOT NULL,
			started_at_unix INTEGER NOT NULL,
			finished_at_unix INTEGER NOT NULL,
			outcome TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(context.Background(), q); err != nil {
			return fmt.Errorf("sqlitestore migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ResolveALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.ALegRecord{}, err
	}
	continuityKey = strings.TrimSpace(continuityKey)
	if continuityKey == "" {
		return b2bua.ALegRecord{}, b2bua.ErrInvalidContinuityKey
	}
	now := time.Now().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b2bua.ALegRecord{}, opErr("resolve_a_leg begin tx", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rec b2bua.ALegRecord
	var created, lastSeen int64
	var wfc int
	err = tx.QueryRowContext(ctx, `SELECT a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed
		FROM a_legs WHERE continuity_key = ?`, continuityKey).Scan(&rec.ALegID, &rec.ContinuityKey, &created, &lastSeen, &wfc)
	if errors.Is(err, sql.ErrNoRows) {
		return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
	}
	if err != nil {
		return b2bua.ALegRecord{}, opErr("resolve_a_leg select", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, now, rec.ALegID); err != nil {
		return b2bua.ALegRecord{}, opErr("resolve_a_leg update last_seen", err)
	}
	if err := tx.Commit(); err != nil {
		return b2bua.ALegRecord{}, opErr("resolve_a_leg commit", err)
	}
	rec.CreatedAt = time.Unix(0, created)
	rec.LastSeenAt = time.Unix(0, now)
	rec.WeightedFirstConsumed = wfc != 0
	return rec, nil
}

func (s *Store) CreateALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.ALegRecord{}, err
	}
	continuityKey = strings.TrimSpace(continuityKey)
	now := time.Now().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b2bua.ALegRecord{}, opErr("create_a_leg begin tx", err)
	}
	defer func() { _ = tx.Rollback() }()

	aID, err := b2bua.RandomALegID()
	if err != nil {
		return b2bua.ALegRecord{}, err
	}

	if continuityKey != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM a_legs WHERE continuity_key = ?`, continuityKey); err != nil {
			return b2bua.ALegRecord{}, opErr("create_a_leg delete prior continuity", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq)
		VALUES(?,?,?,?,0,0)`, aID, continuityKey, now, now)
	if err != nil {
		return b2bua.ALegRecord{}, opErr("create_a_leg insert", err)
	}
	if err := tx.Commit(); err != nil {
		return b2bua.ALegRecord{}, opErr("create_a_leg commit", err)
	}
	return b2bua.ALegRecord{
		ALegID:        aID,
		ContinuityKey: continuityKey,
		CreatedAt:     time.Unix(0, now),
		LastSeenAt:    time.Unix(0, now),
	}, nil
}

func (s *Store) FetchALeg(ctx context.Context, aLegID string) (b2bua.ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.ALegRecord{}, err
	}
	if strings.TrimSpace(aLegID) == "" {
		return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
	}
	now := time.Now().UnixNano()
	var rec b2bua.ALegRecord
	var created, lastSeen int64
	var wfc int
	err := s.db.QueryRowContext(ctx, `SELECT a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed
		FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(&rec.ALegID, &rec.ContinuityKey, &created, &lastSeen, &wfc)
	if errors.Is(err, sql.ErrNoRows) {
		return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
	}
	if err != nil {
		return b2bua.ALegRecord{}, opErr("fetch_a_leg select", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, now, aLegID); err != nil {
		return b2bua.ALegRecord{}, opErr("fetch_a_leg update last_seen", err)
	}
	rec.CreatedAt = time.Unix(0, created)
	rec.LastSeenAt = time.Unix(0, now)
	rec.WeightedFirstConsumed = wfc != 0
	return rec, nil
}

func (s *Store) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v := 0
	if consumed {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE a_legs SET weighted_first_consumed = ?, last_seen_at_unix = ? WHERE a_leg_id = ?`,
		v, time.Now().UnixNano(), aLegID)
	if err != nil {
		return opErr("set_weighted_first_consumed update", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return opErr("set_weighted_first_consumed rows_affected", err)
	}
	if n == 0 {
		return b2bua.ErrALegNotFound
	}
	return nil
}

func (s *Store) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.BLegRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b2bua.BLegRecord{}, opErr("next_b_leg begin tx", err)
	}
	defer func() { _ = tx.Rollback() }()

	var nextSeq int
	err = tx.QueryRowContext(ctx, `SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(&nextSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return b2bua.BLegRecord{}, b2bua.ErrALegNotFound
	}
	if err != nil {
		return b2bua.BLegRecord{}, opErr("next_b_leg select seq", err)
	}
	nextSeq++
	bID, err := b2bua.RandomBLegID()
	if err != nil {
		return b2bua.BLegRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a_legs SET next_b_seq = ?, last_seen_at_unix = ? WHERE a_leg_id = ?`,
		nextSeq, time.Now().UnixNano(), aLegID); err != nil {
		return b2bua.BLegRecord{}, opErr("next_b_leg update a_leg", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO b_legs(a_leg_id, seq, b_leg_id) VALUES(?,?,?)`, aLegID, nextSeq, bID); err != nil {
		return b2bua.BLegRecord{}, opErr("next_b_leg insert b_leg", err)
	}
	if err := tx.Commit(); err != nil {
		return b2bua.BLegRecord{}, opErr("next_b_leg commit", err)
	}
	return b2bua.BLegRecord{BLegID: bID, ALegID: aLegID, Seq: nextSeq}, nil
}

func (s *Store) RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.ALegID == "" || rec.Seq <= 0 || rec.BLegID == "" {
		return fmt.Errorf("%w: missing ids or seq", b2bua.ErrInvalidAttempt)
	}
	var want string
	err := s.db.QueryRowContext(ctx, `SELECT b_leg_id FROM b_legs WHERE a_leg_id = ? AND seq = ?`, rec.ALegID, rec.Seq).Scan(&want)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no b-leg for seq %d", b2bua.ErrInvalidAttempt, rec.Seq)
	}
	if err != nil {
		return opErr("record_attempt select b_leg", err)
	}
	if want != rec.BLegID {
		return fmt.Errorf("%w: b-leg mismatch for seq %d", b2bua.ErrInvalidAttempt, rec.Seq)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO attempts(a_leg_id, seq, b_leg_id, backend_id, effective_model, started_at_unix, finished_at_unix, outcome, reason)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(a_leg_id, seq) DO UPDATE SET
			b_leg_id=excluded.b_leg_id,
			backend_id=excluded.backend_id,
			effective_model=excluded.effective_model,
			started_at_unix=excluded.started_at_unix,
			finished_at_unix=excluded.finished_at_unix,
			outcome=excluded.outcome,
			reason=excluded.reason`,
		rec.ALegID, rec.Seq, rec.BLegID, rec.BackendID, rec.EffectiveModel,
		rec.StartedAt.UnixNano(), rec.FinishedAt.UnixNano(), string(rec.Outcome), rec.Reason)
	if err != nil {
		return opErr("record_attempt upsert", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, time.Now().UnixNano(), rec.ALegID)
	return opErr("record_attempt touch a_leg", err)
}

func (s *Store) LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b_leg_id, a_leg_id, seq, backend_id, effective_model, started_at_unix, finished_at_unix, outcome, reason
		FROM attempts WHERE a_leg_id = ? ORDER BY seq ASC`, aLegID)
	if err != nil {
		return nil, opErr("load_attempts query", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]lipapi.AttemptRecord, 0, 8)
	for rows.Next() {
		var r lipapi.AttemptRecord
		var st, ft int64
		var oc string
		if err := rows.Scan(&r.BLegID, &r.ALegID, &r.Seq, &r.BackendID, &r.EffectiveModel, &st, &ft, &oc, &r.Reason); err != nil {
			return nil, opErr("load_attempts scan", err)
		}
		r.StartedAt = time.Unix(0, st)
		r.FinishedAt = time.Unix(0, ft)
		r.Outcome = lipapi.AttemptOutcome(oc)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("load_attempts rows", err)
	}
	if len(out) == 0 {
		var one string
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return nil, b2bua.ErrALegNotFound
		} else if err != nil {
			return nil, opErr("load_attempts verify a_leg", err)
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, time.Now().UnixNano(), aLegID)
	if err != nil {
		return nil, opErr("load_attempts touch a_leg", err)
	}
	return out, nil
}
