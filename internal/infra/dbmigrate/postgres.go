package dbmigrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/uptrace/bun"
)

func PostgresComponents(ctx context.Context, dsn string, components []string) error {
	database, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return fmt.Errorf("open admin postgres: %w", err)
	}
	migrateErr := migrateAndVerifyComponents(ctx, database, components)
	closeErr := database.Close()
	if migrateErr != nil {
		migrateErr = fmt.Errorf("postgres components: %w", migrateErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close admin postgres: %w", closeErr)
	}
	return errors.Join(migrateErr, closeErr)
}

const (
	ComponentUsageAuthority = "usage-authority"
	ComponentConcurrency    = "concurrency"
	ComponentMetering       = "metering"
	ComponentBilling        = "billing"
)

type postgresComponentDefinition struct {
	name    string
	migrate func(context.Context, *bun.DB) error
	verify  func(context.Context, *bun.DB) error
}

var postgresComponentCatalog = []postgresComponentDefinition{
	{name: ComponentUsageAuthority, migrate: authoritystore.Migrate, verify: authoritystore.VerifySchema},
	{name: ComponentConcurrency, migrate: leasestore.Migrate, verify: leasestore.VerifySchema},
	{name: ComponentMetering, migrate: journalstore.Migrate, verify: journalstore.VerifySchema},
	{name: ComponentBilling, migrate: billingstore.Migrate, verify: billingstore.VerifySchema},
}

// lookupPostgresComponent resolves migrate/verify funcs. Tests may override.
var lookupPostgresComponent = postgresComponent

// SwapLookupPostgresComponentForTest replaces component lookup for tests outside
// this package. Call the returned restore from t.Cleanup.
func SwapLookupPostgresComponentForTest(fn func(component string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error)) (restore func()) {
	prev := lookupPostgresComponent
	lookupPostgresComponent = fn
	return func() { lookupPostgresComponent = prev }
}

func migrateAndVerifyComponents(ctx context.Context, database *bun.DB, components []string) error {
	for _, component := range components {
		migrate, verify, err := lookupPostgresComponent(component)
		if err != nil {
			return err
		}
		if err := migrate(ctx, database); err != nil {
			return fmt.Errorf("%s migration failed: %w", component, err)
		}
		if err := verify(ctx, database); err != nil {
			return fmt.Errorf("%s verification failed: %w", component, err)
		}
	}
	return nil
}

// MigrateComponents runs DDL migrations for the named dual-plane components on
// an already-open admin handle. Verification stays with the caller (CLI verify
// pass or runtime OpenStore readiness).
func MigrateComponents(ctx context.Context, database *bun.DB, components []string) error {
	for _, component := range components {
		migrate, _, err := lookupPostgresComponent(component)
		if err != nil {
			return err
		}
		if err := migrate(ctx, database); err != nil {
			return fmt.Errorf("%s migration failed: %w", component, err)
		}
	}
	return nil
}

func ParseComponents(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		names := make([]string, 0, len(postgresComponentCatalog))
		for _, component := range postgresComponentCatalog {
			names = append(names, component.name)
		}
		raw = strings.Join(names, ",")
	}
	seen := make(map[string]struct{})
	components := make([]string, 0, 3)
	for part := range strings.SplitSeq(raw, ",") {
		component := strings.TrimSpace(part)
		if _, _, err := lookupPostgresComponent(component); err != nil {
			return nil, err
		}
		if _, ok := seen[component]; ok {
			continue
		}
		seen[component] = struct{}{}
		components = append(components, component)
	}
	return components, nil
}

func postgresComponent(component string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
	for _, definition := range postgresComponentCatalog {
		if definition.name == component {
			return definition.migrate, definition.verify, nil
		}
	}
	return nil, nil, fmt.Errorf("unknown component %q", component)
}
