package storecontract_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	_ "modernc.org/sqlite"
)

var bunContractMemSeq atomic.Int64

func TestStoreContract_BunSQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(tb *testing.T) app.Store {
		id := bunContractMemSeq.Add(1)
		dsn := fmt.Sprintf("file:memcontract%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			tb.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			tb.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		s, err := bunstore.NewContext(ctx, bunDB)
		if err != nil {
			_ = sqlDB.Close()
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = s.Close() })
		return s
	})
}
