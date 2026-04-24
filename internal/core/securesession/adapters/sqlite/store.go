// Package sqlite implements securesession app.Store on SQLite for durable secure sessions.
// The blank import of modernc.org/sqlite registers the "sqlite" driver for database/sql;
// linking this package is enough for Open/New to work without an extra import at cmd.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	_ "modernc.org/sqlite" // register "sqlite" driver name
)

// Store persists secure-session state in SQLite.
type Store struct {
	db *sql.DB
}

var (
	_ app.Store              = (*Store)(nil)
	_ app.SessionUsageRollup = (*Store)(nil)
)

func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("securesession/sqlite %s: %w", op, err)
}

// dsnFromPath builds a modernc file URI with busy_timeout and foreign_keys (shared with continuity style).
func dsnFromPath(path string) (string, error) {
	p := strings.ReplaceAll(strings.TrimSpace(path), `\`, `/`)
	if strings.ContainsAny(p, "\x00?#&") {
		return "", fmt.Errorf("securesession/sqlite: path contains invalid character")
	}
	return "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", nil
}

// Open opens (creating if needed) a SQLite-backed secure-session store at path.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("securesession/sqlite: empty path")
	}
	dsn, err := dsnFromPath(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("securesession/sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s, err := New(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// New returns a Store backed by db after applying secure-session migrations. Closing the store closes db.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("securesession/sqlite: nil db")
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		return nil, opErr("migrate", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func mapUniqueErr(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "unique constraint failed") {
		return err
	}
	if strings.Contains(msg, "resume_fingerprint") ||
		strings.Contains(msg, "idx_lip_secure_sessions_resume_fp") ||
		strings.Contains(msg, "lip_secure_sessions.resume_fingerprint") {
		return domain.ErrDuplicateFingerprint
	}
	return domain.ErrDuplicateSessionID
}

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
	return s.LoadByID(ctx, rec.SessionID)
}

func (s *Store) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	FROM lip_secure_sessions WHERE session_id = ?`, string(id))
	return scanRecord(row)
}

func (s *Store) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	FROM lip_secure_sessions WHERE resume_fingerprint = ?`, fp[:])
	return scanRecord(row)
}

func (s *Store) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	FROM lip_secure_sessions WHERE a_leg_id = ?`, aLegID)
	return scanRecord(row)
}

type sessionScanRow interface {
	Scan(dest ...any) error
}

func scanRecord(row sessionScanRow) (domain.Record, error) {
	var (
		sid, ownerID, ownerIssuer, ownerTenant string
		wsID, clientSID, agentDigest           string
		policyVer, effTreat, strictPol         string
		routeHint, redactProf, auditMode       string
		aLegID, lastActSrc                     string
		fpBlob                                 []byte
		te, re                                 int
		lastActUnix, createdUnix               int64
		usageIn, usageOut                      int64
		attemptCount                           int
		traceJ, outcomeJ, acctJ                string
	)
	err := row.Scan(
		&sid, &fpBlob,
		&ownerID, &ownerIssuer, &ownerTenant,
		&wsID, &clientSID, &agentDigest,
		&policyVer, &te, &effTreat, &strictPol,
		&routeHint, &redactProf, &auditMode,
		&aLegID, &re,
		&lastActUnix, &lastActSrc, &createdUnix,
		&usageIn, &usageOut, &attemptCount,
		&traceJ, &outcomeJ, &acctJ,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Record{}, domain.ErrSessionNotFound
	}
	if err != nil {
		return domain.Record{}, opErr("scan session", err)
	}
	if len(fpBlob) != len(domain.TokenFingerprint{}) {
		return domain.Record{}, fmt.Errorf("securesession/sqlite: bad fingerprint length %d", len(fpBlob))
	}
	var fp domain.TokenFingerprint
	copy(fp[:], fpBlob)
	rec := domain.Record{
		SessionID:         domain.SessionID(sid),
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: ownerID, Issuer: ownerIssuer, Tenant: ownerTenant},
		Workspace:         domain.WorkspaceRef{ID: wsID},
		ClientHints:       domain.ClientHints{ClientSessionID: clientSID, AgentIdentityDigest: agentDigest},
		Policy: domain.PolicyMetadata{
			PolicyVersion:            policyVer,
			TranscriptEnabled:        te != 0,
			EffectiveTreatment:       effTreat,
			StricterPolicyResolution: strictPol,
			RouteHint:                routeHint,
			RedactionProfile:         redactProf,
			AuditMode:                auditMode,
		},
		ALegID:             aLegID,
		ResumeEligible:     re != 0,
		LastActivityAt:     time.Unix(0, lastActUnix),
		LastActivitySource: domain.ActivitySource(lastActSrc),
		CreatedAt:          time.Unix(0, createdUnix),
	}
	if err := json.Unmarshal([]byte(traceJ), &rec.LatestAttemptTrace); err != nil {
		return domain.Record{}, opErr("decode trace json", err)
	}
	if err := json.Unmarshal([]byte(outcomeJ), &rec.LatestAttemptOutcome); err != nil {
		return domain.Record{}, opErr("decode outcome json", err)
	}
	if err := json.Unmarshal([]byte(acctJ), &rec.LatestAttemptAccounting); err != nil {
		return domain.Record{}, opErr("decode accounting json", err)
	}
	_ = usageIn
	_ = usageOut
	_ = attemptCount
	return rec, nil
}

func (s *Store) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nano := at.UnixNano()
	// Monotonic merge: keep max(last_activity_unix) so concurrent out-of-order touches cannot regress time.
	res, err := s.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET
		last_activity_unix = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_unix END,
		last_activity_source = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_source END
		WHERE session_id = ?`, nano, nano, nano, string(source), string(id))
	if err != nil {
		return opErr("touch", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

func (s *Store) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opErr("append trace begin", err)
	}
	defer func() { _ = tx.Rollback() }()

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
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return tx.Commit()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opErr("update outcome begin", err)
	}
	defer func() { _ = tx.Rollback() }()

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
		string(outcomeJSON),
		string(outcome.SessionID), string(outcome.TurnID), outcome.BLegID,
	)
	if err != nil {
		return opErr("update attempt trace outcome", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET latest_attempt_outcome_json = ?
		WHERE session_id = ?`, outcomeJSON, string(outcome.SessionID)); err != nil {
		return opErr("update session latest outcome", err)
	}
	return tx.Commit()
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
	return max + 1, nil
}

func (s *Store) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opErr("transcript begin", err)
	}
	defer func() { _ = tx.Rollback() }()

	var te int
	err = tx.QueryRowContext(ctx, `SELECT transcript_enabled FROM lip_secure_sessions WHERE session_id = ?`,
		string(item.SessionID)).Scan(&te)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrSessionNotFound
	}
	if err != nil {
		return opErr("transcript policy read", err)
	}
	if te == 0 {
		return domain.ErrTranscriptDisabled
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lip_secure_transcript(
		session_id, seq, turn_id, event_kind, payload_ref, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
		string(item.SessionID), item.Seq, string(item.TurnID), item.EventKind, item.PayloadRef, item.CreatedAt.UnixNano(),
	)
	if err != nil {
		return mapUniqueErr(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO lip_secure_turns(session_id, turn_id) VALUES(?,?)`,
		string(item.SessionID), string(item.TurnID))
	if err != nil {
		return opErr("insert turn", err)
	}
	return tx.Commit()
}

func (s *Store) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opErr("usage begin", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE lip_secure_sessions SET
		usage_in = usage_in + ?, usage_out = usage_out + ?
		WHERE session_id = ?`, delta.InputTokens, delta.OutputTokens, string(delta.SessionID))
	if err != nil {
		return opErr("usage update totals", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
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
	return tx.Commit()
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
	return max + 1, nil
}

func (s *Store) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO lip_secure_audit(
		session_id, seq, turn_id, action, result, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
		string(item.SessionID), item.Seq, string(item.TurnID), item.Action, item.Result, item.CreatedAt.UnixNano(),
	)
	if err != nil {
		if isFKConstraintErr(err) {
			return domain.ErrSessionNotFound
		}
		return opErr("append audit", err)
	}
	return nil
}

func isFKConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key constraint failed")
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
	out := make([]domain.AuditItem, 0, opts.Limit)
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
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM lip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
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
	var te int
	if err := s.db.QueryRowContext(ctx, `SELECT transcript_enabled FROM lip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&te); err != nil {
		return nil, opErr("transcript policy", err)
	}
	if te == 0 {
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
	out := make([]domain.TranscriptItem, 0, opts.Limit)
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
	defer func() { _ = attRows.Close() }()
	out := make([]domain.AttemptEvidence, 0, 16)
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
			_ = json.Unmarshal([]byte(settingsJ), &settings)
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
	var cond []string
	var args []any
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

// UsageTokenTotals implements [github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app.SessionUsageRollup].
func (s *Store) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	var in, out int64
	err := s.db.QueryRowContext(ctx,
		`SELECT usage_in, usage_out FROM lip_secure_sessions WHERE session_id = ?`, string(id),
	).Scan(&in, &out)
	if errors.Is(err, sql.ErrNoRows) {
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
	// Durable SQLite: never fail mandatory audit solely for non-durable storage (contrast memory.Options.SimulateDurable).
	return nil
}
