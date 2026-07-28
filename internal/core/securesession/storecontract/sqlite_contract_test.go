package storecontract_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/sqlite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract"
)

var sqliteContractMemSeq atomic.Int64

// TestStoreContract_SQLite runs the full contract suite against the SQLite store.
// No contract subtest asserts restart/file persistence, so each subtest uses an
// isolated in-memory database (same migration and DML path, no fsync cost).
// File-backed open/persistence stays covered in internal/core/securesession/adapters/sqlite.
func TestStoreContract_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(t *testing.T) app.Store {
		t.Helper()
		id := sqliteContractMemSeq.Add(1)
		dsn := fmt.Sprintf("file:memsqlitecontract%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		s, err := sqlite.NewContext(context.Background(), sqlDB)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
