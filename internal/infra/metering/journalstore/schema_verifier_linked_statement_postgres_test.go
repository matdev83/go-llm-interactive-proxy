//go:build integration

package journalstore_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

var journalIsolatedSchemaSeq atomic.Uint64

// openIsolatedJournalPostgres gives each verifier test its own disposable
// PostgreSQL schema. The admin endpoint is used only to create and drop that
// schema; every migration, index mutation, and verification runs against the
// isolated search_path, so the shared database schema is never modified.
//
// The runtime DSN must be a URL DSN carrying search_path as a per-connection
// startup parameter. That persists on every pooled connection and a target
// mismatch fails closed via current_schema(). A DSN that cannot carry
// search_path is rejected before any DDL instead of using a session-only
// SET search_path, which a pool cannot guarantee to pin to one connection.
func openIsolatedJournalPostgres(t *testing.T) *bun.DB {
	t.Helper()
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	schema := fmt.Sprintf("journalstore_test_%d_%d", time.Now().UnixNano(), journalIsolatedSchemaSeq.Add(1))
	dsn, hasURLSearchPath := journalDSNWithSearchPath(runtimeDSN, schema)
	if !hasURLSearchPath {
		t.Skipf("isolated journal schema verification requires a URL PostgreSQL DSN carrying search_path per connection; refusing session-only SET fallback")
	}

	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 1)
	quoted := quoteJournalPostgresIdentifier(schema)
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated journal schema: %v", err)
	}
	cleanup := func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		_ = admin.Close()
	}

	bunDB, err := testkit.OpenPostgresBun(dsn, 4)
	if err != nil {
		cleanup()
		t.Fatalf("open isolated journal runtime: %v", err)
	}
	var current string
	if err := bunDB.NewRaw(`SELECT current_schema()`).Scan(context.Background(), &current); err != nil {
		_ = bunDB.Close()
		cleanup()
		t.Fatalf("read isolated journal current_schema: %v", err)
	}
	if current != schema {
		_ = bunDB.Close()
		cleanup()
		t.Fatalf("isolated schema target mismatch: current_schema=%q want %q", current, schema)
	}
	t.Cleanup(func() {
		_ = bunDB.Close()
		cleanup()
	})
	return bunDB
}

func journalDSNWithSearchPath(dsn, schema string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return dsn, false
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String(), true
}

func quoteJournalPostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// TestJournalDSNWithSearchPathRejectsNonURL pins the safety contract used by
// openIsolatedJournalPostgres: only a URL DSN that can carry search_path as a
// per-connection setting is accepted; non-URL DSNs are reported unsupported so
// the caller skips before any DDL rather than falling back to session-only SET.
func TestJournalDSNWithSearchPathRejectsNonURL(t *testing.T) {
	if _, ok := journalDSNWithSearchPath("host=localhost user=x dbname=y", "s"); ok {
		t.Fatal("non-URL DSN must not report per-connection search_path support")
	}
	dsn, ok := journalDSNWithSearchPath("postgresql://u:p@h:5432/db?sslmode=require", "s")
	if !ok {
		t.Fatal("URL postgres DSN must report per-connection search_path support")
	}
	if !strings.Contains(dsn, "search_path=s") {
		t.Fatalf("rewritten DSN is missing search_path: %q", dsn)
	}
}

// PostgreSQL VerifySchema must fail when a B-leg linked-statement accelerator
// index is absent: R10's bounded indexed statement lookup silently degrades to a
// large scan without it.
func TestVerifySchemaPostgresRequiresLinkedStatementIndexes(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{
		"idx_metering_facts_store_bleg",
		"idx_metering_facts_store_bleg_statement",
	} {
		t.Run(name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			_, err := bunDB.ExecContext(ctx, `DROP INDEX IF EXISTS `+name)
			require.NoError(t, err)
			err = journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "missing %s must fail verification", name)
			require.Contains(t, err.Error(), name)
		})
	}
}

// Malformed B-leg index definitions must not satisfy verification even though
// the index name exists: wrong ordered key columns, a missing conjunct, an extra
// restriction, or a disjunction all break the production lookup shape.
func TestVerifySchemaPostgresRejectsMalformedLinkedStatementIndexes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		index  string
		mutate []string
	}{
		{
			name:  "bleg wrong columns",
			index: "idx_metering_facts_store_bleg",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg`,
				`CREATE INDEX idx_metering_facts_store_bleg ON metering_facts(store_id, b_leg_id) WHERE payload_kind = 'observation'`,
			},
		},
		{
			name:  "bleg_statement missing conjunct",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'`,
			},
		},
		{
			name:  "bleg_statement extra conjunct",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'
						AND authority = 'verified_statement'
						AND b_leg_id = 'one-leg'`,
			},
		},
		{
			name:  "bleg_statement disjunction",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'
						OR authority = 'verified_statement'`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			for _, stmt := range tc.mutate {
				_, err := bunDB.ExecContext(ctx, stmt)
				require.NoError(t, err)
			}
			err := journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "malformed %s must fail verification", tc.name)
			require.Contains(t, err.Error(), tc.index)
		})
	}
}

// A migration-history row alone is not proof of a live index; conversely, a
// missing linked-statement migration record must fail verification.
func TestVerifySchemaPostgresRequiresLinkedStatementMigrationHistory(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{
		journalstore.LinkedStatementIndexMigrationName,
		journalstore.LinkedStatementOrderedIndexMigrationName,
		journalstore.LinkedStatementCandidateIndexMigrationName,
	} {
		t.Run(name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			_, err := bunDB.NewRaw(`DELETE FROM bun_metering_journal_migrations WHERE name = ?`, name).Exec(ctx)
			require.NoError(t, err)
			err = journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "missing migration history %s must fail verification", name)
			require.Contains(t, err.Error(), name)
		})
	}
}
