package bunstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/driver/pgdriver"
	libsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("securesession/bunstore %s: %w", op, err)
}

// Store persists secure-session state using Bun.
type Store struct {
	db   *bun.DB
	meta *sessionMetaCache
}

var (
	_ app.Store              = (*Store)(nil)
	_ app.SessionUsageRollup = (*Store)(nil)
)

// New returns a Store backed by db after applying schema. Closing the store closes the underlying sql.DB.
// ctx for migrate is [context.Background]; prefer [NewContext] from composition roots.
func New(db *bun.DB) (*Store, error) {
	return NewContext(context.Background(), db)
}

// NewContext returns a Store backed by db after applying schema, honoring ctx for migrate DDL.
func NewContext(ctx context.Context, db *bun.DB) (*Store, error) {
	return NewContextWithOptions(ctx, db, Options{})
}

// NewContextWithOptions returns a Store like [NewContext] with optional tuning.
func NewContextWithOptions(ctx context.Context, db *bun.DB, opts Options) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("securesession/bunstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("securesession/bunstore: nil bun db")
	}
	s := &Store{db: db}
	if err := runSecureSessionSchemaMigrate(ctx, db); err != nil {
		return nil, opErr("new", err)
	}
	if opts.SQLQueryCacheTTL > 0 {
		cap := uint64(opts.SQLQueryCacheMaxEntries)
		if cap == 0 {
			cap = 4096
		}
		s.meta = newSessionMetaCache(opts.SQLQueryCacheTTL, cap)
	}
	return s, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) invalidateSessionMetaCache(id domain.SessionID) {
	if s.meta != nil {
		s.meta.invalidate(id)
	}
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) transcriptEnabledCached(ctx context.Context, q rowQuerier, id domain.SessionID) (bool, error) {
	if s.meta != nil {
		if it := s.meta.transcript.Get(id); it != nil {
			return it.Value(), nil
		}
	}
	var te int
	err := q.QueryRowContext(ctx, `SELECT transcript_enabled FROM lip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&te)
	if err != nil {
		return false, err
	}
	en := te != 0
	if s.meta != nil {
		s.meta.transcript.Set(id, en, ttlcache.DefaultTTL)
	}
	return en, nil
}

func mapUniqueErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
		if uniqueErrIsResumeFingerprint(pgErr) {
			return domain.ErrDuplicateFingerprint
		}
		return domain.ErrDuplicateSessionID
	}
	return mapUniqueErrSQLite(err)
}

func uniqueErrIsResumeFingerprint(e pgdriver.Error) bool {
	var b strings.Builder
	b.Grow(128)
	b.WriteString(e.Field('n'))
	b.WriteByte(' ')
	b.WriteString(e.Field('M'))
	b.WriteByte(' ')
	b.WriteString(e.Field('D'))
	b.WriteByte(' ')
	b.WriteString(e.Field('s'))
	h := strings.ToLower(b.String())
	return strings.Contains(h, "resume_fingerprint") ||
		strings.Contains(h, "idx_lip_secure_sessions_resume_fp") ||
		strings.Contains(h, "lip_secure_sessions_resume_fingerprint")
}

func mapUniqueErrSQLite(err error) error {
	if err == nil {
		return nil
	}
	var se *libsqlite.Error
	if errors.As(err, &se) && se != nil && se.Code() == sqlite3.SQLITE_CONSTRAINT {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unique") {
			return classifySQLiteUniqueConstraint(msg)
		}
		return err
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "unique constraint failed") && !strings.Contains(msg, "unique constraint") {
		return err
	}
	return classifySQLiteUniqueConstraint(msg)
}

func classifySQLiteUniqueConstraint(msg string) error {
	if strings.Contains(msg, "resume_fingerprint") ||
		strings.Contains(msg, "idx_lip_secure_sessions_resume_fp") ||
		strings.Contains(msg, "lip_secure_sessions.resume_fingerprint") {
		return domain.ErrDuplicateFingerprint
	}
	return domain.ErrDuplicateSessionID
}

func isFKConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) && pgErr.Field('C') == "23503" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key constraint failed") ||
		strings.Contains(msg, "foreign key violation") ||
		strings.Contains(msg, "23503")
}

func (s *Store) insertTurnIgnore(ctx context.Context, tx bun.Tx, sessionID, turnID string) error {
	switch s.db.Dialect().Name() {
	case dialect.SQLite:
		_, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO lip_secure_turns(session_id, turn_id) VALUES(?,?)`,
			sessionID,
			turnID,
		)
		return err
	case dialect.PG:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO lip_secure_turns(session_id, turn_id) VALUES(?,?) ON CONFLICT DO NOTHING`,
			sessionID, turnID)
		return err
	default:
		return fmt.Errorf("securesession/bunstore: unsupported dialect for insert turn")
	}
}

const selectSession = `SELECT
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	FROM lip_secure_sessions WHERE `

func (s *Store) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	fp := rec.ResumeFingerprint[:]
	te := 0
	if rec.Policy.TranscriptEnabled {
		te = 1
	}
	re := 0
	if rec.ResumeEligible {
		re = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO lip_secure_sessions(
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,0,'{}','{}','{}')`,
		string(rec.SessionID), fp,
		rec.Owner.ID, rec.Owner.Issuer, rec.Owner.Tenant,
		rec.Workspace.ID, rec.ClientHints.ClientSessionID, rec.ClientHints.AgentIdentityDigest,
		rec.Policy.PolicyVersion, te, rec.Policy.EffectiveTreatment, rec.Policy.StricterPolicyResolution,
		rec.Policy.RouteHint, rec.Policy.RedactionProfile, rec.Policy.AuditMode,
		rec.ALegID, re,
		rec.CreatedAt.UnixNano(), string(domain.ActivitySystem), rec.CreatedAt.UnixNano(),
	)
	if err != nil {
		return domain.Record{}, mapUniqueErr(err)
	}
	if s.meta != nil {
		s.meta.seedAfterCreate(rec.SessionID, rec.Policy.TranscriptEnabled)
	}
	return s.LoadByID(ctx, rec.SessionID)
}

func (s *Store) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`session_id = ?`, string(id))
	return scanRecord(row)
}

func (s *Store) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`resume_fingerprint = ?`, fp[:])
	return scanRecord(row)
}

func (s *Store) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`a_leg_id = ?`, aLegID)
	return scanRecord(row)
}

func (s *Store) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nano := at.UnixNano()
	res, err := s.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET
		last_activity_unix = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_unix END,
		last_activity_source = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_source END
		WHERE session_id = ?`, nano, nano, nano, string(source), string(id))
	if err != nil {
		return opErr("touch", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return opErr("touch rows affected", err)
	}
	if n == 0 {
		s.invalidateSessionMetaCache(id)
		return domain.ErrSessionNotFound
	}
	return nil
}

func (s *Store) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		settingsJSON, err := json.Marshal(trace.Settings)
		if err != nil {
			return opErr("marshal settings", err)
		}
		traceJSON, err := json.Marshal(trace)
		if err != nil {
			return opErr("marshal trace", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO lip_secure_attempt_traces(
		session_id, turn_id, a_leg_id, b_leg_id, attempt_seq,
		requested_model, requested_alias, resolved_backend, resolved_model,
		route_source, route_reason, settings_json, started_at_unix
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(trace.SessionID), string(trace.TurnID), trace.ALegID, trace.BLegID, trace.AttemptSeq,
			trace.RequestedModel, trace.RequestedAlias, trace.ResolvedBackend, trace.ResolvedModel,
			trace.RouteSource, trace.RouteReason, settingsJSON, trace.StartedAt.UnixNano(),
		)
		if err != nil {
			if isFKConstraintErr(err) {
				s.invalidateSessionMetaCache(trace.SessionID)
				return domain.ErrSessionNotFound
			}
			return opErr("insert attempt trace", err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET
		attempt_count = attempt_count + 1,
		latest_attempt_trace_json = ?
		WHERE session_id = ?`, traceJSON, string(trace.SessionID))
		if err != nil {
			return opErr("update session trace", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("update session trace rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(trace.SessionID)
			return domain.ErrSessionNotFound
		}
		return nil
	})
}

func (s *Store) UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return opErr("marshal outcome", err)
	}
	success := 0
	if outcome.Success {
		success = 1
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE lip_secure_attempt_traces SET
		ended_at_unix = ?,
		success = ?,
		surface_state = ?,
		http_status = ?,
		provider_status = ?,
		error_code = ?,
		timeout_class = ?,
		debug_reason = ?,
		outcome_json = ?
		WHERE session_id = ? AND turn_id = ? AND b_leg_id = ?`,
			outcome.EndedAt.UnixNano(),
			success,
			string(outcome.SurfaceState),
			outcome.HTTPStatus,
			outcome.ProviderStatus,
			outcome.ErrorCode,
			outcome.TimeoutClass,
			outcome.DebugReason,
			outcomeJSON,
			string(outcome.SessionID), string(outcome.TurnID), outcome.BLegID,
		)
		if err != nil {
			return opErr("update attempt trace outcome", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("update attempt trace outcome rows affected", err)
		}
		if n == 0 {
			return domain.ErrSessionNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET latest_attempt_outcome_json = ?
		WHERE session_id = ?`, outcomeJSON, string(outcome.SessionID)); err != nil {
			return opErr("update session latest outcome", err)
		}
		return nil
	})
}

func (s *Store) NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, domain.ErrSessionNotFound
	}
	var max int64
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM lip_secure_transcript WHERE session_id = ?`, string(id)).Scan(&max)
	if err != nil {
		return 0, opErr("next transcript seq", err)
	}
	if max == math.MaxInt64 {
		return 0, opErr("next transcript seq", fmt.Errorf("transcript seq overflow"))
	}
	return max + 1, nil
}

func (s *Store) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Serialize per-session transcript seq: lock parent row, then allocate MAX(seq)+1 in the same transaction.
		res, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET last_activity_unix = last_activity_unix WHERE session_id = ?`,
			string(item.SessionID))
		if err != nil {
			return opErr("transcript lock session", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("transcript lock session rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(item.SessionID)
			return domain.ErrSessionNotFound
		}
		enabled, err := s.transcriptEnabledCached(ctx, tx, item.SessionID)
		if err != nil {
			return opErr("transcript policy read", err)
		}
		if !enabled {
			return domain.ErrTranscriptDisabled
		}
		var nextSeq int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM lip_secure_transcript WHERE session_id = ?`,
			string(item.SessionID)).Scan(&nextSeq)
		if err != nil {
			return opErr("transcript next seq", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO lip_secure_transcript(
		session_id, seq, turn_id, event_kind, payload_ref, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
			string(item.SessionID), nextSeq, string(item.TurnID), item.EventKind, item.PayloadRef, item.CreatedAt.UnixNano(),
		)
		if err != nil {
			return mapUniqueErr(err)
		}
		if err := s.insertTurnIgnore(ctx, tx, string(item.SessionID), string(item.TurnID)); err != nil {
			return opErr("insert turn", err)
		}
		return nil
	})
}

func (s *Store) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET
		usage_in = usage_in + ?, usage_out = usage_out + ?
		WHERE session_id = ?`, delta.InputTokens, delta.OutputTokens, string(delta.SessionID))
		if err != nil {
			return opErr("usage update totals", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("usage update totals rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(delta.SessionID)
			return domain.ErrSessionNotFound
		}
		now := time.Now().UnixNano()
		_, err = tx.ExecContext(ctx, `INSERT INTO lip_secure_usage(
		session_id, turn_id, b_leg_id, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, cost_minor_units, currency, billing_unavailable, created_at_unix
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			string(delta.SessionID), string(delta.TurnID), delta.BLegID,
			delta.InputTokens, delta.OutputTokens,
			delta.CacheReadTokens, delta.CacheWriteTokens, delta.CostMinorUnits, delta.Currency,
			boolToInt(delta.BillingUnavailable), now,
		)
		if err != nil {
			return opErr("usage insert row", err)
		}
		if delta.BLegID != "" {
			acct := domain.AttemptAccounting{
				BLegID:             delta.BLegID,
				InputTokens:        delta.InputTokens,
				OutputTokens:       delta.OutputTokens,
				CacheReadTokens:    delta.CacheReadTokens,
				CacheWriteTokens:   delta.CacheWriteTokens,
				CostMinorUnits:     delta.CostMinorUnits,
				Currency:           delta.Currency,
				BillingUnavailable: delta.BillingUnavailable,
			}
			acctJ, err := json.Marshal(acct)
			if err != nil {
				return opErr("marshal accounting", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET latest_attempt_accounting_json = ?
			WHERE session_id = ?`, acctJ, string(delta.SessionID)); err != nil {
				return opErr("usage accounting", err)
			}
		}
		return nil
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, domain.ErrSessionNotFound
	}
	var max int64
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM lip_secure_audit WHERE session_id = ?`, string(id)).Scan(&max)
	if err != nil {
		return 0, opErr("next audit seq", err)
	}
	if max == math.MaxInt64 {
		return 0, opErr("next audit seq", fmt.Errorf("audit seq overflow"))
	}
	return max + 1, nil
}

func (s *Store) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET last_activity_unix = last_activity_unix WHERE session_id = ?`,
			string(item.SessionID))
		if err != nil {
			return opErr("append audit lock session", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("append audit lock session rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(item.SessionID)
			return domain.ErrSessionNotFound
		}
		var nextSeq int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM lip_secure_audit WHERE session_id = ?`,
			string(item.SessionID)).Scan(&nextSeq)
		if err != nil {
			return opErr("append audit next seq", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO lip_secure_audit(
		session_id, seq, turn_id, action, result, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
			string(item.SessionID), nextSeq, string(item.TurnID), item.Action, item.Result, item.CreatedAt.UnixNano(),
		)
		if err != nil {
			if isFKConstraintErr(err) {
				s.invalidateSessionMetaCache(item.SessionID)
				return domain.ErrSessionNotFound
			}
			return opErr("append audit", err)
		}
		return nil
	})
}

func (s *Store) Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, opErr("audit exists", err)
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	q := `SELECT seq, turn_id, action, result, created_at_unix FROM lip_secure_audit
		WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{string(id), opts.AfterSeq}
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("audit query", err)
	}
	defer func() { _ = rows.Close() }()
	auditCap := opts.Limit
	if auditCap <= 0 {
		// Unbounded read when no LIMIT; give a small buffer to limit repeated growth.
		auditCap = 16
	}
	out := make([]domain.AuditItem, 0, auditCap)
	for rows.Next() {
		var (
			seq                    int64
			turnID, action, result string
			createdUnix            int64
		)
		if err := rows.Scan(&seq, &turnID, &action, &result, &createdUnix); err != nil {
			return nil, opErr("audit scan", err)
		}
		out = append(out, domain.AuditItem{
			SessionID: id, TurnID: domain.TurnID(turnID), Seq: seq,
			Action: action, Result: result, CreatedAt: time.Unix(0, createdUnix),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("audit rows", err)
	}
	return out, nil
}

func (s *Store) sessionExists(ctx context.Context, id domain.SessionID) (bool, error) {
	if s.meta != nil {
		if it := s.meta.exists.Get(id); it != nil {
			return it.Value(), nil
		}
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM lip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		if s.meta != nil {
			s.meta.exists.Set(id, false, ttlcache.DefaultTTL)
		}
		return false, nil
	}
	if err != nil {
		return false, opErr("session exists", err)
	}
	if s.meta != nil {
		s.meta.exists.Set(id, true, ttlcache.DefaultTTL)
	}
	return true, nil
}

func (s *Store) Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, opErr("transcript exists", err)
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	enabled, err := s.transcriptEnabledCached(ctx, s.db, id)
	if err != nil {
		return nil, opErr("transcript policy", err)
	}
	if !enabled {
		return []domain.TranscriptItem{}, nil
	}
	q := `SELECT seq, turn_id, event_kind, payload_ref, created_at_unix FROM lip_secure_transcript
		WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{string(id), opts.AfterSeq}
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("transcript query", err)
	}
	defer func() { _ = rows.Close() }()
	transcriptCap := opts.Limit
	if transcriptCap <= 0 {
		transcriptCap = 16
	}
	out := make([]domain.TranscriptItem, 0, transcriptCap)
	for rows.Next() {
		var seq int64
		var turnID, kind, payload string
		var createdUnix int64
		if err := rows.Scan(&seq, &turnID, &kind, &payload, &createdUnix); err != nil {
			return nil, opErr("transcript scan", err)
		}
		out = append(out, domain.TranscriptItem{
			SessionID: id, TurnID: domain.TurnID(turnID), Seq: seq,
			EventKind: kind, PayloadRef: payload, CreatedAt: time.Unix(0, createdUnix),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("transcript rows", err)
	}
	return out, nil
}

func (s *Store) ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	attRows, err := s.db.QueryContext(ctx, `
		SELECT turn_id, a_leg_id, b_leg_id, attempt_seq,
			requested_model, requested_alias, resolved_backend, resolved_model,
			route_source, route_reason, settings_json, started_at_unix,
			ended_at_unix, success, surface_state, http_status, provider_status,
			error_code, timeout_class, debug_reason
		FROM lip_secure_attempt_traces
		WHERE session_id = ?
		ORDER BY attempt_seq ASC
		LIMIT ?`, string(id), limit)
	if err != nil {
		return nil, opErr("list attempts query", err)
	}
	attClosed := false
	defer func() {
		if !attClosed {
			_ = attRows.Close()
		}
	}()
	out := make([]domain.AttemptEvidence, 0, limit)
	for attRows.Next() {
		var turnID, aLeg, bLeg string
		var attemptSeq int
		var reqModel, reqAlias, resBack, resModel, routeSrc, routeReason, settingsJ string
		var startedUnix, endedUnix int64
		var successInt int
		var surface, provStatus, errCode, timeoutClass, debugReason string
		var httpStatus int
		if err := attRows.Scan(&turnID, &aLeg, &bLeg, &attemptSeq, &reqModel, &reqAlias, &resBack, &resModel,
			&routeSrc, &routeReason, &settingsJ, &startedUnix, &endedUnix, &successInt, &surface, &httpStatus,
			&provStatus, &errCode, &timeoutClass, &debugReason); err != nil {
			return nil, opErr("list attempts scan", err)
		}
		var settings domain.AttemptSettings
		if strings.TrimSpace(settingsJ) != "" && settingsJ != "{}" {
			if err := json.Unmarshal([]byte(settingsJ), &settings); err != nil {
				return nil, opErr("list attempts settings json", err)
			}
		}
		tr := domain.AttemptTrace{
			SessionID:       id,
			TurnID:          domain.TurnID(turnID),
			ALegID:          aLeg,
			BLegID:          bLeg,
			AttemptSeq:      attemptSeq,
			RequestedModel:  reqModel,
			RequestedAlias:  reqAlias,
			ResolvedBackend: resBack,
			ResolvedModel:   resModel,
			RouteSource:     routeSrc,
			RouteReason:     routeReason,
			Settings:        settings,
			StartedAt:       time.Unix(0, startedUnix),
		}
		oc := domain.AttemptOutcome{
			SessionID:      id,
			TurnID:         domain.TurnID(turnID),
			BLegID:         bLeg,
			Success:        successInt != 0,
			SurfaceState:   domain.SurfaceState(surface),
			HTTPStatus:     httpStatus,
			ProviderStatus: provStatus,
			ErrorCode:      errCode,
			TimeoutClass:   timeoutClass,
			DebugReason:    debugReason,
			EndedAt:        time.Unix(0, endedUnix),
		}
		out = append(out, domain.AttemptEvidence{
			Trace:      tr,
			Outcome:    oc,
			Accounting: domain.AttemptAccounting{BLegID: bLeg},
		})
	}
	if err := attRows.Err(); err != nil {
		return nil, opErr("list attempts rows", err)
	}
	// Release first result set before opening another on the same pool (SQLite single connection).
	if err := attRows.Close(); err != nil {
		return nil, opErr("list attempts att close rows", err)
	}
	attClosed = true

	usageRows, err := s.db.QueryContext(ctx, `
		SELECT b_leg_id,
			COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
			COALESCE(SUM(cost_minor_units),0),
			MAX(currency) AS cur,
			MAX(billing_unavailable) AS bu
		FROM lip_secure_usage WHERE session_id = ? GROUP BY b_leg_id`, string(id))
	if err != nil {
		return nil, opErr("list attempts usage", err)
	}
	defer func() { _ = usageRows.Close() }()
	byLeg := make(map[string]domain.AttemptAccounting)
	for usageRows.Next() {
		var b string
		var inTok, outTok, cr, cw, cost int64
		var cur string
		var bu int
		if err := usageRows.Scan(&b, &inTok, &outTok, &cr, &cw, &cost, &cur, &bu); err != nil {
			return nil, opErr("list attempts usage scan", err)
		}
		byLeg[b] = domain.AttemptAccounting{
			BLegID:             b,
			InputTokens:        inTok,
			OutputTokens:       outTok,
			CacheReadTokens:    cr,
			CacheWriteTokens:   cw,
			CostMinorUnits:     cost,
			Currency:           cur,
			BillingUnavailable: bu != 0,
		}
	}
	if err := usageRows.Err(); err != nil {
		return nil, opErr("list attempts usage rows", err)
	}
	for i := range out {
		if ac, ok := byLeg[out[i].Trace.BLegID]; ok {
			out[i].Accounting = ac
		}
	}
	return out, nil
}

func (s *Store) Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT s.session_id, s.owner_id, s.workspace_id, s.last_activity_unix,
		s.attempt_count,
		(SELECT COUNT(1) FROM lip_secure_turns t WHERE t.session_id = s.session_id) AS turn_count,
		s.resume_eligible, s.a_leg_id, s.policy_version, s.transcript_enabled,
		s.redaction_profile, s.audit_mode, s.usage_in, s.usage_out
		FROM lip_secure_sessions s`
	cond := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if query.OwnerID != "" {
		cond = append(cond, `s.owner_id = ?`)
		args = append(args, query.OwnerID)
	}
	if query.WorkspaceID != "" {
		cond = append(cond, `s.workspace_id = ?`)
		args = append(args, query.WorkspaceID)
	}
	if len(cond) > 0 {
		q += ` WHERE ` + strings.Join(cond, ` AND `)
	}
	q += ` ORDER BY s.last_activity_unix DESC, s.session_id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("summary query", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.Summary, 0, limit)
	for rows.Next() {
		var sid, ownerID, wsID string
		var lastActUnix int64
		var attemptCount, turnCount int
		var resumeElig, transcriptEn int
		var aLeg, polVer, redProf, auditMode string
		var usageIn, usageOut int64
		if err := rows.Scan(&sid, &ownerID, &wsID, &lastActUnix, &attemptCount, &turnCount,
			&resumeElig, &aLeg, &polVer, &transcriptEn, &redProf, &auditMode, &usageIn, &usageOut); err != nil {
			return nil, opErr("summary scan", err)
		}
		out = append(out, domain.Summary{
			SessionID:      domain.SessionID(sid),
			OwnerID:        ownerID,
			WorkspaceID:    wsID,
			LastActivityAt: time.Unix(0, lastActUnix),
			TurnCount:      turnCount,
			AttemptCount:   attemptCount,

			ResumeEligible:    resumeElig != 0,
			ALegID:            aLeg,
			PolicyVersion:     polVer,
			TranscriptEnabled: transcriptEn != 0,
			RedactionProfile:  redProf,
			AuditMode:         auditMode,
			UsageInputTokens:  usageIn,
			UsageOutputTokens: usageOut,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("summary rows", err)
	}
	return out, nil
}

func (s *Store) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	var in, out int64
	err := s.db.QueryRowContext(ctx,
		`SELECT usage_in, usage_out FROM lip_secure_sessions WHERE session_id = ?`, string(id),
	).Scan(&in, &out)
	if errors.Is(err, sql.ErrNoRows) {
		s.invalidateSessionMetaCache(id)
		return 0, 0, domain.ErrSessionNotFound
	}
	if err != nil {
		return 0, 0, opErr("usage totals", err)
	}
	return in, out, nil
}

func (s *Store) CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = policy
	return nil
}
