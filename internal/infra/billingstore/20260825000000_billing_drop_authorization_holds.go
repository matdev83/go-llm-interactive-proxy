package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// AuthorizationHoldsDropMigrationName is the guarded schema retirement for
// authorization_holds after open-hold inventory is empty.
const AuthorizationHoldsDropMigrationName = "20260825000000"

func registerAuthorizationHoldsDropMigration() {
	migrations.MustRegister(authorizationHoldsDropUp, func(context.Context, *bun.DB) error { return nil })
}

func authorizationHoldsDropUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing authorization holds drop: nil database")
	}
	var openHolds int
	err := db.NewRaw(`SELECT COUNT(*) FROM authorization_holds WHERE status = 'open'`).Scan(ctx, &openHolds)
	if err != nil {
		if isMissingRelation(err) {
			return nil
		}
		return fmt.Errorf("billing authorization holds drop: count open holds: %w", err)
	}
	if openHolds > 0 {
		return fmt.Errorf("billing authorization holds drop: %d open hold(s) remain; settle or reconcile before dropping authorization_holds", openHolds)
	}
	statements := []string{
		`DROP TABLE IF EXISTS authorization_holds`,
	}
	if db.Dialect().Name() == dialect.PG {
		statements = []string{
			`DROP TABLE IF EXISTS authorization_holds CASCADE`,
		}
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing authorization holds drop DDL: %w", err)
		}
	}
	return nil
}

func isMissingRelation(err error) bool {
	if err == nil {
		return false
	}
	if errorsIsNoSuchTable(err) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such table") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "undefined_table")
}

func errorsIsNoSuchTable(err error) bool {
	return err != nil && (err == sql.ErrNoRows || strings.Contains(strings.ToLower(err.Error()), "no such table"))
}
