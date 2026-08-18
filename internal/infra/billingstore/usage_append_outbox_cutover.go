package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

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

// usageAppendOutboxTableExists remains local to the transitional cutover until
// the forward retirement migration takes ownership in the next delivery phase.
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
	defer conn.Close()

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
	defer rows.Close()
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
