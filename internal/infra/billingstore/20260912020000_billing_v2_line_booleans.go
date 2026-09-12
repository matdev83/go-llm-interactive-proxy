package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingV2LineBooleanRepairMigrationName identifies the forward repair for
// valuation-line boolean projections created by the original V2 migration.
const BillingV2LineBooleanRepairMigrationName = "20260912020000"

var billingV2LineBooleanColumns = [...]string{
	"quantity_present",
	"unit_price_present",
	"rate_numerator_present",
	"rate_denominator_present",
	"amount_present",
	"rounded_present",
	"included_unit",
}

func registerBillingV2LineBooleanRepairMigration() {
	migrations.MustRegister(billingV2LineBooleanRepairSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingV2LineBooleanRepairSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing V2 line boolean repair schema: nil database")
	}
	if db.Dialect().Name() == dialect.SQLite {
		return nil
	}
	if db.Dialect().Name() != dialect.PG {
		return fmt.Errorf("billing V2 line boolean repair schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range billingV2LineBooleanRepairPostgresDDL() {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing V2 line boolean repair DDL: %w", err)
		}
	}
	return nil
}

func billingV2LineBooleanRepairPostgresDDL() []string {
	statements := make([]string, 0, len(billingV2LineBooleanColumns)*3)
	for _, column := range billingV2LineBooleanColumns {
		statements = append(statements, "ALTER TABLE IF EXISTS billing_valuation_lines ALTER COLUMN "+column+" DROP DEFAULT")
	}
	for _, column := range billingV2LineBooleanColumns {
		statements = append(statements, "ALTER TABLE IF EXISTS billing_valuation_lines ALTER COLUMN "+column+" TYPE BOOLEAN USING ("+column+"::text IN ('1','true','t'))")
	}
	for _, column := range billingV2LineBooleanColumns {
		statements = append(statements, "ALTER TABLE IF EXISTS billing_valuation_lines ALTER COLUMN "+column+" SET DEFAULT FALSE")
	}
	return statements
}
