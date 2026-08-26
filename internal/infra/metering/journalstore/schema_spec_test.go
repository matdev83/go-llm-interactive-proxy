package journalstore

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
	_ "modernc.org/sqlite"
)

// MeteringJournalLogicalSchemaSpec returns the declared logical schema invariant specification
// for the metering journal persistence component across SQLite and PostgreSQL.
func MeteringJournalLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "metering-journal",
		Tables: []dbparity.TableSpec{
			{
				Name: "metering_facts",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "sequence", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "source_event_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "perspective", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "boundary", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "lifecycle_scope", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "request_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "b_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "attempt_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "frontend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "backend_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "presence", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "authority", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "recorded_at_unix", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "payload_json", Type: dbparity.TypeJSON, Nullable: dbparity.PtrBool(false)},
					{Name: "identity_version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "source_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), Default: "0"},
					{Name: "source_event_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
					{Name: "source_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), Default: "''"},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "source_event_key"}},
				},
			},
			{
				Name: "metering_fact_filters",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "field_name", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "field_value", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
			},
			{
				Name: "metering_fact_supersessions",
				Columns: []dbparity.ColumnSpec{
					{Name: "id", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "stream_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "from_fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "to_fact_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"id"},
				UniqueConstraints: []dbparity.UniqueConstraintSpec{
					{Columns: []string{"store_id", "stream_id", "from_fact_id", "to_fact_id"}},
				},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{
				Name:    "idx_metering_facts_stream_seq",
				Table:   "metering_facts",
				Columns: []string{"stream_id", "sequence"},
				Unique:  false,
			},
			{
				Name:      "idx_metering_facts_request",
				Table:     "metering_facts",
				Columns:   []string{"request_id"},
				Unique:    false,
				Predicate: "request_id != ''",
			},
			{
				Name:    "metering_facts_store_stream_fact_id_key",
				Table:   "metering_facts",
				Columns: []string{"store_id", "stream_id", "fact_id"},
				Unique:  true,
			},
			{
				Name:    "idx_metering_facts_store_stream_seq",
				Table:   "metering_facts",
				Columns: []string{"store_id", "stream_id", "sequence"},
				Unique:  false,
			},
			{
				Name:      "idx_metering_facts_store_attempt",
				Table:     "metering_facts",
				Columns:   []string{"store_id", "attempt_id"},
				Unique:    false,
				Predicate: "attempt_id != ''",
			},
			{
				Name:    "idx_metering_facts_store_recorded",
				Table:   "metering_facts",
				Columns: []string{"store_id", "recorded_at_unix"},
				Unique:  false,
			},
			{
				Name:    "idx_metering_facts_store_plane",
				Table:   "metering_facts",
				Columns: []string{"store_id", "perspective", "boundary", "lifecycle_scope"},
				Unique:  false,
			},
			{
				Name:    "idx_metering_fact_filters_field",
				Table:   "metering_fact_filters",
				Columns: []string{"store_id", "field_name", "field_value", "stream_id"},
				Unique:  false,
			},
			{
				Name:    "idx_metering_fact_supersessions_to",
				Table:   "metering_fact_supersessions",
				Columns: []string{"store_id", "stream_id", "to_fact_id"},
				Unique:  false,
			},
		},
	}
}

func TestMeteringJournalSchemaSpec_NegativeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "negative.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", filepath.ToSlash(path))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer sqlDB.Close()
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, bunDB))

	t.Run("missing column fails", func(t *testing.T) {
		spec := MeteringJournalLogicalSchemaSpec()
		spec.Tables[0].Columns = append(spec.Tables[0].Columns, dbparity.ColumnSpec{
			Name: "missing_field",
			Type: dbparity.TypeText,
		})
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing column")
	})

	t.Run("wrong index column fails", func(t *testing.T) {
		spec := MeteringJournalLogicalSchemaSpec()
		spec.Indexes[0].Columns = []string{"stream_id"}
		err := dbparity.VerifySQLiteSchema(ctx, bunDB, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "columns mismatch")
	})
}
