//go:build integration

package dbparity_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/uptrace/bun"
)

func newPostgresTestDB(t *testing.T) (*bun.DB, string) {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	bunDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{})
	if err != nil {
		t.Fatalf("OpenPostgresBun: %v", err)
	}

	schemaName := fmt.Sprintf("test_schema_parity_%d", time.Now().UnixNano())
	if _, err := bunDB.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s; SET search_path TO %s;", schemaName, schemaName)); err != nil {
		_ = bunDB.Close()
		t.Fatalf("create isolated schema: %v", err)
	}

	t.Cleanup(func() {
		_, _ = bunDB.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schemaName))
		_ = bunDB.Close()
	})

	return bunDB, schemaName
}

func TestVerifySchema_Postgres_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	stmts := []string{
		`CREATE TABLE users (
	id VARCHAR(64) PRIMARY KEY,
	username TEXT NOT NULL,
	email TEXT,
	is_active BOOLEAN NOT NULL DEFAULT true,
	age INTEGER,
	metadata JSONB,
	avatar BYTEA,
	created_at TIMESTAMPTZ NOT NULL
)`,
		`CREATE TABLE orders (
	order_id VARCHAR(64) PRIMARY KEY,
	user_id VARCHAR(64) NOT NULL REFERENCES users(id),
	amount_nano BIGINT NOT NULL CHECK(amount_nano > 0),
	status TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX idx_users_username ON users(username)`,
		`CREATE INDEX idx_orders_pending ON orders(status, amount_nano) WHERE status = 'pending'`,
		`CREATE OR REPLACE FUNCTION prevent_orders_delete() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'orders are immutable';
END;
$$ LANGUAGE plpgsql`,
		`CREATE TRIGGER orders_immutable_delete BEFORE DELETE ON orders
FOR EACH ROW EXECUTE FUNCTION prevent_orders_delete()`,
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
					{Name: "is_active", Type: dbparity.TypeBoolean, Nullable: dbparity.PtrBool(false), DefaultValue: "true"},
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

	if err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifyPostgresSchema unexpected error: %v", err)
	}

	if err := dbparity.VerifySchema(ctx, bunDB, spec); err != nil {
		t.Fatalf("VerifySchema unexpected error: %v", err)
	}
}

func TestVerifySchema_Postgres_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB, _ := newPostgresTestDB(t)

	if _, err := bunDB.ExecContext(ctx, `
CREATE TABLE accounts (
	id VARCHAR(64) PRIMARY KEY,
	balance BIGINT NOT NULL,
	retired_old_col TEXT
);
CREATE TABLE items (
	id VARCHAR(64) PRIMARY KEY,
	count INTEGER CHECK(count > 0)
);
`); err != nil {
		t.Fatalf("exec DDL: %v", err)
	}

	t.Run("missing table", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{{Name: "non_existent_table"}},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "table \"non_existent_table\" not found") {
			t.Fatalf("expected missing table error, got: %v", err)
		}
	})

	t.Run("missing column", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "missing_col"},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing column \"missing_col\"") {
			t.Fatalf("expected missing column error, got: %v", err)
		}
	})

	t.Run("column type mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "id", Type: dbparity.TypeInteger},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "type mismatch") {
			t.Fatalf("expected type mismatch error, got: %v", err)
		}
	})

	t.Run("nullability mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Tables: []dbparity.TableSpec{
				{
					Name: "accounts",
					Columns: []dbparity.ColumnSpec{
						{Name: "balance", Nullable: dbparity.PtrBool(true)},
					},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "must be nullable") {
			t.Fatalf("expected nullability mismatch error, got: %v", err)
		}
	})

	t.Run("missing index", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{
					Name:    "idx_missing_pg",
					Table:   "accounts",
					Columns: []string{"balance"},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing PostgreSQL index \"idx_missing_pg\"") {
			t.Fatalf("expected missing index error, got: %v", err)
		}
	})

	t.Run("retired column present", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Retired: dbparity.RetiredArtifacts{
				Columns: []dbparity.RetiredColumn{
					{Table: "accounts", Column: "retired_old_col"},
				},
			},
		}
		err := dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "contains retired column \"retired_old_col\"") {
			t.Fatalf("expected retired column error, got: %v", err)
		}
	})

	t.Run("index uniqueness mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_balance", Table: "accounts", Columns: []string{"balance"}, Unique: true},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_balance ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "uniqueness mismatch") {
			t.Fatalf("expected uniqueness mismatch error, got: %v", err)
		}
	})

	t.Run("index columns mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_balance_col", Table: "accounts", Columns: []string{"id"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_balance_col ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "columns mismatch") {
			t.Fatalf("expected columns mismatch error, got: %v", err)
		}
	})

	t.Run("index owning table mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_tbl", Table: "items", Columns: []string{"balance"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_tbl ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "owning table mismatch") {
			t.Fatalf("expected owning table mismatch error, got: %v", err)
		}
	})

	t.Run("index missing predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_miss", Table: "accounts", Columns: []string{"balance"}, Predicate: "balance > 0"},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_miss ON accounts(balance);")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "missing predicate") {
			t.Fatalf("expected missing predicate error, got: %v", err)
		}
	})

	t.Run("index unexpected partial predicate", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_unexp", Table: "accounts", Columns: []string{"balance"}},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_unexp ON accounts(balance) WHERE balance > 0;")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "unexpected partial predicate") {
			t.Fatalf("expected unexpected partial predicate error, got: %v", err)
		}
	})

	t.Run("index predicate mismatch", func(t *testing.T) {
		spec := dbparity.LogicalSchemaSpec{
			Indexes: []dbparity.IndexSpec{
				{Name: "idx_test_accounts_pred_diff", Table: "accounts", Columns: []string{"balance"}, Predicate: "balance > 100"},
			},
		}
		_, err := bunDB.ExecContext(ctx, "CREATE INDEX idx_test_accounts_pred_diff ON accounts(balance) WHERE balance > 0;")
		if err != nil {
			t.Fatalf("create index: %v", err)
		}
		err = dbparity.VerifyPostgresSchema(ctx, bunDB, spec)
		if err == nil || !strings.Contains(err.Error(), "predicate mismatch") {
			t.Fatalf("expected predicate mismatch error, got: %v", err)
		}
	})
}
