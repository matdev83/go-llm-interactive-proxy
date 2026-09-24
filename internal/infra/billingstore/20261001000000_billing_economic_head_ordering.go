package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingEconomicHeadOrderingMigrationName persists the durable current
// winning ordering tuple for allocation-aware selected heads.
//
// Defect repaired: same observation plane compared incoming CreatedAt against
// the head's initial created_at_unix, while head UPDATE advanced only
// updated_at_unix. Sequence t1 -> t3 -> delayed t2 therefore regressed the
// authoritative pointer. Equal timestamps kept first arrival without a
// canonical tie-break.
//
// Contract: the winning tuple is (winning CreatedAt nanos carried by
// updated_at_unix, derivation_hash, dependencies_hash, work_id). Larger
// CreatedAt wins; equal time uses total deterministic lexical tie-break
// (derivation, dependencies, work key), larger wins, consistent SQLite/PG and
// never arrival order. updated_at_unix already carries the winning work's
// CreatedAt; this migration adds the missing derivation identity columns and
// backfills them from durable valuations/work so every compared field is
// persisted and updated atomically on every transition.
const BillingEconomicHeadOrderingMigrationName = "20261001000000"

const billingEconomicHeadOrderingBackfillBatchSize = 256

func registerBillingEconomicHeadOrderingMigration() {
	migrations.MustRegister(billingEconomicHeadOrderingUp, func(context.Context, *bun.DB) error { return nil })
}

func billingEconomicHeadOrderingUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing economic head ordering: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing economic head ordering: nil context")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := addBillingEconomicHeadOrderingColumns(ctx, tx); err != nil {
				return err
			}
			return backfillBillingEconomicHeadOrdering(ctx, tx)
		})
	default:
		return fmt.Errorf("billing economic head ordering: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func addBillingEconomicHeadOrderingColumns(ctx context.Context, tx bun.Tx) error {
	columns := []struct {
		name        string
		sqliteDDL   string
		postgresDDL string
	}{
		{
			name:        "derivation_hash",
			sqliteDDL:   `ALTER TABLE billing_economic_valuation_heads ADD COLUMN derivation_hash TEXT NOT NULL DEFAULT ''`,
			postgresDDL: `ALTER TABLE billing_economic_valuation_heads ADD COLUMN IF NOT EXISTS derivation_hash TEXT NOT NULL DEFAULT ''`,
		},
		{
			name:        "dependencies_hash",
			sqliteDDL:   `ALTER TABLE billing_economic_valuation_heads ADD COLUMN dependencies_hash TEXT NOT NULL DEFAULT ''`,
			postgresDDL: `ALTER TABLE billing_economic_valuation_heads ADD COLUMN IF NOT EXISTS dependencies_hash TEXT NOT NULL DEFAULT ''`,
		},
	}
	for _, column := range columns {
		if tx.Dialect().Name() == dialect.SQLite {
			var count int
			if err := tx.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_economic_valuation_heads') WHERE name = ?`, column.name).Scan(ctx, &count); err != nil {
				return fmt.Errorf("billing economic head ordering sqlite probe %s: %w", column.name, err)
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
			return fmt.Errorf("billing economic head ordering add column %s: %w", column.name, err)
		}
	}
	return nil
}

type billingEconomicHeadOrderingRow struct {
	ID               int64  `bun:"id"`
	StoreID          string `bun:"store_id"`
	InputSetHash     string `bun:"input_set_hash"`
	WorkID           string `bun:"work_id"`
	ValuationID      string `bun:"valuation_id"`
	ValuationVersion int64  `bun:"valuation_version"`
	DerivationHash   string `bun:"derivation_hash"`
	DependenciesHash string `bun:"dependencies_hash"`
}

func backfillBillingEconomicHeadOrdering(ctx context.Context, tx bun.Tx) error {
	var lastID int64
	for {
		rows := make([]billingEconomicHeadOrderingRow, 0, billingEconomicHeadOrderingBackfillBatchSize)
		if err := tx.NewRaw(`SELECT id, store_id, input_set_hash, work_id, valuation_id, valuation_version, derivation_hash, dependencies_hash FROM billing_economic_valuation_heads WHERE id > ? ORDER BY id ASC LIMIT ?`, lastID, billingEconomicHeadOrderingBackfillBatchSize).Scan(ctx, &rows); err != nil {
			return fmt.Errorf("billing economic head ordering backfill scan after id %d: %w", lastID, err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			lastID = row.ID
			if row.DerivationHash != "" || row.DependenciesHash != "" {
				continue
			}
			derivation, dependencies, err := billingEconomicHeadOrderingValues(ctx, tx, row)
			if err != nil {
				return err
			}
			if derivation == "" && dependencies == "" {
				continue
			}
			if _, err := tx.NewRaw(`UPDATE billing_economic_valuation_heads SET derivation_hash = ?, dependencies_hash = ? WHERE id = ?`, derivation, dependencies, row.ID).Exec(ctx); err != nil {
				return fmt.Errorf("billing economic head ordering backfill row %d: %w", row.ID, err)
			}
		}
	}
}

func billingEconomicHeadOrderingValues(ctx context.Context, tx bun.Tx, row billingEconomicHeadOrderingRow) (string, string, error) {
	derivation := ""
	if row.ValuationID != "" && row.ValuationVersion > 0 {
		var valuationHash string
		if err := tx.NewRaw(`SELECT input_set_hash FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ? LIMIT 1`, row.StoreID, row.ValuationID, row.ValuationVersion).Scan(ctx, &valuationHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", fmt.Errorf("%w: billing economic head ordering missing valuation %q/%d for head %d", ErrIdentityConflict, row.ValuationID, row.ValuationVersion, row.ID)
			}
			return "", "", fmt.Errorf("billing economic head ordering valuation lookup head %d: %w", row.ID, err)
		}
		if valuationHash != "" && valuationHash != row.InputSetHash {
			derivation = valuationHash
		}
	}
	dependencies := ""
	if row.WorkID != "" {
		var payload string
		if err := tx.NewRaw(`SELECT payload_json FROM billing_economic_work WHERE store_id = ? AND work_id = ? AND work_version = 1 LIMIT 1`, row.StoreID, row.WorkID).Scan(ctx, &payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Legacy or externally advanced heads may not retain the work
				// marker; derivation from the valuation still orders
				// allocation-aware corrections deterministically.
				return derivation, "", nil
			}
			return "", "", fmt.Errorf("billing economic head ordering work lookup head %d: %w", row.ID, err)
		}
		var work billing.EconomicRevisionWork
		if err := json.Unmarshal([]byte(payload), &work); err != nil {
			return "", "", fmt.Errorf("billing economic head ordering decode work head %d: %w", row.ID, err)
		}
		normalized, err := work.Normalize()
		if err != nil {
			return "", "", fmt.Errorf("billing economic head ordering normalize work head %d: %w", row.ID, err)
		}
		identity, err := normalized.Identity()
		if err != nil {
			return "", "", fmt.Errorf("billing economic head ordering identify work head %d: %w", row.ID, err)
		}
		dependencies = identity.DependenciesHash
		if identity.DerivationHash != "" && derivation == "" {
			derivation = identity.DerivationHash
		}
		if derivation != "" && identity.DerivationHash != "" && derivation != identity.DerivationHash {
			return "", "", fmt.Errorf("%w: billing economic head ordering derivation drift head %d", ErrIdentityConflict, row.ID)
		}
	}
	return derivation, dependencies, nil
}
