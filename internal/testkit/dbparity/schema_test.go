package dbparity_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func newSQLiteTestDB(t *testing.T) *bun.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema_test.db")
	dsn, err := db.SQLiteFileDSN(path)
	if err != nil {
		t.Fatalf("SQLiteFileDSN: %v", err)
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	bunDB, err := db.NewBunDB(sqldb, db.DialectSQLite)
	if err != nil {
		t.Fatalf("NewBunDB: %v", err)
	}
	return bunDB
}

func TestVerifySchema_SQLite_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	stmts := []string{
		`CREATE TABLE users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL,
	email TEXT,
	is_active BOOLEAN NOT NULL DEFAULT 1,
	age INTEGER,
	metadata JSON,
	avatar BLOB,
	created_at TIMESTAMP NOT NULL
)`,
		`CREATE TABLE orders (
	order_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	amount_nano INTEGER NOT NULL CHECK(amount_nano > 0),
	status TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX idx_users_username ON users(username)`,
		`CREATE INDEX idx_orders_pending ON orders(status, amount_nano) WHERE status = 'pending'`,
		`CREATE TRIGGER orders_immutable_delete BEFORE DELETE ON orders
BEGIN
	SELECT RAISE(ABORT, 'orders are immutable');
END`,
	}
	for _, stmt := range stmts {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec DDL failed: %v\nSQL:\n%s", err, stmt)
		}
	}

	spec := dbparity.LogicalSchemaSpec{
		ComponentID: "test-commerce",
		Tables: []dbparity.TableSpec{
			{
				Name: "users",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "username", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "email", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(true)},
					{Name: "is_active", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), DefaultValue: "1"},
					{Name: "age", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(true)},
					{Name: "metadata", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(true)},
					{Name: "avatar", Type: dbparity.TypeBlob, Nullable: dbparity.PtrBool(true)},
					{Name: "created_at", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"username"}},
				},
			},
			{
				Name: "orders",
				Columns: []dbparity.ColumnSpec{
					{Name: "order_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "user_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "amount_nano", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "status", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"order_id"},
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"user_id"}, RefTable: "users"},
				},
				CheckConstraints: []dbparity.CheckConstraintSpec{
					{Expression: "amount_nano > 0"},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_users_username",
				Table:   "users",
				Columns: []string{"username"},
				Unique:  true,
			},
			{
				Name:      "idx_orders_pending",
				Table:     "orders",
				Columns:   []string{"status", "amount_nano"},
				Unique:    false,
				Predicate: "status = 'pending'",
			},
		},
		Protections: []dbparity.ImmutabilityProtection{
			{
				Name:        "orders-immutability",
				Table:       "orders",
				TriggerName: "orders_immutable_delete",
			},
			{
				Name:         "users-app-level-immutability",
				Table:        "users",
				AppLevelOnly: true,
				Description:  "Protected by application layer assertions",
			},
		},
		Retired: dbparity.RetiredArtifacts{
			Tables:   []string{"legacy_orders", "temp_users"},
			Columns:  []dbparity.RetiredColumn{{Table: "users", Column: "legacy_phone"}, {Table: "users", Column: "old_%_col"}},
			Indexes:  []string{"idx_users_legacy_id"},
			Triggers: []string{"trg_old_update"},
		},
	}

	if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySQLiteSchema unexpected error: %v", err)
	}

	if err := dbparity.VerifySchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySchema unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{Name: "missing_table"},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing table")
	}
	if !strings.Contains(err.Error(), "table \"missing_table\" not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "accounts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id"},
					{Name: "balance_nano"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing column")
	}
	if !strings.Contains(err.Error(), "missing column \"balance_nano\"") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_ColumnTypeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, count TEXT NOT NULL);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "accounts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeText},
					{Name: "count", Type: dbparity.TypeInteger},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for column type mismatch")
	}
	if !strings.Contains(err.Error(), "type mismatch") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestVerifySchema_SQLite_NullabilityMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, optional_field TEXT, required_field TEXT NOT NULL);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("expected not null but got nullable", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "optional_field", Nullable: dbparity.PtrBool(false)},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "must be NOT NULL") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("expected nullable but got not null", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "required_field", Nullable: dbparity.PtrBool(true)},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "must be nullable") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_PrimaryKeyMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE compound_pk (store_id TEXT, row_key TEXT, PRIMARY KEY (store_id, row_key));"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("matching compound PK", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name:       "compound_pk",
					PrimaryKey: []string{"store_id", "row_key"},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mismatched PK columns", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name:       "compound_pk",
					PrimaryKey: []string{"store_id"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error for PK mismatch")
		}
		if !strings.Contains(err.Error(), "primary key mismatch") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_MissingIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE logs (id TEXT PRIMARY KEY, level TEXT);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_logs_level",
				Table:   "logs",
				Columns: []string{"level"},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !strings.Contains(err.Error(), "missing SQLite index \"idx_logs_level\"") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE ledger (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Protections: []dbparity.ImmutabilityProtection{
			{
				Name:        "ledger_immutable",
				Table:       "ledger",
				TriggerName: "ledger_no_delete",
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing trigger")
	}
	if !strings.Contains(err.Error(), "missing SQLite immutability trigger \"ledger_no_delete\"") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingCheckConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE items (id TEXT PRIMARY KEY, count INTEGER CHECK(count > 0));"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "items",
				CheckConstraints: []dbparity.CheckConstraintSpec{
					{Expression: "count > 100"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing check constraint")
	}
	if !strings.Contains(err.Error(), "missing check constraint") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_MissingForeignKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)

	if _, err := bunDB.ExecContext(ctx, "CREATE TABLE parent (id TEXT PRIMARY KEY); CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT);"); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	spec := dbparity.LogicalSchemaSpec{
		Tables: []dbparity.TableSpec{
			{
				Name: "child",
				ForeignKeys: []dbparity.ForeignKeySpec{
					{Columns: []string{"parent_id"}, RefTable: "parent"},
				},
			},
		},
	}

	err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
	if err == nil {
		t.Fatal("expected error for missing foreign key")
	}
	if !strings.Contains(err.Error(), "missing foreign key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifySchema_SQLite_RetiredArtifactPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("retired table present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE retired_table (id TEXT PRIMARY KEY);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Tables: []string{"retired_table"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired table is present")
		}
		if !strings.Contains(err.Error(), "retired table \"retired_table\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired column present exact", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, old_balance INTEGER);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "old_balance"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired column is present")
		}
		if !strings.Contains(err.Error(), "table \"accounts\" contains retired column \"old_balance\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired column present pattern wildcard", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, reserved_nano INTEGER);"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "reserved%nano"},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired column pattern matches")
		}
		if !strings.Contains(err.Error(), "table \"accounts\" contains retired column \"reserved_nano\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired index present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT); CREATE INDEX idx_retired ON accounts(name);"); err != nil {
			t.Fatalf("create table and index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Indexes: []string{"idx_retired"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired index is present")
		}
		if !strings.Contains(err.Error(), "retired index \"idx_retired\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("retired trigger present", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE accounts (id TEXT PRIMARY KEY); CREATE TRIGGER trg_retired BEFORE DELETE ON accounts BEGIN SELECT 1; END;"); err != nil {
			t.Fatalf("create table and trigger: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Triggers: []string{"trg_retired"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected error when retired trigger is present")
		}
		if !strings.Contains(err.Error(), "retired trigger \"trg_retired\" is still present") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVerifySchema_NilValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := newSQLiteTestDB(t)
	spec := dbparity.LogicalSchemaSpec{}

	if err := dbparity.VerifySchema(nil, bunDB, spec); err == nil {
		t.Error("VerifySchema(nil ctx) should error")
	}
	if err := dbparity.VerifySchema(ctx, nil, spec); err == nil {
		t.Error("VerifySchema(nil db) should error")
	}
	if err := dbparity.VerifySQLiteSchema(nil, bunDB, spec); err == nil {
		t.Error("VerifySQLiteSchema(nil ctx) should error")
	}
	if err := dbparity.VerifySQLiteSchema(ctx, nil, spec); err == nil {
		t.Error("VerifySQLiteSchema(nil db) should error")
	}
	if err := dbparity.VerifyPostgresSchema(nil, bunDB, spec); err == nil {
		t.Error("VerifyPostgresSchema(nil ctx) should error")
	}
	if err := dbparity.VerifyPostgresSchema(ctx, nil, spec); err == nil {
		t.Error("VerifyPostgresSchema(nil db) should error")
	}
}

func TestVerifySchema_SQLite_NamedIndex_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("uniqueness mismatch: expected unique got non-unique", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Unique: true},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("uniqueness mismatch: expected non-unique got unique", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE UNIQUE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Unique: false},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("columns mismatch: wrong columns", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, name TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"name"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("columns mismatch: wrong order", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, a TEXT, b TEXT); CREATE INDEX idx_users_ab ON users(a, b);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_ab", Table: "users", Columns: []string{"b", "a"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("owning table mismatch", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE TABLE accounts (id TEXT PRIMARY KEY); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "accounts", Columns: []string{"email"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "owning table mismatch") {
			t.Fatalf("expected owning table mismatch error, got: %v", err)
		}
	})

	t.Run("missing predicate on partial spec", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email);"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Predicate: "email != ''"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing predicate") {
			t.Fatalf("expected missing predicate error, got: %v", err)
		}
	})

	t.Run("unexpected predicate on non-partial spec", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT); CREATE INDEX idx_users_email ON users(email) WHERE email != '';"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "unexpected partial predicate") {
			t.Fatalf("expected unexpected partial predicate error, got: %v", err)
		}
	})

	t.Run("predicate expression mismatch", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, status TEXT); CREATE INDEX idx_users_email ON users(email) WHERE status = 'active';"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_users_email", Table: "users", Columns: []string{"email"}, Predicate: "status = 'pending'"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected predicate mismatch error, got: %v", err)
		}
	})

	t.Run("predicate containment rejected: stronger actual predicate fails", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, "CREATE TABLE sessions (id TEXT PRIMARY KEY, a_leg_id TEXT, extra_flag INTEGER); CREATE INDEX idx_sessions_a_leg ON sessions(a_leg_id) WHERE a_leg_id != '' AND extra_flag = 1;"); err != nil {
			t.Fatalf("create table/index: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_sessions_a_leg", Table: "sessions", Columns: []string{"a_leg_id"}, Predicate: "a_leg_id != ''"},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected predicate mismatch error for stronger actual predicate, got nil")
		}
		if !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected error containing 'predicate mismatch', got: %v", err)
		}
		if !strings.Contains(err.Error(), "a_leg_id != '' AND extra_flag = 1") || !strings.Contains(err.Error(), "a_leg_id != ''") {
			t.Fatalf("expected error naming both actual and expected fragments, got: %v", err)
		}
	})
}

func TestVerifySchema_SQLite_DefaultValue_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exact default passes", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'active',
	retry_count INTEGER NOT NULL DEFAULT 0,
	flag INTEGER NOT NULL DEFAULT 1,
	empty_str TEXT NOT NULL DEFAULT '',
	json_meta TEXT NOT NULL DEFAULT '{}'
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "status", Type: dbparity.TypeText, Default: "'active'"},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "0"},
						{Name: "flag", Type: dbparity.TypeInteger, Default: "1"},
						{Name: "empty_str", Type: dbparity.TypeText, Default: "''"},
						{Name: "json_meta", Type: dbparity.TypeText, Default: "'{}'"},
					},
				},
			},
		}
		if err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec); err != nil {
			t.Fatalf("expected exact defaults to pass, got: %v", err)
		}
	})

	t.Run("default present but different fails naming expected and got", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	retry_count INTEGER NOT NULL DEFAULT 0
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "5"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected default mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "default mismatch") {
			t.Fatalf("expected error containing 'default mismatch', got: %v", err)
		}
		if !strings.Contains(err.Error(), "got \"0\"") || !strings.Contains(err.Error(), "want \"5\"") {
			t.Fatalf("expected error naming got '0' and want '5', got: %v", err)
		}
	})

	t.Run("default removed fails", func(t *testing.T) {
		bunDB := newSQLiteTestDB(t)
		if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE items (
	id TEXT PRIMARY KEY,
	retry_count INTEGER NOT NULL
);`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "items",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeText, PrimaryKey: true},
						{Name: "retry_count", Type: dbparity.TypeInteger, Default: "0"},
					},
				},
			},
		}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		if err == nil {
			t.Fatal("expected default mismatch error when default is removed, got nil")
		}
		if !strings.Contains(err.Error(), "default mismatch") || !strings.Contains(err.Error(), "got no default") {
			t.Fatalf("expected error naming 'got no default', got: %v", err)
		}
	})
}


