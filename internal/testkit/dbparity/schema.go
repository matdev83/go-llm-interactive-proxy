package dbparity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// SemanticType represents an engine-agnostic data type category.
type SemanticType string

const (
	TypeText      SemanticType = "text"
	TypeInteger   SemanticType = "integer"
	TypeBoolean   SemanticType = "boolean"
	TypeTimestamp SemanticType = "timestamp"
	TypeBlob      SemanticType = "blob"
	TypeJSON      SemanticType = "json"
	TypeNumeric   SemanticType = "numeric"
)

// PtrBool is a convenience helper returning a pointer to a bool value.
func PtrBool(b bool) *bool {
	return &b
}

// ColumnSpec describes expected column-level invariants.
type ColumnSpec struct {
	Name            string       `json:"name"`
	Type            SemanticType `json:"type,omitempty"`             // Optional semantic type category
	Nullable        *bool        `json:"nullable,omitempty"`         // Optional nullability expectation
	PrimaryKey      bool         `json:"primary_key,omitempty"`      // Part of primary key
	Default         string       `json:"default,omitempty"`          // Expected default fragment ("" means no assertion)
	DefaultValue    string       `json:"default_value,omitempty"`    // Deprecated alias / backwards compatibility for Default
	DefaultPostgres string       `json:"default_postgres,omitempty"` // Optional engine-specific default override for PostgreSQL
}

func (c ColumnSpec) expectedDefault(isPostgres bool) string {
	if isPostgres && c.DefaultPostgres != "" {
		return c.DefaultPostgres
	}
	if c.Default != "" {
		return c.Default
	}
	return c.DefaultValue
}

// ForeignKeySpec describes expected foreign key constraints.
type ForeignKeySpec struct {
	Name       string   `json:"name,omitempty"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns,omitempty"`
}

// UniqueConstraintSpec describes expected unique constraints.
type UniqueConstraintSpec struct {
	Name    string   `json:"name,omitempty"`
	Columns []string `json:"columns"`
}

// CheckConstraintSpec describes expected check constraints.
type CheckConstraintSpec struct {
	Name       string `json:"name,omitempty"`
	Expression string `json:"expression"` // Substring/fragment expected in check definition
}

// TableSpec describes expected table-level logical schema invariants.
type TableSpec struct {
	Name              string                 `json:"name"`
	Columns           []ColumnSpec           `json:"columns,omitempty"`
	PrimaryKey        []string               `json:"primary_key,omitempty"` // Compound/single PK column order
	ForeignKeys       []ForeignKeySpec       `json:"foreign_keys,omitempty"`
	UniqueConstraints []UniqueConstraintSpec `json:"unique_constraints,omitempty"`
	CheckConstraints  []CheckConstraintSpec  `json:"check_constraints,omitempty"`
}

// IndexSpec describes expected index invariants.
type IndexSpec struct {
	Name      string   `json:"name,omitempty"`      // Optional if unique signature is matched
	Table     string   `json:"table"`               // Table on which index resides
	Columns   []string `json:"columns,omitempty"`   // Column list (ordered)
	Unique    bool     `json:"unique,omitempty"`    // Whether the index is unique
	Predicate string   `json:"predicate,omitempty"` // WHERE clause fragment for partial indexes
}

// ImmutabilityProtection describes expected immutability protections (triggers or documented app-level enforcement).
type ImmutabilityProtection struct {
	Name         string `json:"name"`                     // Descriptive name
	Table        string `json:"table"`                    // Target table
	TriggerName  string `json:"trigger_name,omitempty"`   // Required trigger name in DB (if DB-enforced)
	AppLevelOnly bool   `json:"app_level_only,omitempty"` // If true, documented as app-level enforced (absent-by-design in DB)
	Description  string `json:"description,omitempty"`
}

// RetiredColumn represents a column on a table that must not exist.
type RetiredColumn struct {
	Table  string `json:"table"`
	Column string `json:"column"` // Exact name or prefix pattern (e.g. "reserved%nano" or glob)
}

// RetiredArtifacts records tables, columns, indexes, and triggers that must NOT exist.
type RetiredArtifacts struct {
	Tables   []string        `json:"tables,omitempty"`
	Columns  []RetiredColumn `json:"columns,omitempty"`
	Indexes  []string        `json:"indexes,omitempty"`
	Triggers []string        `json:"triggers,omitempty"`
}

// LogicalSchemaSpec represents declared logical schema invariants for a component.
type LogicalSchemaSpec struct {
	ComponentID string                   `json:"component_id,omitempty"`
	Tables      []TableSpec              `json:"tables,omitempty"`
	Indexes     []IndexSpec              `json:"indexes,omitempty"`
	Protections []ImmutabilityProtection `json:"protections,omitempty"`
	Retired     RetiredArtifacts         `json:"retired,omitempty"`
}

// VerifySchema verifies that the database matches the declared logical schema spec,
// dispatching to engine-native metadata probes based on the database dialect.
func VerifySchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}
	switch database.Dialect().Name() {
	case dialect.SQLite:
		return VerifySQLiteSchema(ctx, database, spec)
	case dialect.PG:
		return VerifyPostgresSchema(ctx, database, spec)
	default:
		return fmt.Errorf("dbparity: unsupported database dialect %q for schema verification", database.Dialect().Name())
	}
}

// VerifySQLiteSchema verifies the logical schema spec against a SQLite database
// using sqlite_master and PRAGMA table_info / index_list metadata introspection.
func VerifySQLiteSchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}

	// 1. Check retired artifacts
	for _, table := range spec.Retired.Tables {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired table %q: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired table %q is still present", table)
		}
	}

	for _, rc := range spec.Retired.Columns {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, rc.Table).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: sqlite check table for retired column %q: %w", rc.Table, err)
		}
		if tblCount == 0 {
			continue
		}
		cols, err := sqliteTableColumns(ctx, database, rc.Table)
		if err != nil {
			return err
		}
		for colName := range cols {
			if matchColumnNameOrPattern(rc.Column, colName) {
				return fmt.Errorf("dbparity: table %q contains retired column %q", rc.Table, colName)
			}
		}
	}

	for _, idx := range spec.Retired.Indexes {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired index %q: %w", idx, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired index %q is still present", idx)
		}
	}

	for _, trg := range spec.Retired.Triggers {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired trigger %q: %w", trg, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired trigger %q is still present", trg)
		}
	}

	// 2. Check required tables & columns
	for _, tbl := range spec.Tables {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl.Name).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: sqlite check table %q: %w", tbl.Name, err)
		}
		if tblCount == 0 {
			return fmt.Errorf("dbparity: table %q not found", tbl.Name)
		}

		colMap, err := sqliteTableColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		var actualPKCols []string
		pkMap := make(map[int]string)
		var pkIndices []int
		for _, info := range colMap {
			if info.pk > 0 {
				pkMap[info.pk] = info.name
				pkIndices = append(pkIndices, info.pk)
			}
		}
		slices.Sort(pkIndices)
		for _, idx := range pkIndices {
			actualPKCols = append(actualPKCols, pkMap[idx])
		}

		for _, col := range tbl.Columns {
			info, exists := colMap[strings.ToLower(col.Name)]
			if !exists {
				return fmt.Errorf("dbparity: table %q: missing column %q", tbl.Name, col.Name)
			}

			if col.Type != "" {
				if !matchSQLiteSemanticType(col.Type, info.colType) {
					return fmt.Errorf("dbparity: table %q: column %q type mismatch: got %q, want category %q", tbl.Name, col.Name, info.colType, col.Type)
				}
			}

			if col.Nullable != nil {
				isNotNull := info.notnull != 0 || info.pk > 0
				if *col.Nullable && isNotNull {
					return fmt.Errorf("dbparity: table %q: column %q must be nullable", tbl.Name, col.Name)
				}
				if !*col.Nullable && !isNotNull {
					return fmt.Errorf("dbparity: table %q: column %q must be NOT NULL", tbl.Name, col.Name)
				}
			}

			if col.PrimaryKey && info.pk == 0 {
				return fmt.Errorf("dbparity: table %q: column %q must be part of primary key", tbl.Name, col.Name)
			}

			if expDefault := col.expectedDefault(false); expDefault != "" {
				if !info.dflt.Valid {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got no default, want %q", tbl.Name, col.Name, expDefault)
				}
				if normalizeDefault(info.dflt.String) != normalizeDefault(expDefault) {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got %q, want %q", tbl.Name, col.Name, info.dflt.String, expDefault)
				}
			}
		}

		if len(tbl.PrimaryKey) > 0 {
			if !equalFoldSlice(actualPKCols, tbl.PrimaryKey) {
				return fmt.Errorf("dbparity: table %q: primary key mismatch: got %v, want %v", tbl.Name, actualPKCols, tbl.PrimaryKey)
			}
		}

		// Introspect table DDL for check constraints and foreign keys
		var tableDDL string
		if err := database.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl.Name).Scan(ctx, &tableDDL); err != nil {
			return fmt.Errorf("dbparity: sqlite table definition %q: %w", tbl.Name, err)
		}
		lowerDDL := strings.ToLower(tableDDL)

		for _, chk := range tbl.CheckConstraints {
			if !strings.Contains(lowerDDL, strings.ToLower(chk.Expression)) {
				return fmt.Errorf("dbparity: table %q missing check constraint containing %q", tbl.Name, chk.Expression)
			}
		}

		for _, fk := range tbl.ForeignKeys {
			fkMatched := false
			// Check PRAGMA foreign_key_list
			fkRows, err := database.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list('%s')", escapeIdentifier(tbl.Name)))
			if err == nil {
				for fkRows.Next() {
					var id, seq int
					var refTable, fromCol, toCol, onUpdate, onDelete, match string
					if err := fkRows.Scan(&id, &seq, &refTable, &fromCol, &toCol, &onUpdate, &onDelete, &match); err == nil {
						if strings.EqualFold(refTable, fk.RefTable) {
							if len(fk.Columns) == 1 && strings.EqualFold(fromCol, fk.Columns[0]) {
								fkMatched = true
								break
							}
						}
					}
				}
				_ = fkRows.Close()
			}
			if !fkMatched {
				// Fallback to DDL inspection
				if strings.Contains(lowerDDL, "references "+strings.ToLower(fk.RefTable)) {
					fkMatched = true
				}
			}
			if !fkMatched {
				return fmt.Errorf("dbparity: table %q missing foreign key referencing %q (%v)", tbl.Name, fk.RefTable, fk.Columns)
			}
		}

		for _, uc := range tbl.UniqueConstraints {
			if !sqliteHasUniqueConstraint(ctx, database, tbl.Name, uc.Columns, lowerDDL) {
				return fmt.Errorf("dbparity: table %q missing unique constraint on %v", tbl.Name, uc.Columns)
			}
		}
	}

	// 3. Check required indexes
	for _, idx := range spec.Indexes {
		if idx.Name != "" {
			var tblName string
			var idxSQL sql.NullString
			if err := database.NewRaw(`SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?`, idx.Name).Scan(ctx, &tblName, &idxSQL); err != nil || tblName == "" {
				if err != nil && err != sql.ErrNoRows {
					return fmt.Errorf("dbparity: sqlite check index %q: %w", idx.Name, err)
				}
				return fmt.Errorf("dbparity: missing SQLite index %q on table %q", idx.Name, idx.Table)
			}

			if !strings.EqualFold(tblName, idx.Table) {
				return fmt.Errorf("dbparity: SQLite index %q owning table mismatch: got table %q, want table %q", idx.Name, tblName, idx.Table)
			}

			entries, err := sqliteGetIndexList(ctx, database, tblName)
			if err != nil {
				return fmt.Errorf("dbparity: sqlite get index list for %q: %w", tblName, err)
			}
			var foundEntry *sqliteIndexEntry
			for _, entry := range entries {
				if strings.EqualFold(entry.name, idx.Name) {
					e := entry
					foundEntry = &e
					break
				}
			}
			if foundEntry == nil {
				return fmt.Errorf("dbparity: SQLite index %q not found in table %q index list", idx.Name, idx.Table)
			}

			if foundEntry.unique != idx.Unique {
				return fmt.Errorf("dbparity: SQLite index %q on table %q uniqueness mismatch: got unique=%v, want unique=%v", idx.Name, idx.Table, foundEntry.unique, idx.Unique)
			}

			if len(idx.Columns) > 0 {
				actualCols, err := sqliteIndexColumns(ctx, database, idx.Name)
				if err != nil {
					return fmt.Errorf("dbparity: sqlite query columns for index %q: %w", idx.Name, err)
				}
				if !equalFoldSlice(actualCols, idx.Columns) {
					return fmt.Errorf("dbparity: SQLite index %q on table %q columns mismatch: got %v, want %v", idx.Name, idx.Table, actualCols, idx.Columns)
				}
			}

			if idx.Predicate != "" {
				if !foundEntry.partial {
					return fmt.Errorf("dbparity: SQLite index %q on table %q missing predicate: expected %q, but index is not partial", idx.Name, idx.Table, idx.Predicate)
				}
				if !idxSQL.Valid || !matchPredicate(idx.Predicate, idxSQL.String) {
					return fmt.Errorf("dbparity: SQLite index %q on table %q predicate mismatch: got %q, want matching %q", idx.Name, idx.Table, extractWhereClause(idxSQL.String), idx.Predicate)
				}
			} else if foundEntry.partial {
				return fmt.Errorf("dbparity: SQLite index %q on table %q unexpected partial predicate: got %q, want non-partial index", idx.Name, idx.Table, extractWhereClause(idxSQL.String))
			}
		} else {
			if !sqliteHasIndexMatching(ctx, database, idx.Table, idx.Columns, idx.Unique, idx.Predicate) {
				return fmt.Errorf("dbparity: table %q missing index on %v (unique=%v)", idx.Table, idx.Columns, idx.Unique)
			}
		}
	}

	// 4. Check immutability protections
	for _, p := range spec.Protections {
		if p.AppLevelOnly {
			continue
		}
		if p.TriggerName != "" {
			var count int
			if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, p.TriggerName).Scan(ctx, &count); err != nil || count == 0 {
				if err != nil {
					return fmt.Errorf("dbparity: sqlite check trigger %q: %w", p.TriggerName, err)
				}
				return fmt.Errorf("dbparity: missing SQLite immutability trigger %q on table %q", p.TriggerName, p.Table)
			}
		}
	}

	return nil
}

// VerifyPostgresSchema verifies the logical schema spec against a PostgreSQL database
// using information_schema, pg_indexes, pg_constraint, and pg_trigger metadata introspection.
func VerifyPostgresSchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}

	// 1. Check retired artifacts
	for _, table := range spec.Retired.Tables {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired table %q: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired table %q is still present", table)
		}
	}

	for _, rc := range spec.Retired.Columns {
		var colNames []string
		rows, err := database.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`, rc.Table)
		if err != nil {
			return fmt.Errorf("dbparity: postgres check retired column in table %q: %w", rc.Table, err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return fmt.Errorf("dbparity: postgres scan column: %w", err)
			}
			colNames = append(colNames, name)
		}
		_ = rows.Close()

		for _, colName := range colNames {
			if matchColumnNameOrPattern(rc.Column, colName) {
				return fmt.Errorf("dbparity: table %q contains retired column %q", rc.Table, colName)
			}
		}
	}

	for _, idx := range spec.Retired.Indexes {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, idx).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired index %q: %w", idx, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired index %q is still present", idx)
		}
	}

	for _, trg := range spec.Retired.Triggers {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND tr.tgname = ? AND NOT tr.tgisinternal`, trg).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired trigger %q: %w", trg, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired trigger %q is still present", trg)
		}
	}

	// 2. Check required tables & columns
	for _, tbl := range spec.Tables {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, tbl.Name).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: postgres check table %q: %w", tbl.Name, err)
		}
		if tblCount == 0 {
			return fmt.Errorf("dbparity: table %q not found", tbl.Name)
		}

		colMap, err := postgresTableColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		actualPKCols, err := postgresPrimaryKeyColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		for _, col := range tbl.Columns {
			info, exists := colMap[strings.ToLower(col.Name)]
			if !exists {
				return fmt.Errorf("dbparity: table %q: missing column %q", tbl.Name, col.Name)
			}

			if col.Type != "" {
				if !matchPostgresSemanticType(col.Type, info.dataType, info.udtName) {
					return fmt.Errorf("dbparity: table %q: column %q type mismatch: got %q (%q), want category %q", tbl.Name, col.Name, info.dataType, info.udtName, col.Type)
				}
			}

			if col.Nullable != nil {
				if *col.Nullable && strings.EqualFold(info.isNullable, "NO") {
					return fmt.Errorf("dbparity: table %q: column %q must be nullable", tbl.Name, col.Name)
				}
				if !*col.Nullable && strings.EqualFold(info.isNullable, "YES") {
					return fmt.Errorf("dbparity: table %q: column %q must be NOT NULL", tbl.Name, col.Name)
				}
			}

			if col.PrimaryKey && !slices.Contains(actualPKCols, strings.ToLower(col.Name)) {
				return fmt.Errorf("dbparity: table %q: column %q must be part of primary key", tbl.Name, col.Name)
			}

			if expDefault := col.expectedDefault(true); expDefault != "" {
				if !info.columnDefault.Valid {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got no default, want %q", tbl.Name, col.Name, expDefault)
				}
				if normalizeDefault(info.columnDefault.String) != normalizeDefault(expDefault) {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got %q, want %q", tbl.Name, col.Name, info.columnDefault.String, expDefault)
				}
			}
		}

		if len(tbl.PrimaryKey) > 0 {
			if !equalFoldSlice(actualPKCols, tbl.PrimaryKey) {
				return fmt.Errorf("dbparity: table %q: primary key mismatch: got %v, want %v", tbl.Name, actualPKCols, tbl.PrimaryKey)
			}
		}

		// Check Constraints
		if len(tbl.CheckConstraints) > 0 {
			checkDefs, err := postgresConstraintDefs(ctx, database, tbl.Name, "c")
			if err != nil {
				return err
			}
			for _, chk := range tbl.CheckConstraints {
				found := false
				for _, def := range checkDefs {
					if strings.Contains(strings.ToLower(def), strings.ToLower(chk.Expression)) {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("dbparity: table %q missing check constraint containing %q", tbl.Name, chk.Expression)
				}
			}
		}

		// Foreign Keys
		if len(tbl.ForeignKeys) > 0 {
			fkDefs, err := postgresConstraintDefs(ctx, database, tbl.Name, "f")
			if err != nil {
				return err
			}
			for _, fk := range tbl.ForeignKeys {
				found := false
				for _, def := range fkDefs {
					lowerDef := strings.ToLower(def)
					if strings.Contains(lowerDef, "foreign key") && strings.Contains(lowerDef, strings.ToLower(fk.RefTable)) {
						allColsMatch := true
						for _, col := range fk.Columns {
							if !strings.Contains(lowerDef, strings.ToLower(col)) {
								allColsMatch = false
								break
							}
						}
						if allColsMatch {
							found = true
							break
						}
					}
				}
				if !found {
					return fmt.Errorf("dbparity: table %q missing foreign key referencing %q (%v)", tbl.Name, fk.RefTable, fk.Columns)
				}
			}
		}

		// Unique Constraints
		if len(tbl.UniqueConstraints) > 0 {
			uniqueDefs, err := postgresConstraintDefs(ctx, database, tbl.Name, "u")
			if err != nil {
				return err
			}
			for _, uc := range tbl.UniqueConstraints {
				found := false
				for _, def := range uniqueDefs {
					lowerDef := strings.ToLower(def)
					allColsMatch := true
					for _, col := range uc.Columns {
						if !strings.Contains(lowerDef, strings.ToLower(col)) {
							allColsMatch = false
							break
						}
					}
					if allColsMatch {
						found = true
						break
					}
				}
				if !found {
					// Also check unique indexes
					var idxDefs []string
					rows, err := database.QueryContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ?`, tbl.Name)
					if err == nil {
						for rows.Next() {
							var idef string
							if err := rows.Scan(&idef); err == nil {
								idxDefs = append(idxDefs, idef)
							}
						}
						_ = rows.Close()
					}
					for _, idef := range idxDefs {
						lowerDef := strings.ToLower(idef)
						if strings.Contains(lowerDef, "unique") {
							allColsMatch := true
							for _, col := range uc.Columns {
								if !strings.Contains(lowerDef, strings.ToLower(col)) {
									allColsMatch = false
									break
								}
							}
							if allColsMatch {
								found = true
								break
							}
						}
					}
				}
				if !found {
					return fmt.Errorf("dbparity: table %q missing unique constraint on %v", tbl.Name, uc.Columns)
				}
			}
		}
	}

	// 3. Check required indexes
	for _, idx := range spec.Indexes {
		if idx.Name != "" {
			info, err := postgresGetIndexDetailed(ctx, database, idx.Name)
			if err != nil || info == nil {
				if err != nil {
					return fmt.Errorf("dbparity: postgres check index %q: %w", idx.Name, err)
				}
				return fmt.Errorf("dbparity: missing PostgreSQL index %q on table %q", idx.Name, idx.Table)
			}

			if !strings.EqualFold(info.tableName, idx.Table) {
				return fmt.Errorf("dbparity: PostgreSQL index %q owning table mismatch: got table %q, want table %q", idx.Name, info.tableName, idx.Table)
			}

			if info.unique != idx.Unique {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q uniqueness mismatch: got unique=%v, want unique=%v", idx.Name, idx.Table, info.unique, idx.Unique)
			}

			if len(idx.Columns) > 0 {
				if !equalFoldSlice(info.columns, idx.Columns) {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q columns mismatch: got %v, want %v", idx.Name, idx.Table, info.columns, idx.Columns)
				}
			}

			if idx.Predicate != "" {
				if !info.partial {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q missing predicate: expected %q, but index is not partial", idx.Name, idx.Table, idx.Predicate)
				}
				predSource := info.predicate
				if predSource == "" {
					predSource = info.indexDef
				}
				if !matchPredicate(idx.Predicate, predSource) {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q predicate mismatch: got %q, want matching %q", idx.Name, idx.Table, info.predicate, idx.Predicate)
				}
			} else if info.partial {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q unexpected partial predicate: got %q, want non-partial index", idx.Name, idx.Table, info.predicate)
			}
		} else {
			if !postgresHasIndexMatching(ctx, database, idx.Table, idx.Columns, idx.Unique, idx.Predicate) {
				return fmt.Errorf("dbparity: table %q missing index on %v (unique=%v)", idx.Table, idx.Columns, idx.Unique)
			}
		}
	}

	// 4. Check immutability protections
	for _, p := range spec.Protections {
		if p.AppLevelOnly {
			continue
		}
		if p.TriggerName != "" {
			var count int
			if err := database.NewRaw(`SELECT COUNT(1) FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = ? AND tr.tgname = ? AND NOT tr.tgisinternal`, p.Table, p.TriggerName).Scan(ctx, &count); err != nil || count == 0 {
				if err != nil {
					return fmt.Errorf("dbparity: postgres check trigger %q on table %q: %w", p.TriggerName, p.Table, err)
				}
				return fmt.Errorf("dbparity: missing PostgreSQL immutability trigger %q on table %q", p.TriggerName, p.Table)
			}
		}
	}

	return nil
}

type sqliteColInfo struct {
	cid     int
	name    string
	colType string
	notnull int
	dflt    sql.NullString
	pk      int
}

func sqliteTableColumns(ctx context.Context, database *bun.DB, tableName string) (map[string]sqliteColInfo, error) {
	rows, err := database.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info('%s')", escapeIdentifier(tableName)))
	if err != nil {
		return nil, fmt.Errorf("dbparity: sqlite table_info %q: %w", tableName, err)
	}
	defer rows.Close()

	cols := make(map[string]sqliteColInfo)
	for rows.Next() {
		var info sqliteColInfo
		if err := rows.Scan(&info.cid, &info.name, &info.colType, &info.notnull, &info.dflt, &info.pk); err != nil {
			return nil, fmt.Errorf("dbparity: sqlite scan table_info %q: %w", tableName, err)
		}
		cols[strings.ToLower(info.name)] = info
	}
	return cols, nil
}

type sqliteIndexEntry struct {
	name    string
	unique  bool
	partial bool
}

func sqliteGetIndexList(ctx context.Context, database *bun.DB, tableName string) ([]sqliteIndexEntry, error) {
	rows, err := database.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list('%s')", escapeIdentifier(tableName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []sqliteIndexEntry
	for rows.Next() {
		var seq int
		var idxName string
		var isUnique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &idxName, &isUnique, &origin, &partial); err == nil {
			entries = append(entries, sqliteIndexEntry{
				name:    idxName,
				unique:  isUnique == 1,
				partial: partial == 1,
			})
		}
	}
	return entries, nil
}

func sqliteHasUniqueConstraint(ctx context.Context, database *bun.DB, tableName string, columns []string, lowerDDL string) bool {
	// Check PRAGMA index_list
	entries, err := sqliteGetIndexList(ctx, database, tableName)
	if err == nil {
		for _, entry := range entries {
			if entry.unique {
				// Check index columns
				idxCols, err := sqliteIndexColumns(ctx, database, entry.name)
				if err == nil && equalFoldSlice(idxCols, columns) {
					return true
				}
			}
		}
	}
	// Fallback to DDL
	for _, col := range columns {
		if !strings.Contains(lowerDDL, strings.ToLower(col)) {
			return false
		}
	}
	return strings.Contains(lowerDDL, "unique")
}

func sqliteIndexColumns(ctx context.Context, database *bun.DB, indexName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info('%s')", escapeIdentifier(indexName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err == nil {
			cols = append(cols, name)
		}
	}
	return cols, nil
}

func sqliteHasIndexMatching(ctx context.Context, database *bun.DB, tableName string, columns []string, unique bool, predicate string) bool {
	entries, err := sqliteGetIndexList(ctx, database, tableName)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.unique != unique {
			continue
		}
		if predicate != "" && !entry.partial {
			continue
		}
		if predicate == "" && entry.partial {
			continue
		}
		idxCols, err := sqliteIndexColumns(ctx, database, entry.name)
		if err != nil || !equalFoldSlice(idxCols, columns) {
			continue
		}
		if predicate != "" {
			var idxSQL sql.NullString
			_ = database.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, entry.name).Scan(ctx, &idxSQL)
			if !idxSQL.Valid || !matchPredicate(predicate, idxSQL.String) {
				continue
			}
		}
		return true
	}
	return false
}

type pgColInfo struct {
	columnName    string
	dataType      string
	udtName       string
	isNullable    string
	columnDefault sql.NullString
}

func postgresTableColumns(ctx context.Context, database *bun.DB, tableName string) (map[string]pgColInfo, error) {
	rows, err := database.QueryContext(ctx, `
SELECT column_name, data_type, udt_name, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ?`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query columns for table %q: %w", tableName, err)
	}
	defer rows.Close()

	cols := make(map[string]pgColInfo)
	for rows.Next() {
		var info pgColInfo
		if err := rows.Scan(&info.columnName, &info.dataType, &info.udtName, &info.isNullable, &info.columnDefault); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan column for table %q: %w", tableName, err)
		}
		cols[strings.ToLower(info.columnName)] = info
	}
	return cols, nil
}

func postgresPrimaryKeyColumns(ctx context.Context, database *bun.DB, tableName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
WHERE tc.table_schema = current_schema()
  AND tc.table_name = ?
  AND tc.constraint_type = 'PRIMARY KEY'
ORDER BY kcu.ordinal_position`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query PK for table %q: %w", tableName, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan PK column: %w", err)
		}
		cols = append(cols, strings.ToLower(col))
	}
	return cols, nil
}

func postgresConstraintDefs(ctx context.Context, database *bun.DB, tableName string, contype string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?
  AND c.contype = ?`, tableName, contype)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query constraint %q for table %q: %w", contype, tableName, err)
	}
	defer rows.Close()

	var defs []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan constraint def: %w", err)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func matchSQLiteSemanticType(expected SemanticType, rawType string) bool {
	upper := strings.ToUpper(strings.TrimSpace(rawType))
	switch expected {
	case TypeText:
		return strings.Contains(upper, "TEXT") ||
			strings.Contains(upper, "CHAR") ||
			strings.Contains(upper, "CLOB") ||
			strings.Contains(upper, "STRING")
	case TypeInteger:
		return strings.Contains(upper, "INT")
	case TypeBoolean:
		return strings.Contains(upper, "BOOL") || strings.Contains(upper, "INT")
	case TypeTimestamp:
		return strings.Contains(upper, "TIME") ||
			strings.Contains(upper, "DATE") ||
			strings.Contains(upper, "INT")
	case TypeBlob:
		return strings.Contains(upper, "BLOB") ||
			strings.Contains(upper, "BINARY") ||
			strings.Contains(upper, "BYTEA") ||
			strings.Contains(upper, "RAW")
	case TypeJSON:
		return strings.Contains(upper, "JSON") ||
			strings.Contains(upper, "TEXT") ||
			strings.Contains(upper, "CHAR")
	case TypeNumeric:
		return strings.Contains(upper, "REAL") ||
			strings.Contains(upper, "FLOA") ||
			strings.Contains(upper, "DOUB") ||
			strings.Contains(upper, "NUM") ||
			strings.Contains(upper, "DEC")
	default:
		return strings.Contains(upper, strings.ToUpper(string(expected)))
	}
}

func matchPostgresSemanticType(expected SemanticType, dataType, udtName string) bool {
	dt := strings.ToLower(strings.TrimSpace(dataType))
	udt := strings.ToLower(strings.TrimSpace(udtName))
	switch expected {
	case TypeText:
		return dt == "text" ||
			strings.Contains(dt, "char") ||
			strings.Contains(dt, "varchar") ||
			udt == "text" || udt == "varchar" || udt == "citext" || udt == "name"
	case TypeInteger:
		return strings.Contains(dt, "int") ||
			strings.Contains(dt, "serial") ||
			strings.Contains(udt, "int") ||
			strings.Contains(udt, "serial")
	case TypeBoolean:
		return strings.Contains(dt, "bool") || strings.Contains(udt, "bool")
	case TypeTimestamp:
		return strings.Contains(dt, "time") ||
			strings.Contains(dt, "date") ||
			strings.Contains(udt, "time") ||
			strings.Contains(udt, "date") ||
			strings.Contains(dt, "int")
	case TypeBlob:
		return dt == "bytea" || udt == "bytea" || strings.Contains(dt, "blob")
	case TypeJSON:
		return strings.Contains(dt, "json") ||
			strings.Contains(udt, "json") ||
			dt == "text" || udt == "text"
	case TypeNumeric:
		return strings.Contains(dt, "numeric") ||
			strings.Contains(dt, "decimal") ||
			strings.Contains(dt, "real") ||
			strings.Contains(dt, "double") ||
			strings.Contains(dt, "float") ||
			strings.Contains(udt, "numeric") ||
			strings.Contains(udt, "float")
	default:
		return dt == strings.ToLower(string(expected)) || udt == strings.ToLower(string(expected))
	}
}

func matchColumnNameOrPattern(pattern, name string) bool {
	p := strings.ToLower(pattern)
	n := strings.ToLower(name)
	if p == n {
		return true
	}
	if strings.Contains(p, "%") {
		globPattern := strings.ReplaceAll(p, "%", "*")
		matched, err := filepath.Match(globPattern, n)
		if err == nil && matched {
			return true
		}
	}
	if strings.Contains(p, "*") {
		matched, err := filepath.Match(p, n)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func equalFoldSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func escapeIdentifier(ident string) string {
	return strings.ReplaceAll(ident, "'", "''")
}

type pgIndexDetailed struct {
	name      string
	tableName string
	unique    bool
	partial   bool
	columns   []string
	predicate string
	indexDef  string
}

func postgresGetIndexDetailed(ctx context.Context, database *bun.DB, indexName string) (*pgIndexDetailed, error) {
	var info pgIndexDetailed
	row := database.QueryRowContext(ctx, `
SELECT 
    c.relname AS index_name,
    t.relname AS table_name,
    i.indisunique AS is_unique,
    i.indpred IS NOT NULL AS is_partial,
    COALESCE(pg_get_expr(i.indpred, i.indrelid), '') AS predicate,
    COALESCE(pi.indexdef, '') AS indexdef
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_indexes pi ON pi.schemaname = n.nspname AND pi.indexname = c.relname
WHERE n.nspname = current_schema()
  AND c.relname = ?`, indexName)

	if err := row.Scan(&info.name, &info.tableName, &info.unique, &info.partial, &info.predicate, &info.indexDef); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("dbparity: postgres query index %q: %w", indexName, err)
	}

	cols, err := postgresIndexColumns(ctx, database, indexName)
	if err != nil {
		return nil, err
	}
	info.columns = cols
	return &info, nil
}

func postgresIndexColumns(ctx context.Context, database *bun.DB, indexName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT a.attname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
WHERE n.nspname = current_schema()
  AND c.relname = ?
ORDER BY k.ord`, indexName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query index columns for %q: %w", indexName, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan index column: %w", err)
		}
		cols = append(cols, col)
	}
	return cols, nil
}

func postgresHasIndexMatching(ctx context.Context, database *bun.DB, tableName string, columns []string, unique bool, predicate string) bool {
	rows, err := database.QueryContext(ctx, `
SELECT c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?`, tableName)
	if err != nil {
		return false
	}
	defer rows.Close()

	var indexNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			indexNames = append(indexNames, name)
		}
	}
	for _, idxName := range indexNames {
		info, err := postgresGetIndexDetailed(ctx, database, idxName)
		if err != nil || info == nil {
			continue
		}
		if info.unique != unique {
			continue
		}
		if predicate != "" && !info.partial {
			continue
		}
		if predicate == "" && info.partial {
			continue
		}
		if !equalFoldSlice(info.columns, columns) {
			continue
		}
		if predicate != "" {
			predSource := info.predicate
			if predSource == "" {
				predSource = info.indexDef
			}
			if !matchPredicate(predicate, predSource) {
				continue
			}
		}
		return true
	}
	return false
}

var typeCastRegex = regexp.MustCompile(`::[a-zA-Z0-9_]+`)

func normalizePredicate(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if idx := strings.Index(s, "where "); idx >= 0 {
		s = s[idx+len("where "):]
	}
	s = typeCastRegex.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "!=", " <> ")
	s = strings.ReplaceAll(s, "(", " ")
	s = strings.ReplaceAll(s, ")", " ")
	s = strings.ReplaceAll(s, "<>", " __NE__ ")
	s = strings.ReplaceAll(s, "<=", " __LE__ ")
	s = strings.ReplaceAll(s, ">=", " __GE__ ")
	s = strings.ReplaceAll(s, "=", " = ")
	s = strings.ReplaceAll(s, "<", " < ")
	s = strings.ReplaceAll(s, ">", " > ")
	s = strings.ReplaceAll(s, "__NE__", " <> ")
	s = strings.ReplaceAll(s, "__LE__", " <= ")
	s = strings.ReplaceAll(s, "__GE__", " >= ")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func matchPredicate(expected, actual string) bool {
	normExpected := normalizePredicate(expected)
	normActual := normalizePredicate(actual)
	if normExpected == "" && normActual == "" {
		return true
	}
	if normExpected == "" || normActual == "" {
		return false
	}
	return normExpected == normActual
}

func extractWhereClause(sqlStr string) string {
	if idx := strings.Index(strings.ToLower(sqlStr), "where "); idx >= 0 {
		return strings.TrimSpace(sqlStr[idx+len("where "):])
	}
	return strings.TrimSpace(sqlStr)
}

func normalizeDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = typeCastRegex.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		inner = typeCastRegex.ReplaceAllString(inner, "")
		s = strings.TrimSpace(inner)
	}

	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2 {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, "''", "'")
	}
	s = strings.TrimSpace(s)

	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		s = strings.TrimSpace(inner)
	}

	if intVal, err := strconv.ParseInt(s, 10, 64); err == nil {
		s = strconv.FormatInt(intVal, 10)
	} else if floatVal, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "eE") {
		s = strconv.FormatFloat(floatVal, 'f', -1, 64)
	}

	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
