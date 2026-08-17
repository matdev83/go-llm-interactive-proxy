package billingstore

import (
	"context"
	"fmt"

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
