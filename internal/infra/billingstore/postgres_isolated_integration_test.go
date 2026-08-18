//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/uptrace/bun"
)

var postgresBillingSchemaSeq atomic.Uint64

// openIsolatedPostgresBun gives a billing integration test its own temporary
// schema. The admin endpoint is used only to create/drop that schema; all test
// work uses the configured runtime endpoint. URL DSNs carry search_path to each
// connection. DSNs that cannot carry it use a single pooled connection and SET
// as a safe fallback.
func openIsolatedPostgresBun(t *testing.T, runtimeDSN string, maxOpen int) (*bun.DB, string) {
	t.Helper()
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 1)
	schema := fmt.Sprintf("billing_test_%d_%d", time.Now().UnixNano(), postgresBillingSchemaSeq.Add(1))
	quotedSchema := quotePostgresIdentifier(schema)
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}

	dsn, hasURLSearchPath := postgresDSNWithSearchPath(runtimeDSN, schema)
	openMax := maxOpen
	if !hasURLSearchPath {
		openMax = 1
	}
	bunDB, err := testkit.OpenPostgresBun(dsn, openMax)
	if err != nil {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close()
		t.Fatalf("open isolated PostgreSQL runtime: %v", err)
	}
	if !hasURLSearchPath {
		if _, err := bunDB.ExecContext(context.Background(), "SET search_path TO "+quotedSchema); err != nil {
			_ = bunDB.Close()
			_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
			_ = admin.Close()
			t.Fatalf("set isolated PostgreSQL search_path: %v", err)
		}
	}
	t.Cleanup(func() {
		_ = bunDB.Close()
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		_ = admin.Close()
	})
	return bunDB, schema
}

func postgresDSNWithSearchPath(dsn, schema string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return dsn, false
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String(), true
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
