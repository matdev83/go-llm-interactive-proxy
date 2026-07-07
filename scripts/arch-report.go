package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const hexagonalBaselineRelPath = "testdata/architecture/hexagonal_migration_baseline.json"

var hotspotFiles = []string{
	"internal/core/runtime/executor.go",
	"internal/infra/runtimebundle/build.go",
	"internal/infra/runtimebundle/options.go",
	"internal/stdhttp/server.go",
	"internal/pluginreg/standard_table.go",
	"internal/pluginreg/reg.go",
}

type pkgMeta struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
}

type baselineFile struct {
	Packages []baselineEntry `json:"packages"`
}

type baselineEntry struct {
	GoListPattern     string           `json:"go_list_pattern"`
	Classification    string           `json:"classification"`
	RetirementTrigger string           `json:"retirement_trigger"`
	Backlog           *baselineBacklog `json:"backlog,omitempty"`
}

type baselineBacklog struct {
	NextExtraction string `json:"next_extraction"`
}

func main() {
	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	modPath, err := modulePath(root)
	if err != nil {
		fatal(err)
	}
	pkgs, err := packageImportGraph(root)
	if err != nil {
		fatal(err)
	}

	var b strings.Builder
	fmt.Fprintln(&b, "# Architecture report")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Advisory only; produced by `make arch-report`.")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Module: `%s`\n\n", modPath)

	writePackageLineReport(&b, pkgs)
	writeHotspotReport(&b, root)
	writeFanOutReport(&b, pkgs, modPath)
	writeFanInReport(&b, pkgs, modPath)
	writeExportedSymbolsReport(&b, root)
	writeBaselineClassifications(&b, root)

	fmt.Print(b.String())
}

func writePackageLineReport(b *strings.Builder, pkgs []pkgMeta) {
	fmt.Fprintln(b, "## Non-test lines by package (top 25)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | Lines |")
	fmt.Fprintln(b, "| --- | --- |")

	type row struct {
		path  string
		lines int
	}
	rows := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		n := 0
		for _, f := range p.GoFiles {
			lines, err := countFileLines(filepath.Join(p.Dir, f))
			if err != nil {
				continue
			}
			n += lines
		}
		rows = append(rows, row{path: p.ImportPath, lines: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].lines != rows[j].lines {
			return rows[i].lines > rows[j].lines
		}
		return rows[i].path < rows[j].path
	})
	writeLimitRows(b, rows, 25, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.lines)
	})
}

func writeHotspotReport(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Hotspot files (critical-file budgets)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| File | Lines |")
	fmt.Fprintln(b, "| --- | --- |")
	for _, rel := range hotspotFiles {
		n, err := countFileLines(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintf(b, "| `%s` | (missing) |\n", rel)
			continue
		}
		fmt.Fprintf(b, "| `%s` | %d |\n", rel, n)
	}
	fmt.Fprintln(b)
}

func writeFanOutReport(b *strings.Builder, pkgs []pkgMeta, modPath string) {
	fmt.Fprintln(b, "## Direct internal import fan-out (top 20)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | DirectInternalImports |")
	fmt.Fprintln(b, "| --- | --- |")

	internalPrefix := modPath + "/internal/"
	type row struct {
		path string
		n    int
	}
	rows := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		if !strings.HasPrefix(p.ImportPath, internalPrefix) {
			continue
		}
		n := 0
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				n++
			}
		}
		rows = append(rows, row{path: p.ImportPath, n: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].path < rows[j].path
	})
	writeLimitRows(b, rows, 20, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.n)
	})
}

func writeFanInReport(b *strings.Builder, pkgs []pkgMeta, modPath string) {
	fmt.Fprintln(b, "## Direct internal import fan-in (top 20)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | ImportedBy |")
	fmt.Fprintln(b, "| --- | --- |")

	internalPrefix := modPath + "/internal/"
	importers := map[string]int{}
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				importers[imp]++
			}
		}
	}
	type row struct {
		path string
		n    int
	}
	rows := make([]row, 0, len(importers))
	for p, n := range importers {
		rows = append(rows, row{path: p, n: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].path < rows[j].path
	})
	writeLimitRows(b, rows, 20, func(r row) string {
		return fmt.Sprintf("| `%s` | %d |", r.path, r.n)
	})
}

func writeExportedSymbolsReport(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Exported symbols (public contracts)")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Package | ExportedSymbols |")
	fmt.Fprintln(b, "| --- | --- |")
	for _, rel := range []string{"pkg/lipapi", "pkg/lipsdk"} {
		n, err := exportedSymbols(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintf(b, "| `%s` | (error: %v) |\n", rel, err)
			continue
		}
		fmt.Fprintf(b, "| `%s` | %d |\n", rel, n)
	}
	fmt.Fprintln(b)
}

func writeBaselineClassifications(b *strings.Builder, root string) {
	fmt.Fprintln(b, "## Hexagonal baseline classifications")
	fmt.Fprintln(b)

	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		fmt.Fprintf(b, "(could not read baseline: %v)\n\n", err)
		return
	}
	var doc baselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(b, "(could not decode baseline: %v)\n\n", err)
		return
	}

	fmt.Fprintln(b, "| Package | Class | Retirement target / next extraction |")
	fmt.Fprintln(b, "| --- | --- | --- |")
	for _, row := range doc.Packages {
		notes := row.RetirementTrigger
		if row.Backlog != nil {
			notes = row.Backlog.NextExtraction
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n",
			strings.TrimPrefix(row.GoListPattern, "./"), row.Classification, notes)
	}
	fmt.Fprintln(b)
}

func writeLimitRows[T any](b *strings.Builder, rows []T, limit int, format func(T) string) {
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, r := range rows[:limit] {
		fmt.Fprintln(b, format(r))
	}
	fmt.Fprintln(b)
}

func exportedSymbols(pkgDir string) (int, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return 0, err
	}
	fset := token.NewFileSet()
	var count int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return 0, err
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name != nil && s.Name.IsExported() {
							count++
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() {
								count++
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.IsExported() {
					count++
				}
			}
		}
	}
	return count, nil
}

func countFileLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var n int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

func modulePath(root string) (string, error) {
	cmd := exec.Command("go", "list", "-m")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func packageImportGraph(root string) ([]pkgMeta, error) {
	cmd := exec.Command("go", "list", "-json", "-test=false", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []pkgMeta
	for dec.More() {
		var p pkgMeta
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for range 12 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find go.mod above %s", wd)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "arch-report: %v\n", err)
	os.Exit(1)
}
