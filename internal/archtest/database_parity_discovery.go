package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var versionedMigrationRegex = regexp.MustCompile(`^\d{14}_.*\.go$`)

// DialectFinding records a discovered database dialect indicator in production source code.
type DialectFinding struct {
	File   string
	Line   int
	Kind   string
	Detail string
}

func (f DialectFinding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", f.File, f.Line, f.Kind, f.Detail)
}

// DiscoverMigrationRoots walks production trees to find directories containing versioned Bun migration files.
func DiscoverMigrationRoots(repoRoot string) (map[string][]string, error) {
	roots := make(map[string][]string)
	for _, top := range ProductionScanRoots() {
		base := filepath.Join(repoRoot, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			filename := info.Name()
			if strings.HasSuffix(filename, "_test.go") || !versionedMigrationRegex.MatchString(filename) {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(filepath.Dir(rel))
			roots[dir] = append(roots[dir], filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for dir := range roots {
		sort.Strings(roots[dir])
	}
	return roots, nil
}

// DiscoverDialectSensitivePackages scans production Go files and groups dialect-sensitive findings by package directory.
func DiscoverDialectSensitivePackages(repoRoot string) (map[string][]DialectFinding, error) {
	findingsByPkg := make(map[string][]DialectFinding)
	err := WalkProductionGoFiles(repoRoot, func(rel, abs string, src []byte) error {
		// Skip test harness and archtest packages
		if strings.HasPrefix(rel, "internal/testkit/") || strings.HasPrefix(rel, "internal/archtest/") || strings.HasPrefix(rel, "pkg/testkit/") {
			return nil
		}
		findings, err := inspectFileDialectIndicators(abs, rel, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if len(findings) > 0 {
			pkgDir := PackageDirFromRel(rel)
			findingsByPkg[pkgDir] = append(findingsByPkg[pkgDir], findings...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findingsByPkg, nil
}

var dialectSensitiveImports = map[string]string{
	"github.com/uptrace/bun/dialect/sqlitedialect": "sqlite dialect import",
	"github.com/uptrace/bun/dialect/pgdialect":     "postgres dialect import",
	"github.com/uptrace/bun/driver/pgdriver":       "postgres driver import",
	"modernc.org/sqlite":                          "sqlite driver import",
	"modernc.org/sqlite/lib":                      "sqlite driver library import",
	"github.com/uptrace/bun/migrate":              "bun migration registry import",
}

func inspectFileDialectIndicators(abs, rel string, src []byte) ([]DialectFinding, error) {
	fset, f, err := ParseGoSource(abs, src)
	if err != nil {
		return nil, err
	}
	return inspectParsedDialectIndicators(fset, f, rel), nil
}

func inspectParsedDialectIndicators(fset *token.FileSet, f *ast.File, rel string) []DialectFinding {
	var findings []DialectFinding

	// Check dialect imports
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		importPath := strings.Trim(imp.Path.Value, `"`)
		if desc, ok := dialectSensitiveImports[importPath]; ok {
			line := fset.Position(imp.Pos()).Line
			findings = append(findings, DialectFinding{
				File:   rel,
				Line:   line,
				Kind:   "import",
				Detail: fmt.Sprintf("%s (%s)", importPath, desc),
			})
		}
	}

	// Walk AST for dialect constants, selectors, error handlers, and raw SQL indicators
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		line := fset.Position(n.Pos()).Line

		switch node := n.(type) {
		case *ast.SelectorExpr:
			// e.g. dialect.SQLite, dialect.PG, db.DialectSQLite, db.DialectPostgres
			if ident, ok := node.X.(*ast.Ident); ok {
				if (ident.Name == "dialect" && (node.Sel.Name == "SQLite" || node.Sel.Name == "PG")) ||
					(ident.Name == "db" && (node.Sel.Name == "DialectSQLite" || node.Sel.Name == "DialectPostgres")) {
					findings = append(findings, DialectFinding{
						File:   rel,
						Line:   line,
						Kind:   "dialect_selector",
						Detail: fmt.Sprintf("%s.%s", ident.Name, node.Sel.Name),
					})
				}
			}

		case *ast.CallExpr:
			// Error helper calls like isSQLiteBusy, classifySQLiteUniqueConstraint, mapUniqueErrSQLite
			if ident, ok := node.Fun.(*ast.Ident); ok {
				if ident.Name == "isSQLiteBusy" || ident.Name == "classifySQLiteUniqueConstraint" || ident.Name == "mapUniqueErrSQLite" {
					findings = append(findings, DialectFinding{
						File:   rel,
						Line:   line,
						Kind:   "sqlite_error_handler",
						Detail: ident.Name + " call",
					})
				}
			}

		case *ast.BasicLit:
			if node.Kind == token.STRING {
				val := strings.ToLower(node.Value)
				for _, sqlMarker := range []struct {
					substr string
					kind   string
					label  string
				}{
					{"pragma_table_info", "sqlite_schema_metadata", "pragma_table_info query"},
					{"sqlite_master", "sqlite_schema_metadata", "sqlite_master query"},
					{"information_schema.columns", "postgres_schema_metadata", "information_schema.columns query"},
					{"information_schema.tables", "postgres_schema_metadata", "information_schema.tables query"},
					{"pg_indexes", "postgres_schema_metadata", "pg_indexes query"},
					{"pg_constraint", "postgres_schema_metadata", "pg_constraint query"},
					{"pg_get_constraintdef", "postgres_schema_metadata", "pg_get_constraintdef query"},
					{"for update", "dialect_locking_construct", "FOR UPDATE locking query"},
					{"begin immediate", "dialect_locking_construct", "BEGIN IMMEDIATE transaction"},
					{"_txlock=immediate", "dialect_locking_construct", "_txlock=immediate DSN parameter"},
				} {
					if strings.Contains(val, sqlMarker.substr) {
						findings = append(findings, DialectFinding{
							File:   rel,
							Line:   line,
							Kind:   sqlMarker.kind,
							Detail: sqlMarker.label,
						})
					}
				}
			}
		}
		return true
	})

	return findings
}

// DiscoverDeclaredInterfaceAssertions returns all asserted interface names declared as `var _ <Interface> = ...` in f.
func DiscoverDeclaredInterfaceAssertions(f *ast.File) []string {
	var assertions []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			isBlankIdent := false
			for _, name := range vs.Names {
				if name != nil && name.Name == "_" {
					isBlankIdent = true
					break
				}
			}
			if !isBlankIdent {
				continue
			}
			if vs.Type != nil {
				if name := exprToString(vs.Type); name != "" {
					assertions = append(assertions, name)
				}
			}
			for _, val := range vs.Values {
				if call, ok := val.(*ast.CallExpr); ok {
					if name := exprToString(call.Fun); name != "" {
						assertions = append(assertions, name)
					}
				}
			}
		}
	}
	return assertions
}

func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		xStr := exprToString(e.X)
		if xStr != "" {
			return xStr + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.ParenExpr:
		return exprToString(e.X)
	default:
		return ""
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
