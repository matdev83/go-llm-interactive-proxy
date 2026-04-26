//go:build integration

package storecontract_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestStoreContract_BunPostgreSQL runs the secure-session contract suite against Bun on PostgreSQL
// when LIP_TEST_POSTGRES_DSN (or legacy LIP_MANAGED_POSTGRES_DSN) is set.
func TestStoreContract_BunPostgreSQL(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	storecontract.RunAll(t, func(tb *testing.T) app.Store {
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenPostgresBun(ctx, dsn, pool)
		if err != nil {
			tb.Fatal(err)
		}
		s, err := bunstore.NewContext(ctx, bunDB)
		if err != nil {
			_ = bunDB.Close()
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = s.Close() })
		return s
	})
}
