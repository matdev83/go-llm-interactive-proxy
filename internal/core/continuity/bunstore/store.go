package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("bunstore: %s: %w", op, err)
}

// Store persists A-leg rows, B-leg allocations, and attempt lineage using Bun.
type Store struct {
	db *bun.DB
}

var _ b2bua.Store = (*Store)(nil)

// New returns a Store backed by db after applying schema. Closing the store closes the underlying sql.DB.
// ctx bounds migrate DDL; use [NewContext] for explicit cancellation/timeouts.
func New(db *bun.DB) (*Store, error) {
	return NewContext(context.Background(), db)
}

// NewContext returns a Store backed by db after applying schema, honoring ctx for migrate DDL.
func NewContext(ctx context.Context, db *bun.DB) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("bunstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("bunstore: nil bun db")
	}
	s := &Store{db: db}
	if err := runContinuitySchemaMigrate(ctx, db); err != nil {
		return nil, opErr("new", err)
	}
	return s, nil
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
	var rec b2bua.ALegRecord
	var created, lastSeen int64
	var wfc int
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		err := tx.NewRaw(`
SELECT a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed
FROM a_legs WHERE continuity_key = ?
`, continuityKey).Scan(ctx, &rec.ALegID, &rec.ContinuityKey, &created, &lastSeen, &wfc)
		if errors.Is(err, sql.ErrNoRows) {
			return b2bua.ErrALegNotFound
		}
		if err != nil {
			return opErr("resolve a leg select", err)
		}
		_, err = tx.NewRaw(`
UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?
`, now, rec.ALegID).Exec(ctx)
		if err != nil {
			return opErr("resolve a leg update last seen", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, b2bua.ErrALegNotFound) {
			return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
		}
		return b2bua.ALegRecord{}, err
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
	var out b2bua.ALegRecord
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		aID, err := b2bua.RandomALegID()
		if err != nil {
			return err
		}
		if continuityKey != "" {
			_, err := tx.NewRaw(`DELETE FROM a_legs WHERE continuity_key = ?`, continuityKey).Exec(ctx)
			if err != nil {
				return opErr("create a leg delete prior continuity", err)
			}
		}
		_, err = tx.NewRaw(`
INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq)
VALUES(?,?,?,?,0,0)
`, aID, continuityKey, now, now).Exec(ctx)
		if err != nil {
			return opErr("create a leg insert", err)
		}
		out = b2bua.ALegRecord{
			ALegID:        aID,
			ContinuityKey: continuityKey,
			CreatedAt:     time.Unix(0, now),
			LastSeenAt:    time.Unix(0, now),
		}
		return nil
	})
	if err != nil {
		return b2bua.ALegRecord{}, err
	}
	return out, nil
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
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var created, lastSeen int64
		var wfc int
		err := tx.NewRaw(`
SELECT a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed
FROM a_legs WHERE a_leg_id = ?
`, aLegID).Scan(ctx, &rec.ALegID, &rec.ContinuityKey, &created, &lastSeen, &wfc)
		if errors.Is(err, sql.ErrNoRows) {
			return b2bua.ErrALegNotFound
		}
		if err != nil {
			return opErr("fetch a leg select", err)
		}
		_, err = tx.NewRaw(`
UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?
`, now, aLegID).Exec(ctx)
		if err != nil {
			return opErr("fetch a leg update last seen", err)
		}
		rec.CreatedAt = time.Unix(0, created)
		rec.LastSeenAt = time.Unix(0, now)
		rec.WeightedFirstConsumed = wfc != 0
		return nil
	})
	if err != nil {
		if errors.Is(err, b2bua.ErrALegNotFound) {
			return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
		}
		return b2bua.ALegRecord{}, err
	}
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
	res, err := s.db.NewRaw(`
UPDATE a_legs SET weighted_first_consumed = ?, last_seen_at_unix = ? WHERE a_leg_id = ?
`, v, time.Now().UnixNano(), aLegID).Exec(ctx)
	if err != nil {
		return opErr("set weighted first consumed update", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return opErr("set weighted first consumed rows affected", err)
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
	var out b2bua.BLegRecord
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now().UnixNano()
		switch s.db.Dialect().Name() {
		case dialect.PG:
			var nextSeq int
			err := tx.NewRaw(`
UPDATE a_legs SET next_b_seq = next_b_seq + 1, last_seen_at_unix = ?
WHERE a_leg_id = ? AND next_b_seq < ?
RETURNING next_b_seq
`, now, aLegID, math.MaxInt).Scan(ctx, &nextSeq)
			if errors.Is(err, sql.ErrNoRows) {
				var cur int
				err2 := tx.NewRaw(`SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &cur)
				if errors.Is(err2, sql.ErrNoRows) {
					return b2bua.ErrALegNotFound
				}
				if err2 != nil {
					return opErr("next b leg pg followup select", err2)
				}
				if cur >= math.MaxInt {
					return opErr("next b leg seq cap", fmt.Errorf("b-leg sequence overflow"))
				}
				return opErr("next b leg pg allocate", fmt.Errorf("unexpected empty update"))
			}
			if err != nil {
				return opErr("next b leg pg update returning", err)
			}
			bID, err := b2bua.RandomBLegID()
			if err != nil {
				return err
			}
			_, err = tx.NewRaw(`
INSERT INTO b_legs(a_leg_id, seq, b_leg_id) VALUES(?,?,?)
`, aLegID, nextSeq, bID).Exec(ctx)
			if err != nil {
				return opErr("next b leg insert b leg", err)
			}
			out = b2bua.BLegRecord{BLegID: bID, ALegID: aLegID, Seq: nextSeq}
			return nil
		default:
			var nextSeq int
			err := tx.NewRaw(`SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &nextSeq)
			if errors.Is(err, sql.ErrNoRows) {
				return b2bua.ErrALegNotFound
			}
			if err != nil {
				return opErr("next b leg select seq", err)
			}
			if nextSeq == math.MaxInt {
				return opErr("next b leg seq cap", fmt.Errorf("b-leg sequence overflow"))
			}
			nextSeq++
			bID, err := b2bua.RandomBLegID()
			if err != nil {
				return err
			}
			_, err = tx.NewRaw(`
UPDATE a_legs SET next_b_seq = ?, last_seen_at_unix = ? WHERE a_leg_id = ?
`, nextSeq, now, aLegID).Exec(ctx)
			if err != nil {
				return opErr("next b leg update a leg", err)
			}
			_, err = tx.NewRaw(`
INSERT INTO b_legs(a_leg_id, seq, b_leg_id) VALUES(?,?,?)
`, aLegID, nextSeq, bID).Exec(ctx)
			if err != nil {
				return opErr("next b leg insert b leg", err)
			}
			out = b2bua.BLegRecord{BLegID: bID, ALegID: aLegID, Seq: nextSeq}
			return nil
		}
	})
	if err != nil {
		if errors.Is(err, b2bua.ErrALegNotFound) {
			return b2bua.BLegRecord{}, b2bua.ErrALegNotFound
		}
		return b2bua.BLegRecord{}, err
	}
	return out, nil
}

func (s *Store) RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.ALegID == "" || rec.Seq <= 0 || rec.BLegID == "" {
		return fmt.Errorf("%w: missing ids or seq", b2bua.ErrInvalidAttempt)
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var want string
		err := tx.NewRaw(`SELECT b_leg_id FROM b_legs WHERE a_leg_id = ? AND seq = ?`, rec.ALegID, rec.Seq).Scan(ctx, &want)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no b-leg for seq %d", b2bua.ErrInvalidAttempt, rec.Seq)
		}
		if err != nil {
			return opErr("record attempt select b leg", err)
		}
		if want != rec.BLegID {
			return fmt.Errorf("%w: b-leg mismatch for seq %d", b2bua.ErrInvalidAttempt, rec.Seq)
		}
		touch := time.Now().UnixNano()
		_, err = tx.NewRaw(`
INSERT INTO attempts(a_leg_id, seq, b_leg_id, backend_id, effective_model, started_at_unix, finished_at_unix, outcome, reason)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(a_leg_id, seq) DO UPDATE SET
	b_leg_id=excluded.b_leg_id,
	backend_id=excluded.backend_id,
	effective_model=excluded.effective_model,
	started_at_unix=excluded.started_at_unix,
	finished_at_unix=excluded.finished_at_unix,
	outcome=excluded.outcome,
	reason=excluded.reason
`,
			rec.ALegID, rec.Seq, rec.BLegID, rec.BackendID, rec.EffectiveModel,
			rec.StartedAt.UnixNano(), rec.FinishedAt.UnixNano(), string(rec.Outcome), rec.Reason,
		).Exec(ctx)
		if err != nil {
			return opErr("record attempt upsert", err)
		}
		_, err = tx.NewRaw(`UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, touch, rec.ALegID).Exec(ctx)
		return opErr("record attempt touch a leg", err)
	})
}

func (s *Store) LoadAttempts(ctx context.Context, aLegID string) (out []lipapi.AttemptRecord, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	defer func() {
		if rows == nil {
			return
		}
		if cerr := rows.Close(); cerr != nil {
			ce := opErr("load attempts close rows", cerr)
			if err == nil {
				err = ce
			} else {
				err = errors.Join(err, ce)
			}
		}
	}()
	rows, err = s.db.QueryContext(ctx, `
SELECT b_leg_id, a_leg_id, seq, backend_id, effective_model, started_at_unix, finished_at_unix, outcome, reason
FROM attempts WHERE a_leg_id = ? ORDER BY seq ASC
`, aLegID)
	if err != nil {
		return nil, opErr("load attempts query", err)
	}
	out = make([]lipapi.AttemptRecord, 0, 8)
	for rows.Next() {
		var r lipapi.AttemptRecord
		var st, ft int64
		var oc string
		if err = rows.Scan(
			&r.BLegID,
			&r.ALegID,
			&r.Seq,
			&r.BackendID,
			&r.EffectiveModel,
			&st,
			&ft,
			&oc,
			&r.Reason,
		); err != nil {
			return nil, opErr("load attempts scan", err)
		}
		r.StartedAt = time.Unix(0, st)
		r.FinishedAt = time.Unix(0, ft)
		r.Outcome = lipapi.AttemptOutcome(oc)
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, opErr("load attempts rows", err)
	}
	if len(out) == 0 {
		var one string
		err = s.db.NewRaw(`SELECT 1 FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, b2bua.ErrALegNotFound
		}
		if err != nil {
			return nil, opErr("load attempts verify a leg", err)
		}
	}
	_, err = s.db.NewRaw(`UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, time.Now().UnixNano(), aLegID).Exec(ctx)
	if err != nil {
		return nil, opErr("load attempts touch a leg", err)
	}
	return out, nil
}
