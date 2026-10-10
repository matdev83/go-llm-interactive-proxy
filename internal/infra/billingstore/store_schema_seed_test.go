package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
	"modernc.org/sqlite"
)

// The billing schema is built by a long chain of SQLite migrations. Running the
// whole chain for every fixture is the dominant Windows test cost: a fresh
// file-backed database costs hundreds of milliseconds because each migration
// statement is flushed individually. Fixtures restore the migrated database
// image instead of reparsing its DDL, while each test still owns a private
// database with its original connection/pooling policy. Production
// construction still runs the real migration chain.
//
// The immutable image contains the complete migration output. DDL and history
// are also captured for fixtures that deliberately replay individual objects.
// Restoring it makes the subsequent NewDurableStore migration a no-op, so
// fixtures exercise the same store
// construction path as production after migration.
var (
	testSchemaOnce sync.Once
	testSchemaDDL  []string
	testSchemaRows [][]any
	testSchemaErr  error
	testSchemaDir  string
	testSchemaPath string
)

type testSchemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

const testSchemaInsertMigrationSQL = `INSERT INTO bun_billing_migrations (id, name, group_id, migrated_at) VALUES (?,?,?,?)`

func loadTestSchema() error {
	testSchemaOnce.Do(func() {
		ctx := context.Background()
		sqlDB, err := sql.Open("sqlite", "file:billingstore-test-schema-template?mode=memory&cache=shared&_pragma=foreign_keys(ON)")
		if err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: open template: %w", err)
			return
		}
		defer func() { _ = sqlDB.Close() }()
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: bun template: %w", err)
			return
		}
		if err := Migrate(ctx, bunDB); err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: migrate template: %w", err)
			return
		}
		rows, err := sqlDB.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
		if err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: dump objects: %w", err)
			return
		}
		for rows.Next() {
			var stmt string
			if err := rows.Scan(&stmt); err != nil {
				_ = rows.Close()
				testSchemaErr = fmt.Errorf("billingstore test schema: scan object: %w", err)
				return
			}
			testSchemaDDL = append(testSchemaDDL, stmt)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			testSchemaErr = fmt.Errorf("billingstore test schema: iterate objects: %w", err)
			return
		}
		if err := rows.Close(); err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: close objects: %w", err)
			return
		}
		mRows, err := sqlDB.QueryContext(ctx, `SELECT id, name, group_id, migrated_at FROM bun_billing_migrations ORDER BY id`)
		if err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: dump history: %w", err)
			return
		}
		for mRows.Next() {
			var id, name, groupID, migratedAt any
			if err := mRows.Scan(&id, &name, &groupID, &migratedAt); err != nil {
				_ = mRows.Close()
				testSchemaErr = fmt.Errorf("billingstore test schema: scan history: %w", err)
				return
			}
			testSchemaRows = append(testSchemaRows, []any{id, name, groupID, migratedAt})
		}
		if err := mRows.Err(); err != nil {
			_ = mRows.Close()
			testSchemaErr = fmt.Errorf("billingstore test schema: iterate history: %w", err)
			return
		}
		if err := mRows.Close(); err != nil {
			testSchemaErr = fmt.Errorf("billingstore test schema: close history: %w", err)
			return
		}
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			testSchemaErr = err
			return
		}
		defer func() { _ = conn.Close() }()
		var image []byte
		if err := conn.Raw(func(raw any) error {
			serializer, ok := raw.(interface{ Serialize() ([]byte, error) })
			if !ok {
				return fmt.Errorf("sqlite fixture driver does not support serialization")
			}
			image, err = serializer.Serialize()
			return err
		}); err != nil {
			testSchemaErr = err
			return
		}
		testSchemaDir, err = os.MkdirTemp("", "lip-billing-schema-")
		if err != nil {
			testSchemaErr = err
			return
		}
		testSchemaPath = filepath.Join(testSchemaDir, "schema.sqlite")
		testSchemaErr = os.WriteFile(testSchemaPath, image, 0o600)
	})
	return testSchemaErr
}

func applyTestSchema(ctx context.Context, ex testSchemaExecer) error {
	if err := loadTestSchema(); err != nil {
		return err
	}
	for _, stmt := range testSchemaDDL {
		if _, err := ex.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("billingstore test schema: apply object: %w", err)
		}
	}
	for _, row := range testSchemaRows {
		if _, err := ex.ExecContext(ctx, testSchemaInsertMigrationSQL, row...); err != nil {
			return fmt.Errorf("billingstore test schema: apply history: %w", err)
		}
	}
	return nil
}

// seedTestSchemaIfEmpty materializes the migrated billing schema on a fresh
// SQLite database without re-running the migration chain. It is a no-op when
// the database already contains user tables, so restart tests that reopen the
// same database or upgrade tests that pre-build a legacy schema still run the
// real migrations. Each fixture owns its own database, so this never shares
// mutable state across tests.
func seedTestSchemaIfEmpty(tb testing.TB, bunDB *bun.DB) {
	tb.Helper()
	ctx := context.Background()
	var existing int
	if err := bunDB.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(ctx, &existing); err != nil {
		tb.Fatalf("billingstore test schema: inspect database: %v", err)
	}
	if existing != 0 {
		return
	}
	if err := loadTestSchema(); err != nil {
		tb.Fatal(err)
	}
	conn, err := bunDB.DB.Conn(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Raw(func(raw any) error {
		restorer, ok := raw.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite fixture driver does not support backup restoration")
		}
		backup, err := restorer.NewRestore("file:" + filepath.ToSlash(testSchemaPath) + "?mode=ro")
		if err != nil {
			return err
		}
		_, stepErr := backup.Step(-1)
		return errors.Join(stepErr, backup.Finish())
	}); err != nil {
		tb.Fatalf("billingstore test schema: clone: %v", err)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if testSchemaDir != "" {
		// Only remove the exact task-owned directory created under the OS temp root.
		tempRoot, err := filepath.Abs(os.TempDir())
		if err != nil || filepath.Dir(testSchemaDir) != tempRoot {
			os.Exit(1)
		}
		if err := os.RemoveAll(testSchemaDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

// TestLoadTestSchemaCapturesEveryMigration guards the template capture against a
// truncated iteration: a partial sqlite_master or migration-history read must
// surface as an error rather than being cached by sync.Once and replayed by
// every fixture.
func TestLoadTestSchemaCapturesEveryMigration(t *testing.T) {
	t.Parallel()
	if err := loadTestSchema(); err != nil {
		t.Fatalf("loadTestSchema: %v", err)
	}
	if len(testSchemaDDL) == 0 {
		t.Fatal("captured no schema objects")
	}
	registerMigrations()
	if got, want := len(testSchemaRows), len(migrations.Sorted()); got != want {
		t.Fatalf("captured %d migration history rows, want %d", got, want)
	}
}
