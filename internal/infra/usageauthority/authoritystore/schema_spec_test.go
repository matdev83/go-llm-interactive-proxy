package authoritystore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

// UsageAuthorityLogicalSchemaSpec returns the declared logical schema invariant specification
// for the usage authority persistence component across SQLite and PostgreSQL.
func UsageAuthorityLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "usage-authority",
		Tables: []dbparity.TableSpec{
			{
				Name: "usage_authority_state",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "readiness_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "next_decision_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id"},
			},
			{
				Name: "usage_authority_limit_rows",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "row_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "row_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "row_key"},
			},
			{
				Name: "usage_authority_decisions",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "decision_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "source_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "row_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "decision_seq"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "source_key"}},
				},
			},
			{
				Name: "usage_authority_decision_filters",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "decision_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "field_name", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "field_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "decision_seq", "field_name"},
			},
			{
				Name: "usage_authority_limit_filters",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "row_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "field_name", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "field_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "row_key", "field_name"},
			},
			{
				Name: "usage_authority_reservations",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "reservation_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "source_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "record_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "reservation_key"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "source_key"}},
				},
			},
			{
				Name: "usage_authority_unreserved_usage_facts",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "fact_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "record_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "fact_key"},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "usage_authority_decision_filters_lookup",
				Table:   "usage_authority_decision_filters",
				Columns: []string{"store_id", "field_name", "field_value", "decision_seq"},
				Unique:  false,
			},
			{
				Name:    "usage_authority_limit_filters_lookup",
				Table:   "usage_authority_limit_filters",
				Columns: []string{"store_id", "field_name", "field_value", "row_key"},
				Unique:  false,
			},
		},
	}
}

func TestUsageAuthoritySchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newTestDB := func(t *testing.T) *bun.DB {
		t.Helper()
		path := filepath.Join(t.TempDir(), "negative.db")
		dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", filepath.ToSlash(path))
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		require.NoError(t, Migrate(ctx, bunDB))
		return bunDB
	}

	t.Run("missing column fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)
		spec := UsageAuthorityLogicalSchemaSpec()
		spec.Tables[0].Columns = append(spec.Tables[0].Columns, dbparity.ColumnSpec{
			Name: "non_existent_column",
			Type: dbparity.TypeText,
		})
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing column")
	})

	t.Run("wrong index column fails", func(t *testing.T) {
		t.Parallel()
		bunDB := newTestDB(t)
		spec := UsageAuthorityLogicalSchemaSpec()
		spec.Indexes[0].Columns = []string{"store_id", "decision_seq"}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})
}
