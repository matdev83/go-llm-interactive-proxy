package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// LegacyUsageRetirementMigrationName is a forward-only destructive migration.
// The baseline TUR/LUR/processing DDL remains immutable historical input.
const LegacyUsageRetirementMigrationName = "20260901000000"

var ErrLegacyUsageRetirementBlocked = errors.New("billingstore: legacy usage retirement blocked")

const legacyRetirementLockKey = "go-lip.billing.legacy-usage-retirement.v1"

func registerLegacyUsageRetirementMigration() {
	migrations.MustRegister(legacyUsageRetirementSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func legacyUsageRetirementSchemaUp(ctx context.Context, db *bun.DB) error {
	return retireLegacyUsagePersistence(ctx, db)
}

// retireLegacyUsagePersistence proves that old rows have already reached a
// durable terminal result before deleting their historical operational store.
// No legacy payload is translated: the old model lacks the current call/leg
// completeness and fingerprint contract, so conversion would be guesswork.
func retireLegacyUsagePersistence(ctx context.Context, db *bun.DB) error {
	return retireLegacyUsagePersistenceWithHook(ctx, db, nil)
}

func retireLegacyUsagePersistenceWithHook(ctx context.Context, db *bun.DB, afterLock func()) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrLegacyUsageRetirementBlocked)
	}
	if db == nil {
		return fmt.Errorf("%w: nil database", ErrLegacyUsageRetirementBlocked)
	}
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: reserve migration connection: %v", ErrLegacyUsageRetirementBlocked, err)
	}
	defer conn.Close()

	name := db.Dialect().Name()
	if name != dialect.SQLite && name != dialect.PG {
		return fmt.Errorf("%w: unsupported dialect %s", ErrLegacyUsageRetirementBlocked, name.String())
	}
	if _, err := conn.ExecContext(ctx, legacyRetirementBegin(name)); err != nil {
		return fmt.Errorf("%w: begin critical section: %v", ErrLegacyUsageRetirementBlocked, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if name == dialect.PG {
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('`+legacyRetirementLockKey+`'))`); err != nil {
			return fmt.Errorf("%w: PostgreSQL migration serialization: %v", ErrLegacyUsageRetirementBlocked, err)
		}
	}
	exists, err := legacyTablesExist(ctx, conn, name)
	if err != nil {
		return fmt.Errorf("%w: inspect legacy tables: %v", ErrLegacyUsageRetirementBlocked, err)
	}
	if name == dialect.PG {
		for _, table := range legacyUsageTables {
			if exists[table] {
				if _, err := conn.ExecContext(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); err != nil {
					return fmt.Errorf("%w: lock %s: %v", ErrLegacyUsageRetirementBlocked, table, err)
				}
			}
		}
	}
	if afterLock != nil {
		afterLock()
	}
	if err := proveLegacyUsageRetirable(ctx, conn, exists, name); err != nil {
		return err
	}
	if err := dropLegacyUsageObjects(ctx, conn, name, exists); err != nil {
		return fmt.Errorf("%w: drop legacy objects: %v", ErrLegacyUsageRetirementBlocked, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("%w: commit critical section: %v", ErrLegacyUsageRetirementBlocked, err)
	}
	committed = true
	return nil
}

var legacyUsageTables = []string{"usage_record_processing", "leg_usage_records", "turn_usage_records"}

type legacyUsageProof struct {
	key         string
	fingerprint string
	payload     string
}

type legacyProcessingProof struct {
	key         string
	fingerprint string
	status      string
	resultRef   string
}

func legacyRetirementBegin(name dialect.Name) string {
	if name == dialect.SQLite {
		// One dedicated connection plus IMMEDIATE excludes a new SQLite writer
		// between this proof and the DROP statements.
		return "BEGIN IMMEDIATE"
	}
	return "BEGIN"
}

func legacyUsageTableNames(ctx context.Context, db *bun.DB) ([]string, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database")
	}
	var names []string
	for _, table := range legacyUsageTables {
		var count int
		var err error
		if db.Dialect().Name() == dialect.SQLite {
			err = db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &count)
		} else if db.Dialect().Name() == dialect.PG {
			err = db.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(ctx, &count)
		} else {
			return nil, fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
		}
		if err != nil {
			return nil, err
		}
		if count != 0 {
			names = append(names, table)
		}
	}
	return names, nil
}

func legacyTablesExist(ctx context.Context, conn *sql.Conn, name dialect.Name) (map[string]bool, error) {
	out := make(map[string]bool, len(legacyUsageTables))
	for _, table := range legacyUsageTables {
		var count int
		var err error
		if name == dialect.SQLite {
			err = conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
		} else {
			err = conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`, table).Scan(&count)
		}
		if err != nil {
			return nil, err
		}
		out[table] = count == 1
	}
	return out, nil
}

func proveLegacyUsageRetirable(ctx context.Context, conn *sql.Conn, exists map[string]bool, name dialect.Name) error {
	if !exists["turn_usage_records"] && !exists["leg_usage_records"] && !exists["usage_record_processing"] {
		return nil
	}
	// A partial legacy schema is not evidence of safe retirement. Empty partial
	// remnants can be dropped, but any row is unprovable without its parent.
	if exists["turn_usage_records"] != exists["usage_record_processing"] {
		for _, table := range []string{"turn_usage_records", "usage_record_processing"} {
			if exists[table] {
				var count int
				if err := conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+table).Scan(&count); err != nil {
					return legacyRetirementBlocked("inspect %s: %v", table, err)
				}
				if count != 0 {
					return legacyRetirementBlocked("%s exists without its proof table", table)
				}
			}
		}
	}

	turns := make(map[string]legacyUsageProof)
	if exists["turn_usage_records"] {
		rows, err := conn.QueryContext(ctx, `SELECT tur_key, fingerprint, payload_json FROM turn_usage_records`)
		if err != nil {
			return legacyRetirementBlocked("inspect turn_usage_records: %v", err)
		}
		for rows.Next() {
			var row legacyUsageProof
			if err := rows.Scan(&row.key, &row.fingerprint, &row.payload); err != nil {
				rows.Close()
				return legacyRetirementBlocked("read turn_usage_records: %v", err)
			}
			if strings.TrimSpace(row.key) == "" || strings.TrimSpace(row.fingerprint) == "" || !legacyPayloadObject(row.payload) {
				rows.Close()
				return legacyRetirementBlocked("turn_usage_records contains malformed or unprovable row %q", row.key)
			}
			turns[row.key] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return legacyRetirementBlocked("read turn_usage_records: %v", err)
		}
		rows.Close()
	}

	processed := make(map[string]legacyProcessingProof)
	if exists["usage_record_processing"] {
		rows, err := conn.QueryContext(ctx, `SELECT tur_key, tur_fingerprint, status, result_ref FROM usage_record_processing`)
		if err != nil {
			return legacyRetirementBlocked("inspect usage_record_processing: %v", err)
		}
		for rows.Next() {
			var row legacyProcessingProof
			if err := rows.Scan(&row.key, &row.fingerprint, &row.status, &row.resultRef); err != nil {
				rows.Close()
				return legacyRetirementBlocked("read usage_record_processing: %v", err)
			}
			if strings.TrimSpace(row.key) == "" {
				rows.Close()
				return legacyRetirementBlocked("usage_record_processing contains an empty key")
			}
			if row.status != "processed" {
				rows.Close()
				return legacyRetirementBlocked("unresolved usage_record_processing row %q has status %q", row.key, row.status)
			}
			if err := proveLegacyProcessedResult(ctx, conn, name, row); err != nil {
				rows.Close()
				return err
			}
			processed[row.key] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return legacyRetirementBlocked("read usage_record_processing: %v", err)
		}
		rows.Close()
	}
	for key, row := range turns {
		proof, ok := processed[key]
		if !ok || proof.fingerprint != row.fingerprint || strings.TrimSpace(proof.resultRef) == "" {
			return legacyRetirementBlocked("legacy TUR %q lacks a matching durable processed result; conversion is not provable", key)
		}
	}
	for key := range processed {
		if _, ok := turns[key]; !ok {
			return legacyRetirementBlocked("processing proof %q has no TUR row", key)
		}
	}
	if exists["leg_usage_records"] {
		rows, err := conn.QueryContext(ctx, `SELECT lur_key, tur_key, fingerprint, payload_json FROM leg_usage_records`)
		if err != nil {
			return legacyRetirementBlocked("inspect leg_usage_records: %v", err)
		}
		for rows.Next() {
			var row legacyUsageProof
			var turKey string
			if err := rows.Scan(&row.key, &turKey, &row.fingerprint, &row.payload); err != nil {
				rows.Close()
				return legacyRetirementBlocked("read leg_usage_records: %v", err)
			}
			if strings.TrimSpace(row.key) == "" || strings.TrimSpace(turKey) == "" || strings.TrimSpace(row.fingerprint) == "" || !legacyPayloadObject(row.payload) {
				rows.Close()
				return legacyRetirementBlocked("leg_usage_records contains malformed or unprovable row %q", row.key)
			}
			if _, ok := turns[turKey]; !ok {
				rows.Close()
				return legacyRetirementBlocked("legacy LUR %q has no parent TUR %q", row.key, turKey)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return legacyRetirementBlocked("read leg_usage_records: %v", err)
		}
		rows.Close()
	}
	return nil
}

const legacyCustomerSettlementResultPrefix = "customer-settlement:v1:"

// proveLegacyProcessedResult accepts only the result reference emitted by the
// old settlement path and verifies its durable current operation snapshot. A
// non-empty result_ref is not evidence by itself: arbitrary references,
// journal IDs, and snapshots without an integrity fingerprint remain blocked.
func proveLegacyProcessedResult(ctx context.Context, conn *sql.Conn, name dialect.Name, row legacyProcessingProof) error {
	ref := strings.TrimSpace(row.resultRef)
	key := strings.TrimSpace(row.key)
	if !strings.HasPrefix(ref, legacyCustomerSettlementResultPrefix) || strings.TrimPrefix(ref, legacyCustomerSettlementResultPrefix) != key {
		return legacyRetirementBlocked("processed usage_record_processing row %q has an unsupported or unrelated result_ref", row.key)
	}
	var count int
	query := `SELECT COUNT(1) FROM billing_operation_snapshots WHERE operation_key = ` + cutoverPlaceholder(name, 1) + ` AND operation_kind = 'customer_settlement' AND source_key = ` + cutoverPlaceholder(name, 2) + ` AND fingerprint <> '' AND integrity_fingerprint <> ''`
	if err := conn.QueryRowContext(ctx, query, ref, key).Scan(&count); err != nil {
		return legacyRetirementBlocked("inspect durable result for processed row %q: %v", row.key, err)
	}
	if count != 1 {
		return legacyRetirementBlocked("processed usage_record_processing row %q lacks a matching durable terminal result", row.key)
	}
	return nil
}

func legacyPayloadObject(payload string) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(payload), &object) == nil && object != nil
}

func legacyRetirementBlocked(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLegacyUsageRetirementBlocked, fmt.Sprintf(format, args...))
}

func dropLegacyUsageObjects(ctx context.Context, conn *sql.Conn, name dialect.Name, exists map[string]bool) error {
	triggers := []struct{ name, table string }{
		{"billing_tur_immutable_update", "turn_usage_records"}, {"billing_tur_immutable_delete", "turn_usage_records"},
		{"billing_lur_immutable_update", "leg_usage_records"}, {"billing_lur_immutable_delete", "leg_usage_records"},
		{"billing_tur_immutable", "turn_usage_records"}, {"billing_lur_immutable", "leg_usage_records"},
	}
	for _, trigger := range triggers {
		if name == dialect.PG && !exists[trigger.table] {
			continue
		}
		statement := `DROP TRIGGER IF EXISTS ` + trigger.name
		if name == dialect.PG {
			statement += ` ON ` + trigger.table
		}
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, index := range []string{"idx_billing_processing_status", sessionAccountIndex} {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
			return err
		}
	}
	for _, table := range []string{"usage_record_processing", "leg_usage_records", "turn_usage_records"} {
		if exists[table] {
			if _, err := conn.ExecContext(ctx, `DROP TABLE `+table); err != nil {
				return err
			}
		}
	}
	return nil
}
