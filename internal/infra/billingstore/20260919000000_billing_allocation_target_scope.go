package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingAllocationTargetScopeMigrationName identifies the forward repair for
// target-scope search projections. The canonical allocation and target JSON
// remain immutable authority; these columns only accelerate scoped queries and
// provide a consistency check for the canonical target subject.
const BillingAllocationTargetScopeMigrationName = "20260919000000"

const billingAllocationTargetScopeBackfillBatchSize = 256

func registerBillingAllocationTargetScopeMigration() {
	migrations.MustRegister(billingAllocationTargetScopeSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingAllocationTargetScopeSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing allocation target scope schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing allocation target scope schema: nil context")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := addBillingAllocationTargetScopeColumns(ctx, tx); err != nil {
				return err
			}
			if err := dropBillingAllocationTargetImmutability(ctx, tx); err != nil {
				return err
			}
			if err := backfillBillingAllocationTargetScope(ctx, tx); err != nil {
				return err
			}
			if err := rebuildBillingAllocationTargetIndex(ctx, tx); err != nil {
				return err
			}
			return restoreBillingAllocationTargetImmutability(ctx, tx)
		})
	default:
		return fmt.Errorf("billing allocation target scope schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func addBillingAllocationTargetScopeColumns(ctx context.Context, tx bun.Tx) error {
	columns := []struct {
		name        string
		sqliteDDL   string
		postgresDDL string
	}{
		{
			name:        "target_tenant_id",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_tenant_id TEXT NOT NULL DEFAULT ''`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_tenant_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			name:        "target_pool_id",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_pool_id TEXT NOT NULL DEFAULT ''`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_pool_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			name:        "target_window_id",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_window_id TEXT NOT NULL DEFAULT ''`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_window_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			name:        "target_reset_at_unix",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_reset_at_unix INTEGER NOT NULL DEFAULT 0`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_reset_at_unix BIGINT NOT NULL DEFAULT 0`,
		},
		{
			name:        "target_start_at_unix",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_start_at_unix INTEGER NOT NULL DEFAULT 0`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_start_at_unix BIGINT NOT NULL DEFAULT 0`,
		},
		{
			name:        "target_end_at_unix",
			sqliteDDL:   `ALTER TABLE billing_allocation_targets ADD COLUMN target_end_at_unix INTEGER NOT NULL DEFAULT 0`,
			postgresDDL: `ALTER TABLE billing_allocation_targets ADD COLUMN IF NOT EXISTS target_end_at_unix BIGINT NOT NULL DEFAULT 0`,
		},
	}
	for _, column := range columns {
		if tx.Dialect().Name() == dialect.SQLite {
			var count int
			if err := tx.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_allocation_targets') WHERE name = ?`, column.name).Scan(ctx, &count); err != nil {
				return fmt.Errorf("billing allocation target scope sqlite column probe %s: %w", column.name, err)
			}
			if count != 0 {
				continue
			}
		}
		statement := column.postgresDDL
		if tx.Dialect().Name() == dialect.SQLite {
			statement = column.sqliteDDL
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			if tx.Dialect().Name() == dialect.SQLite {
				lower := strings.ToLower(err.Error())
				if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
					continue
				}
			}
			return fmt.Errorf("billing allocation target scope add column %s: %w", column.name, err)
		}
	}
	return nil
}

func dropBillingAllocationTargetImmutability(ctx context.Context, tx bun.Tx) error {
	statements := []string{`DROP TRIGGER IF EXISTS billing_allocation_targets_immutable_update`, `DROP TRIGGER IF EXISTS billing_allocation_targets_immutable_delete`}
	if tx.Dialect().Name() == dialect.PG {
		statements = []string{`DROP TRIGGER IF EXISTS billing_allocation_targets_immutable ON billing_allocation_targets`}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing allocation target scope drop immutability: %w", err)
		}
	}
	return nil
}

func restoreBillingAllocationTargetImmutability(ctx context.Context, tx bun.Tx) error {
	statements := []string{
		`CREATE TRIGGER IF NOT EXISTS billing_allocation_targets_immutable_update BEFORE UPDATE ON billing_allocation_targets BEGIN SELECT RAISE(ABORT, 'billing allocation targets are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_allocation_targets_immutable_delete BEFORE DELETE ON billing_allocation_targets BEGIN SELECT RAISE(ABORT, 'billing allocation targets are immutable'); END`,
	}
	if tx.Dialect().Name() == dialect.PG {
		statements = []string{`CREATE TRIGGER billing_allocation_targets_immutable BEFORE UPDATE OR DELETE ON billing_allocation_targets FOR EACH ROW EXECUTE FUNCTION billing_reject_allocation_mutation()`}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing allocation target scope restore immutability: %w", err)
		}
	}
	return nil
}

func rebuildBillingAllocationTargetIndex(ctx context.Context, tx bun.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+billingAllocationTargetIndex); err != nil {
		return fmt.Errorf("billing allocation target scope drop index: %w", err)
	}
	statement := `CREATE INDEX IF NOT EXISTS ` + billingAllocationTargetIndex + ` ON billing_allocation_targets(store_id, target_kind, target_subject_id, target_json, allocation_id, allocation_version, target_id)`
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("billing allocation target scope create index: %w", err)
	}
	return nil
}

type billingAllocationTargetScopeRow struct {
	ID                    int64  `bun:"id"`
	StoreID               string `bun:"store_id"`
	AllocationID          string `bun:"allocation_id"`
	AllocationVersion     int64  `bun:"allocation_version"`
	TargetID              string `bun:"target_id"`
	TargetKind            string `bun:"target_kind"`
	TargetSubjectID       string `bun:"target_subject_id"`
	TargetJSON            string `bun:"target_json"`
	AccountID             string `bun:"account_id"`
	PeriodID              string `bun:"period_id"`
	CanonicalJSON         string `bun:"canonical_json"`
	AllocationFingerprint string `bun:"allocation_fingerprint"`
}

type billingAllocationTargetScopeValues struct {
	ID             int64
	TargetTenantID string
	TargetPoolID   string
	TargetWindowID string
	ResetAt        int64
	StartAt        int64
	EndAt          int64
	AccountID      string
	PeriodID       string
}

func backfillBillingAllocationTargetScope(ctx context.Context, tx bun.Tx) error {
	var orphanCount int
	if err := tx.NewRaw(`
		SELECT COUNT(1)
		FROM billing_allocation_targets t
		LEFT JOIN billing_allocations a
			ON a.store_id = t.store_id AND a.allocation_id = t.allocation_id
			AND a.allocation_version = t.allocation_version
		WHERE a.id IS NULL`).Scan(ctx, &orphanCount); err != nil {
		return fmt.Errorf("billing allocation target scope orphan probe: %w", err)
	}
	if orphanCount != 0 {
		return fmt.Errorf("%w: billing allocation target scope has %d orphan rows", ErrIdentityConflict, orphanCount)
	}
	var lastID int64
	for {
		rows := make([]billingAllocationTargetScopeRow, 0, billingAllocationTargetScopeBackfillBatchSize)
		if err := tx.NewRaw(`
			SELECT t.id, t.store_id, t.allocation_id, t.allocation_version,
				t.target_id, t.target_kind, t.target_subject_id, t.target_json,
				t.account_id, t.period_id, a.canonical_json,
				a.fingerprint AS allocation_fingerprint
			FROM billing_allocation_targets t
			JOIN billing_allocations a
				ON a.store_id = t.store_id AND a.allocation_id = t.allocation_id
				AND a.allocation_version = t.allocation_version
			WHERE t.id > ?
			ORDER BY t.id ASC
			LIMIT ?`, lastID, billingAllocationTargetScopeBackfillBatchSize).Scan(ctx, &rows); err != nil {
			return fmt.Errorf("billing allocation target scope backfill scan after id %d: %w", lastID, err)
		}
		if len(rows) == 0 {
			return nil
		}
		updates := make([]billingAllocationTargetScopeValues, 0, len(rows))
		for _, row := range rows {
			lastID = row.ID
			values, err := billingAllocationTargetScopeValuesFromRow(row)
			if err != nil {
				return err
			}
			updates = append(updates, values)
		}
		for _, values := range updates {
			if _, err := tx.NewRaw(`
				UPDATE billing_allocation_targets SET
					account_id = ?, period_id = ?, target_tenant_id = ?, target_pool_id = ?,
					target_window_id = ?, target_reset_at_unix = ?, target_start_at_unix = ?,
					target_end_at_unix = ?
				WHERE id = ?`,
				values.AccountID, values.PeriodID, values.TargetTenantID, values.TargetPoolID,
				values.TargetWindowID, values.ResetAt, values.StartAt, values.EndAt, values.ID,
			).Exec(ctx); err != nil {
				return fmt.Errorf("billing allocation target scope backfill row %d: %w", values.ID, err)
			}
		}
	}
}

func billingAllocationTargetScopeValuesFromRow(row billingAllocationTargetScopeRow) (billingAllocationTargetScopeValues, error) {
	if row.ID <= 0 || row.AllocationVersion <= 0 || row.AllocationVersion > math.MaxInt64 {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("%w: allocation target row %d has invalid identity", ErrIdentityConflict, row.ID)
	}
	var record economics.AllocationRecord
	if err := json.Unmarshal([]byte(row.CanonicalJSON), &record); err != nil {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("billing allocation target scope row %d: decode canonical allocation: %w", row.ID, err)
	}
	canonical, err := record.Canonical()
	if err != nil {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("billing allocation target scope row %d: validate canonical allocation: %w", row.ID, err)
	}
	if canonical.SourceSubject.StoreID != row.StoreID || canonical.ID != row.AllocationID || canonical.Version != uint64(row.AllocationVersion) {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("%w: allocation target row %d parent identity drift", ErrIdentityConflict, row.ID)
	}
	if row.AllocationFingerprint != "" && row.AllocationFingerprint != canonical.Fingerprint() {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("%w: allocation target row %d parent payload drift", ErrIdentityConflict, row.ID)
	}

	var expected *economics.AllocationTarget
	for i := range canonical.Targets {
		if canonical.Targets[i].TargetID == row.TargetID {
			expected = &canonical.Targets[i]
			break
		}
	}
	if expected == nil {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("%w: allocation target row %d target identity drift", ErrIdentityConflict, row.ID)
	}
	expectedKind, expectedSubjectID, expectedJSON := "", "", "{}"
	if !expected.Unallocated {
		expectedKind = string(expected.Target.Kind)
		expectedSubjectID = subjectIDForEconomics(expected.Target)
		payload, marshalErr := json.Marshal(expected.Target)
		if marshalErr != nil {
			return billingAllocationTargetScopeValues{}, fmt.Errorf("billing allocation target scope row %d: encode canonical target: %w", row.ID, marshalErr)
		}
		expectedJSON = string(payload)
	}
	if row.TargetKind != expectedKind || row.TargetSubjectID != expectedSubjectID || row.TargetJSON != expectedJSON {
		return billingAllocationTargetScopeValues{}, fmt.Errorf("%w: allocation target row %d target projection drift", ErrIdentityConflict, row.ID)
	}
	values := billingAllocationTargetScopeValues{
		ID:        row.ID,
		AccountID: expected.AccountID,
		PeriodID:  expected.PeriodID,
	}
	if !expected.Unallocated {
		values.TargetTenantID = expected.Target.TenantID
		values.TargetPoolID = expected.Target.PoolID
		values.TargetWindowID = expected.Target.WindowID
		values.ResetAt = allocationTargetTimeProjection(expected.Target.ResetAt)
		values.StartAt = allocationTargetTimeProjection(expected.Target.StartAt)
		values.EndAt = allocationTargetTimeProjection(expected.Target.EndAt)
	}
	return values, nil
}

func allocationTargetTimeProjection(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano()
}
