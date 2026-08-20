package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// UsageAppendOutboxRetirementMigrationName is the forward cutover marker. The
// 20260824000000 source remains immutable historical input; it is deliberately
// not registered for fresh installs.
const UsageAppendOutboxRetirementMigrationName = "20260829000000"

func registerUsageAppendOutboxRetirementMigration() {
	migrations.MustRegister(usageAppendOutboxRetirementSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageAppendOutboxRetirementSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage append outbox retirement: nil database")
	}
	exists, err := usageAppendOutboxTableExists(ctx, db)
	if err != nil {
		return fmt.Errorf("billing usage append outbox retirement: inspect source: %w", err)
	}
	if !exists {
		return nil
	}
	// Cutover has a central-only sink and performs the preserve-or-block drain
	// before the dialect-specific proof and DROP. It therefore cannot mistake a
	// local spool append for delivery into current usage storage.
	return (&DurableStore{db: db}).CutoverUsageAppendOutbox(ctx)
}

func usageAppendOutboxTableExists(ctx context.Context, db *bun.DB) (bool, error) {
	var count int
	var err error
	switch db.Dialect().Name() {
	case dialect.SQLite:
		err = db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'usage_append_outbox'`).Scan(ctx, &count)
	case dialect.PG:
		err = db.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'usage_append_outbox'`).Scan(ctx, &count)
	default:
		return false, fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	return count == 1, err
}

// Historical preserve-or-block adapter; runtime composition never calls this code.
const (
	usageAppendOutboxBaseRetryDelay = time.Second
	usageAppendOutboxMaxRetryDelay  = time.Hour
)

func (s *DurableStore) EnqueueCallUsageAppend(ctx context.Context, record billing.CallUsageRecord, cause string) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode failed call usage append: %w", err)
	}
	return s.enqueueUsageAppend(ctx, sealed.Key, billing.UsageAppendCall, sealed.CallID, payload, cause)
}

func (s *DurableStore) EnqueueCallLegUsageAppend(ctx context.Context, record billing.CallLegUsageRecord, cause string) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode failed call-leg usage append: %w", err)
	}
	return s.enqueueUsageAppend(ctx, sealed.Key, billing.UsageAppendLeg, sealed.CallID, payload, cause)
}

func (s *DurableStore) enqueueUsageAppend(ctx context.Context, key string, kind billing.UsageAppendKind, callID billing.BillingCallID, payload []byte, cause string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return err
	}
	cause = strings.TrimSpace(cause)
	now := time.Now().UTC()
	_, err := s.db.NewRaw(`INSERT INTO usage_append_outbox( append_key, kind, call_id, payload_json, status, attempt_count, next_attempt_at, last_error, created_at, updated_at ) VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?) ON CONFLICT(append_key) DO NOTHING`, key, string(kind), callID.String(), string(payload), now, cause, now, now).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: enqueue usage append: %w", err)
	}
	return nil
}

func (s *DurableStore) ListPendingUsageAppendWork(ctx context.Context, limit int) ([]billing.UsageAppendWork, error) {
	return s.listUsageAppendWork(ctx, limit, true)
}

// listAllPendingUsageAppendWork is used only by the destructive cutover. It
// includes deferred rows regardless of next_attempt_at; migration must not
// mistake a future retry schedule for proof of delivery.
func (s *DurableStore) listAllPendingUsageAppendWork(ctx context.Context, limit int) ([]billing.UsageAppendWork, error) {
	return s.listUsageAppendWork(ctx, limit, false)
}

func (s *DurableStore) listUsageAppendWork(ctx context.Context, limit int, dueOnly bool) ([]billing.UsageAppendWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if limit <= 0 {
		limit = 32
	}
	if err := s.pruneProcessedUsageAppendWork(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		return nil, err
	}
	type row struct {
		Key     string `bun:"append_key"`
		Kind    string `bun:"kind"`
		CallID  string `bun:"call_id"`
		Payload string `bun:"payload_json"`
	}
	query := `SELECT append_key, kind, call_id, payload_json FROM usage_append_outbox WHERE status = 'pending'`
	args := []any{}
	if dueOnly {
		query += ` AND next_attempt_at <= ?`
		args = append(args, time.Now().UTC())
	}
	query += ` ORDER BY next_attempt_at, updated_at, append_key LIMIT ?`
	args = append(args, limit)
	var rows []row
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list pending usage appends: %w", err)
	}
	out := make([]billing.UsageAppendWork, 0, len(rows))
	for _, item := range rows {
		callID, err := billing.ParseBillingCallID(item.CallID)
		if err != nil {
			return nil, fmt.Errorf("billingstore: parse usage append call ID: %w", err)
		}
		work := billing.UsageAppendWork{Key: item.Key, Kind: billing.UsageAppendKind(item.Kind)}
		switch work.Kind {
		case billing.UsageAppendCall:
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(item.Payload), &record); err != nil {
				return nil, fmt.Errorf("billingstore: decode failed call usage append: %w", err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return nil, fmt.Errorf("billingstore: seal failed call usage append: %w", err)
			}
			if sealed.Key != item.Key || sealed.CallID != callID {
				return nil, fmt.Errorf("billingstore: failed call usage append identity mismatch: %s", item.Key)
			}
			work.Call = &sealed
		case billing.UsageAppendLeg:
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(item.Payload), &record); err != nil {
				return nil, fmt.Errorf("billingstore: decode failed call-leg usage append: %w", err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return nil, fmt.Errorf("billingstore: seal failed call-leg usage append: %w", err)
			}
			if sealed.Key != item.Key || sealed.CallID != callID {
				return nil, fmt.Errorf("billingstore: failed call-leg usage append identity mismatch: %s", item.Key)
			}
			work.Leg = &sealed
		default:
			return nil, fmt.Errorf("billingstore: unsupported usage append kind %q", item.Kind)
		}
		out = append(out, work)
	}
	return out, nil
}

func (s *DurableStore) MarkUsageAppendProcessed(ctx context.Context, key string) error {
	return s.updateUsageAppendStatus(ctx, key, "processed", "", "pending")
}

func (s *DurableStore) FailUsageAppend(ctx context.Context, key, reason string) error {
	return s.updateUsageAppendStatus(ctx, key, "failed", strings.TrimSpace(reason), "pending")
}

func (s *DurableStore) updateUsageAppendStatus(ctx context.Context, key, status, reason, from string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("billingstore: usage append key is required")
	}
	result, err := s.db.NewRaw(`UPDATE usage_append_outbox SET status = ?, last_error = ?, updated_at = ? WHERE append_key = ? AND status = ?`, status, reason, time.Now().UTC(), key, from).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: update usage append status: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: usage append status rows affected: %w", err)
	} else if count == 0 {
		var existing string
		if scanErr := s.db.NewRaw(`SELECT status FROM usage_append_outbox WHERE append_key = ?`, key).Scan(ctx, &existing); errors.Is(scanErr, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: usage append work not found: %s", key)
		} else if scanErr != nil {
			return fmt.Errorf("billingstore: inspect usage append status: %w", scanErr)
		}
	}
	return nil
}

func (s *DurableStore) DeferUsageAppend(ctx context.Context, key, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("billingstore: usage append key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin usage append defer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var attempts int
	if err := tx.NewRaw(`UPDATE usage_append_outbox SET attempt_count = attempt_count + 1, last_error = ?, updated_at = ? WHERE append_key = ? AND status = 'pending' RETURNING attempt_count`, strings.TrimSpace(reason), now, key).Scan(ctx, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: usage append work not found or terminal: %s", key)
		}
		return fmt.Errorf("billingstore: defer usage append: %w", err)
	}
	delay := retryBackoffDelay(usageAppendOutboxBaseRetryDelay, usageAppendOutboxMaxRetryDelay, attempts)
	if _, err := tx.NewRaw(`UPDATE usage_append_outbox SET next_attempt_at = ? WHERE append_key = ? AND status = 'pending' AND attempt_count = ?`, now.Add(delay), key, attempts).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: schedule usage append retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit usage append defer: %w", err)
	}
	return nil
}

func (s *DurableStore) pruneProcessedUsageAppendWork(ctx context.Context, before time.Time) error {
	_, err := s.db.NewRaw(`
DELETE FROM usage_append_outbox
WHERE append_key IN (
	SELECT append_key FROM usage_append_outbox
	WHERE status = 'processed' AND updated_at < ?
	ORDER BY updated_at
	LIMIT 256
)`, before).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: prune processed usage appends: %w", err)
	}
	return nil
}

// Historical preserve-or-block adapter; runtime composition never calls this code.
// ErrUsageAppendDrainBlocked means historical central transport work could not
// be proven delivered. Callers must reconcile it before any destructive schema
// migration; rows are intentionally preserved.
var ErrUsageAppendDrainBlocked = errors.New("billingstore: usage append outbox drain blocked")

// DrainUsageAppendOutbox performs the preserve-or-block part of Phase 2
// cutover. It validates and replays every pending row into the current
// central call/leg records, treats an identical central replay as success, and
// refuses to claim completion for malformed, conflicting, or unresolved rows.
// Schema deletion is deliberately separate and must run its own dialect-locked
// migration critical section after this proof.
func (s *DurableStore) DrainUsageAppendOutbox(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: nil store", ErrUsageAppendDrainBlocked)
	}
	for {
		work, err := s.listAllPendingUsageAppendWork(ctx, 256)
		if err != nil {
			return fmt.Errorf("%w: read pending rows: %v", ErrUsageAppendDrainBlocked, err)
		}
		if len(work) == 0 {
			break
		}
		for _, item := range work {
			var replayErr error
			switch item.Kind {
			case billing.UsageAppendCall:
				if item.Call == nil {
					replayErr = errors.New("nil call payload")
				} else {
					replayErr = s.AppendCall(ctx, *item.Call)
				}
			case billing.UsageAppendLeg:
				if item.Leg == nil {
					replayErr = errors.New("nil leg payload")
				} else {
					replayErr = s.AppendLeg(ctx, *item.Leg)
				}
			default:
				replayErr = fmt.Errorf("unsupported kind %q", item.Kind)
			}
			if replayErr != nil {
				return fmt.Errorf("%w: key %s: %v", ErrUsageAppendDrainBlocked, item.Key, replayErr)
			}
			if err := s.MarkUsageAppendProcessed(ctx, item.Key); err != nil {
				return fmt.Errorf("%w: mark %s processed: %v", ErrUsageAppendDrainBlocked, item.Key, err)
			}
		}
	}
	if err := s.reconcileProcessedUsageAppend(ctx); err != nil {
		return fmt.Errorf("%w: reconcile processed rows: %v", ErrUsageAppendDrainBlocked, err)
	}
	var unresolved int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM usage_append_outbox WHERE status NOT IN ('processed')`).Scan(ctx, &unresolved); err != nil {
		return fmt.Errorf("%w: verify unresolved rows: %v", ErrUsageAppendDrainBlocked, err)
	}
	if unresolved != 0 {
		return fmt.Errorf("%w: %d unresolved rows remain", ErrUsageAppendDrainBlocked, unresolved)
	}
	return nil
}

func (s *DurableStore) reconcileProcessedUsageAppend(ctx context.Context) error {
	type row struct {
		Key     string `bun:"append_key"`
		Kind    string `bun:"kind"`
		Payload string `bun:"payload_json"`
	}
	var rows []row
	if err := s.db.NewRaw(`SELECT append_key, kind, payload_json FROM usage_append_outbox WHERE status = 'processed' ORDER BY append_key`).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, row := range rows {
		switch billing.UsageAppendKind(row.Kind) {
		case billing.UsageAppendCall:
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
				return fmt.Errorf("processed call %s: %w", row.Key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return fmt.Errorf("processed call %s: %w", row.Key, err)
			}
			if sealed.Key != row.Key {
				return fmt.Errorf("processed call identity mismatch: %s", row.Key)
			}
			if err := s.AppendCall(ctx, sealed); err != nil {
				return fmt.Errorf("processed call %s replay: %w", row.Key, err)
			}
		case billing.UsageAppendLeg:
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
				return fmt.Errorf("processed leg %s: %w", row.Key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return fmt.Errorf("processed leg %s: %w", row.Key, err)
			}
			if sealed.Key != row.Key {
				return fmt.Errorf("processed leg identity mismatch: %s", row.Key)
			}
			if err := s.AppendLeg(ctx, sealed); err != nil {
				return fmt.Errorf("processed leg %s replay: %w", row.Key, err)
			}
		default:
			return fmt.Errorf("processed row %s has unsupported kind %q", row.Key, row.Kind)
		}
	}
	return nil
}

// UsageAppendOutboxUnresolved reports the destructive-migration proof input.
// It is intentionally a read-only query and does not silently classify failed
// or malformed work as obsolete.
func (s *DurableStore) UsageAppendOutboxUnresolved(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("billingstore: nil store")
	}
	var count int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM usage_append_outbox WHERE status NOT IN ('processed')`).Scan(ctx, &count); err != nil {
		// After an explicit successful cutover the source schema is gone; its
		// unresolved proof is therefore the empty set.
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no such table") || strings.Contains(lower, "does not exist") {
			return 0, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return count, nil
}

// Historical preserve-or-block adapter; runtime composition never calls this code.
// AppendCall and AppendLeg are used only by the explicit historical drain.
// They are intentionally bound to DurableStore so a local spool cannot be
// supplied as proof of central delivery.
func (s *DurableStore) AppendCall(ctx context.Context, record billing.CallUsageRecord) error {
	return s.AppendCallUsage(ctx, record)
}

func (s *DurableStore) AppendLeg(ctx context.Context, record billing.CallLegUsageRecord) error {
	return s.AppendCallLegUsage(ctx, record)
}

// ErrUsageAppendCutoverBlocked is returned when destructive retirement cannot
// prove that every legacy append has been delivered and reconciled.
var ErrUsageAppendCutoverBlocked = errors.New("billingstore: usage append outbox cutover blocked")

// CutoverUsageAppendOutbox drains legacy pending work and then retires the
// schema in one dialect-specific migration critical section. The historical
// migration which introduced the table remains immutable; this operation is
// the explicit, operator-invoked destructive cutover.
func (s *DurableStore) CutoverUsageAppendOutbox(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: nil store", ErrUsageAppendCutoverBlocked)
	}
	exists, err := usageAppendOutboxTableExists(ctx, s.db)
	if err != nil {
		return fmt.Errorf("%w: inspect source: %v", ErrUsageAppendCutoverBlocked, err)
	}
	if !exists {
		return nil
	}
	if err := s.DrainUsageAppendOutbox(ctx); err != nil {
		return fmt.Errorf("%w: drain: %v", ErrUsageAppendCutoverBlocked, err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: reserve migration connection: %v", ErrUsageAppendCutoverBlocked, err)
	}
	defer func() { _ = conn.Close() }()

	begin := cutoverBeginStatement(s.db.Dialect().Name())
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return fmt.Errorf("%w: begin critical section: %v", ErrUsageAppendCutoverBlocked, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if lock := cutoverLockStatement(s.db.Dialect().Name()); lock != "" {
		if _, err := conn.ExecContext(ctx, lock); err != nil {
			return fmt.Errorf("%w: lock legacy outbox: %v", ErrUsageAppendCutoverBlocked, err)
		}
	}
	var unresolved int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM usage_append_outbox WHERE status NOT IN ('processed')`).Scan(&unresolved); err != nil {
		return fmt.Errorf("%w: final unresolved proof: %v", ErrUsageAppendCutoverBlocked, err)
	}
	if unresolved != 0 {
		return fmt.Errorf("%w: %d unresolved rows remain", ErrUsageAppendCutoverBlocked, unresolved)
	}
	if err := reconcileProcessedInCriticalSection(ctx, conn, s.db.Dialect().Name()); err != nil {
		return fmt.Errorf("%w: source-key/fingerprint reconciliation: %v", ErrUsageAppendCutoverBlocked, err)
	}

	if _, err := conn.ExecContext(ctx, "DROP TABLE usage_append_outbox"); err != nil {
		return fmt.Errorf("%w: drop legacy outbox: %v", ErrUsageAppendCutoverBlocked, err)
	}
	if err := commitMigration(ctx, conn); err != nil {
		return fmt.Errorf("%w: commit critical section: %v", ErrUsageAppendCutoverBlocked, err)
	}
	committed = true
	return nil
}

func reconcileProcessedInCriticalSection(ctx context.Context, conn bun.Conn, dialectName dialect.Name) error {
	rows, err := conn.QueryContext(ctx, `SELECT append_key, kind, payload_json FROM usage_append_outbox WHERE status = 'processed' ORDER BY append_key`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, kind, payload string
		if err := rows.Scan(&key, &kind, &payload); err != nil {
			return err
		}
		var recordKey, fingerprint string
		switch billing.UsageAppendKind(kind) {
		case billing.UsageAppendCall:
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(payload), &record); err != nil {
				return fmt.Errorf("call %s payload: %w", key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return err
			}
			recordKey, fingerprint = sealed.Key, sealed.Fingerprint
		case billing.UsageAppendLeg:
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(payload), &record); err != nil {
				return fmt.Errorf("leg %s payload: %w", key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return err
			}
			recordKey, fingerprint = sealed.Key, sealed.Fingerprint
		default:
			return fmt.Errorf("row %s has unsupported kind %q", key, kind)
		}
		if recordKey != key {
			return fmt.Errorf("row %s identity mismatch", key)
		}
		table, keyColumn := "usage_call_records", "usage_call_key"
		if billing.UsageAppendKind(kind) == billing.UsageAppendLeg {
			table, keyColumn = "usage_leg_records", "usage_leg_key"
		}
		var stored string
		query := "SELECT fingerprint FROM " + table + " WHERE " + keyColumn + " = " + cutoverPlaceholder(dialectName, 1)
		if err := conn.QueryRowContext(ctx, query, key).Scan(&stored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("row %s has no proven central effect", key)
			}
			return err
		}
		if stored != fingerprint {
			return fmt.Errorf("row %s fingerprint mismatch", key)
		}
	}
	return rows.Err()
}

func cutoverBeginStatement(name dialect.Name) string {
	if name == dialect.SQLite {
		// The proof and DROP must share one reserved connection and one
		// IMMEDIATE transaction; two pooled statements would leave a race.
		return "BEGIN IMMEDIATE"
	}
	return "BEGIN"
}

func cutoverLockStatement(name dialect.Name) string {
	if name == dialect.PG {
		return "LOCK TABLE usage_append_outbox IN ACCESS EXCLUSIVE MODE"
	}
	return ""
}

func cutoverPlaceholder(name dialect.Name, ordinal int) string {
	if name == dialect.PG {
		return "$" + strconv.Itoa(ordinal)
	}
	return "?"
}

func commitMigration(ctx context.Context, conn bun.Conn) error {
	_, err := conn.ExecContext(ctx, "COMMIT")
	return err
}

// Historical authorization audit decoder; isolated from current runtime reports.
// HistoricalAuthorizationJournal is an audit-only decode of a retired
// authorization posting. It is deliberately not billing.JournalTransaction:
// current validation, writers, reconciliation, and reports cannot post or
// replay this historical book.
type HistoricalAuthorizationJournal struct {
	ID                    string
	AccountID             string
	Currency              string
	SourceKey             string
	SemanticFingerprint   string
	TurnID                string
	ALegID                string
	BLegID                string
	AccountSequence       *int64
	ReversalOf            string
	CorrectsTransactionID string
	CorrectionGroupID     string
	OperationKind         string
	BalanceBeforeNano     int64
	BalanceAfterNano      int64
	ReservedBeforeNano    int64
	ReservedAfterNano     int64
	SpendableBeforeNano   int64
	SpendableAfterNano    int64
	CreditFloorNano       int64
	CreditLimitNano       int64
	Mode                  string
	SnapshotVersionBefore uint64
	SnapshotVersionAfter  uint64
	RecordedAt            time.Time
	Entries               []billing.JournalEntry
}

// HistoricalAuthorizationJournals reads old authorization rows for audit and
// migration tooling only. No current report calls this reader.
func (s *DurableStore) HistoricalAuthorizationJournals(ctx context.Context, accountID string) ([]HistoricalAuthorizationJournal, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	var rows []historicalAuthorizationRow
	if err := s.db.NewRaw(`
SELECT transaction_id, account_id, currency, source_key, semantic_fingerprint,
 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of,
 corrects_transaction_id, correction_group_id, operation_kind,
 balance_before_nano, balance_after_nano, reserved_before_nano,
 reserved_after_nano, spendable_before_nano, spendable_after_nano,
 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before,
 snapshot_version_after, recorded_at
FROM journal_transactions WHERE account_id = ? AND book = 'authorization'
ORDER BY recorded_at, transaction_id`, accountID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: historical authorization decode: %w", err)
	}
	out := make([]HistoricalAuthorizationJournal, 0, len(rows))
	for _, row := range rows {
		entries, err := historicalJournalEntries(ctx, s, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, HistoricalAuthorizationJournal{
			ID: row.ID, AccountID: row.AccountID, Currency: row.Currency, SourceKey: row.SourceKey,
			SemanticFingerprint: row.SemanticFingerprint, TurnID: row.TurnID, ALegID: row.ALegID, BLegID: row.BLegID,
			AccountSequence: row.AccountSequence, ReversalOf: row.ReversalOf, CorrectsTransactionID: row.CorrectsTransactionID,
			CorrectionGroupID: row.CorrectionGroupID, OperationKind: row.OperationKind,
			BalanceBeforeNano: row.BalanceBefore, BalanceAfterNano: row.BalanceAfter,
			ReservedBeforeNano: row.ReservedBefore, ReservedAfterNano: row.ReservedAfter,
			SpendableBeforeNano: row.SpendableBefore, SpendableAfterNano: row.SpendableAfter,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: row.Mode,
			SnapshotVersionBefore: row.VersionBefore, SnapshotVersionAfter: row.VersionAfter,
			RecordedAt: row.RecordedAt, Entries: entries,
		})
	}
	return out, nil
}

type historicalAuthorizationRow struct {
	ID                    string    `bun:"transaction_id"`
	AccountID             string    `bun:"account_id"`
	Currency              string    `bun:"currency"`
	SourceKey             string    `bun:"source_key"`
	SemanticFingerprint   string    `bun:"semantic_fingerprint"`
	TurnID                string    `bun:"turn_id"`
	ALegID                string    `bun:"a_leg_id"`
	BLegID                string    `bun:"b_leg_id"`
	AccountSequence       *int64    `bun:"account_sequence"`
	ReversalOf            string    `bun:"reversal_of"`
	CorrectsTransactionID string    `bun:"corrects_transaction_id"`
	CorrectionGroupID     string    `bun:"correction_group_id"`
	OperationKind         string    `bun:"operation_kind"`
	BalanceBefore         int64     `bun:"balance_before_nano"`
	BalanceAfter          int64     `bun:"balance_after_nano"`
	ReservedBefore        int64     `bun:"reserved_before_nano"`
	ReservedAfter         int64     `bun:"reserved_after_nano"`
	SpendableBefore       int64     `bun:"spendable_before_nano"`
	SpendableAfter        int64     `bun:"spendable_after_nano"`
	CreditFloor           int64     `bun:"credit_floor_nano"`
	CreditLimit           int64     `bun:"credit_limit_nano"`
	Mode                  string    `bun:"mode"`
	VersionBefore         uint64    `bun:"snapshot_version_before"`
	VersionAfter          uint64    `bun:"snapshot_version_after"`
	RecordedAt            time.Time `bun:"recorded_at"`
}

func historicalJournalEntries(ctx context.Context, s *DurableStore, id string) ([]billing.JournalEntry, error) {
	var rows []journalEntryRow
	if err := s.db.NewRaw(`SELECT transaction_id, ordinal, ledger_account, side, currency, amount_nano FROM journal_entries WHERE transaction_id = ? ORDER BY ordinal`, id).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: historical authorization entries: %w", err)
	}
	entries := make([]billing.JournalEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, billing.JournalEntry{LedgerAccount: row.LedgerAccount, Side: billing.JournalSide(row.Side), Amount: billing.Money{Nano: row.AmountNano, Currency: row.Currency}})
	}
	return entries, nil
}

// legacySnapshotIntegrityZero verifies snapshots written before the current
// snapshot DTO removed its reserved field. Those historical rows remain
// auditable without reintroducing the field into current billing types.
func legacySnapshotIntegrityZero(operationKey, accountID, operationKind, sourceKey, fingerprint string, before, after billing.AccountSnapshot, sequenceStart, sequenceEnd uint64) string {
	type legacyAccountSnapshot struct {
		BalanceNano     int64
		LegacyHeldNano  int64 `json:"ReservedNano"`
		SpendableNano   int64
		CreditFloorNano int64
		CreditLimitNano int64
		Mode            billing.AccountMode
		Currency        string
		Version         uint64
	}
	type legacySnapshot struct {
		Version                                                        string
		OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
		Before, After                                                  legacyAccountSnapshot
		SequenceStart, SequenceEnd                                     uint64
	}
	payload, _ := json.Marshal(legacySnapshot{
		Version: "snapshot:v1", OperationKey: operationKey, AccountID: accountID, OperationKind: operationKind, SourceKey: sourceKey, Fingerprint: fingerprint,
		Before:        legacyAccountSnapshot{BalanceNano: before.BalanceNano, LegacyHeldNano: 0, SpendableNano: before.SpendableNano, CreditFloorNano: before.CreditFloorNano, CreditLimitNano: before.CreditLimitNano, Mode: before.Mode, Currency: before.Currency, Version: before.Version},
		After:         legacyAccountSnapshot{BalanceNano: after.BalanceNano, LegacyHeldNano: 0, SpendableNano: after.SpendableNano, CreditFloorNano: after.CreditFloorNano, CreditLimitNano: after.CreditLimitNano, Mode: after.Mode, Currency: after.Currency, Version: after.Version},
		SequenceStart: sequenceStart, SequenceEnd: sequenceEnd,
	})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("snapshot:v1:%x", digest[:])
}
