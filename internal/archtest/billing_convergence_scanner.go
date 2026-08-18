package archtest

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

func countBytesLines(src []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	return n
}

// isBillingFinalConvergenceExcluded reports whether rel/src is excluded from
// the production count. Always excludes tests, testdata, vendor, .worktrees,
// node_modules, generated files, and architecture-test/tooling package sources.
func isBillingFinalConvergenceExcluded(rel string, src []byte, excludedGlobs []string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, "_test_helpers.go") {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "testdata", "vendor", ".worktrees", "node_modules", "archtest":
			return true
		}
	}
	if isBillingFinalConvergenceGenerated(src) {
		return true
	}
	for _, pattern := range excludedGlobs {
		if billingFinalConvergenceMatchGlob(pattern, rel) {
			return true
		}
	}
	return false
}

func isBillingFinalConvergenceGenerated(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Code generated") && strings.Contains(t, "DO NOT EDIT") {
			return true
		}
		if strings.HasPrefix(t, "package ") {
			break
		}
	}
	return false
}

func billingFinalConvergenceMatchGlob(pattern, path string) bool {
	pSegs := billingFinalConvergenceTrimEmpty(strings.Split(filepath.ToSlash(pattern), "/"))
	fSegs := strings.Split(filepath.ToSlash(path), "/")
	return billingFinalConvergenceMatchSegments(pSegs, fSegs)
}

func billingFinalConvergenceTrimEmpty(segs []string) []string {
	for len(segs) > 0 && segs[0] == "" {
		segs = segs[1:]
	}
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	return segs
}

func billingFinalConvergenceMatchSegments(pSegs, fSegs []string) bool {
	for len(pSegs) > 0 {
		if pSegs[0] == "**" {
			if len(pSegs) == 1 {
				return true
			}
			rest := pSegs[1:]
			for i := 0; i <= len(fSegs); i++ {
				if billingFinalConvergenceMatchSegments(rest, fSegs[i:]) {
					return true
				}
			}
			return false
		}
		if len(fSegs) == 0 {
			return false
		}
		ok, err := filepath.Match(pSegs[0], fSegs[0])
		if err != nil || !ok {
			return false
		}
		pSegs = pSegs[1:]
		fSegs = fSegs[1:]
	}
	return len(fSegs) == 0
}

func CountBillingFinalConvergenceRootLines(root string, entry BillingFinalConvergenceRoot, excludedGlobs []string) (int, error) {
	fs := &workingTreeFS{root: root}
	return CountBillingFinalConvergenceRootLinesFS(fs, entry, excludedGlobs)
}

func CountBillingFinalConvergenceRootLinesFS(fs archtestFS, entry BillingFinalConvergenceRoot, excludedGlobs []string) (int, error) {
	var total int
	err := fs.WalkRootFiles(entry.Path, func(rel string, src []byte) error {
		if isBillingFinalConvergenceExcluded(rel, src, excludedGlobs) {
			return nil
		}
		total += countBytesLines(src)
		return nil
	})
	return total, err
}

func CountBillingFinalConvergenceFileLines(root, rel string) (int, error) {
	fs := &workingTreeFS{root: root}
	return CountBillingFinalConvergenceFileLinesFS(fs, rel)
}

func CountBillingFinalConvergenceFileLinesFS(fs archtestFS, rel string) (int, error) {
	src, err := fs.ReadFile(rel)
	if err != nil {
		return 0, err
	}
	return countBytesLines(src), nil
}

type billingFinalConvergenceDeclInfo struct {
	file       string
	pkgName    string // Store pkgPath
	kind       string
	names      []string
	startLine  int
	endLine    int
	references map[string]struct{}
}

func resolvePackageName(fs archtestFS, importPath string) string {
	if !strings.HasPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/") {
		parts := strings.Split(importPath, "/")
		return parts[len(parts)-1]
	}
	relDir := strings.TrimPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/")
	entries, err := fs.ReadDir(relDir)
	if err != nil {
		parts := strings.Split(importPath, "/")
		return parts[len(parts)-1]
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		content, err := fs.ReadFile(relDir + "/" + entry.Name())
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "package ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					return parts[1]
				}
			}
		}
	}
	parts := strings.Split(importPath, "/")
	return parts[len(parts)-1]
}

func billingFinalConvergenceFileDecls(fs archtestFS, fset *token.FileSet, f *ast.File, rel string) []billingFinalConvergenceDeclInfo {
	var out []billingFinalConvergenceDeclInfo
	pkgPath := filepath.ToSlash(filepath.Dir(rel))

	imports := make(map[string]string)
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		var relDir string
		if strings.HasPrefix(path, "github.com/matdev83/go-llm-interactive-proxy/") {
			relDir = strings.TrimPrefix(path, "github.com/matdev83/go-llm-interactive-proxy/")
		} else {
			parts := strings.Split(path, "/")
			relDir = parts[len(parts)-1]
		}

		var alias string
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			alias = resolvePackageName(fs, path)
		}
		imports[alias] = relDir
	}

	for _, decl := range f.Decls {
		info, ok := billingFinalConvergenceDeclInfoFrom(fs, fset, decl, rel, pkgPath, imports)
		if !ok {
			continue
		}
		out = append(out, info)
	}
	return out
}

func billingFinalConvergenceDeclInfoFrom(fs archtestFS, fset *token.FileSet, decl ast.Decl, rel string, pkgPath string, imports map[string]string) (billingFinalConvergenceDeclInfo, bool) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Name == nil {
			return billingFinalConvergenceDeclInfo{}, false
		}
		kind := "func"
		if d.Recv != nil {
			kind = "method"
		}
		return billingFinalConvergenceDeclInfo{
			file: rel, pkgName: pkgPath, kind: kind, names: []string{d.Name.Name},
			startLine: fset.Position(d.Pos()).Line, endLine: fset.Position(d.End()).Line,
			references: collectQualifiedRefs(pkgPath, imports, decl),
		}, true
	case *ast.GenDecl:
		var names []string
		switch d.Tok {
		case token.TYPE:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil {
					names = append(names, ts.Name.Name)
				}
			}
		case token.VAR, token.CONST:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range vs.Names {
						if n != nil {
							names = append(names, n.Name)
						}
					}
				}
			}
		default:
			return billingFinalConvergenceDeclInfo{}, false
		}
		if len(names) == 0 {
			return billingFinalConvergenceDeclInfo{}, false
		}
		kind := "var"
		switch d.Tok {
		case token.TYPE:
			kind = "type"
		case token.CONST:
			kind = "const"
		}
		return billingFinalConvergenceDeclInfo{
			file: rel, pkgName: pkgPath, kind: kind, names: names,
			startLine: fset.Position(d.Pos()).Line, endLine: fset.Position(d.End()).Line,
			references: collectQualifiedRefs(pkgPath, imports, decl),
		}, true
	}
	return billingFinalConvergenceDeclInfo{}, false
}

func collectQualifiedRefs(pkgPath string, imports map[string]string, node ast.Node) map[string]struct{} {
	out := make(map[string]struct{})
	if node == nil {
		return out
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.SelectorExpr:
			if xIdent, ok := expr.X.(*ast.Ident); ok {
				if relDir, ok := imports[xIdent.Name]; ok {
					out[relDir+"."+expr.Sel.Name] = struct{}{}
				} else {
					out[xIdent.Name+"."+expr.Sel.Name] = struct{}{}
				}
				return false
			}
		case *ast.Ident:
			out[pkgPath+"."+expr.Name] = struct{}{}
		}
		return true
	})
	return out
}

func billingFinalConvergenceIsUnder(rel, rootPath string) bool {
	rel = filepath.ToSlash(rel)
	rootPath = filepath.ToSlash(rootPath)
	return rel == rootPath || strings.HasPrefix(rel, rootPath+"/")
}

func ComputeBillingFinalConvergenceSymbolInventory(root string, doc BillingFinalConvergenceBaselineFile) ([]BillingFinalConvergenceDeclaration, error) {
	fs := &workingTreeFS{root: root}
	return ComputeBillingFinalConvergenceSymbolInventoryFS(fs, doc)
}

func ComputeBillingFinalConvergenceSymbolInventoryFS(fs archtestFS, doc BillingFinalConvergenceBaselineFile) ([]BillingFinalConvergenceDeclaration, error) {
	initialSeeds := make(map[string]bool)
	for _, s := range doc.SeedSymbols {
		initialSeeds[s] = true
	}

	economicDirs := make(map[string]bool)
	for _, r := range doc.IncludedRoots {
		economicDirs[filepath.ToSlash(r.Path)] = true
	}
	for _, f := range doc.IncludedFiles {
		economicDirs[filepath.ToSlash(filepath.Dir(f.Path))] = true
	}
	// The design explicitly includes money-specific UsageAuthority code outside
	// the whole-file billing roots through the fixed-point inventory. Keep this
	// allowlist narrow and path-qualified so common identifiers in unrelated
	// packages cannot seed the economic surface.
	for _, dir := range []string{
		"internal/core/usageauthority",
		"internal/infra/usageauthority",
	} {
		economicDirs[dir] = true
	}

	seeds := make(map[string]struct{})
	rootPaths := make([]string, 0, len(doc.IncludedRoots))
	for _, r := range doc.IncludedRoots {
		rootPaths = append(rootPaths, r.Path)
	}
	fileSet := make(map[string]struct{}, len(doc.IncludedFiles))
	for _, f := range doc.IncludedFiles {
		fileSet[filepath.ToSlash(f.Path)] = struct{}{}
	}

	type parsedFile struct {
		rel  string
		decl []billingFinalConvergenceDeclInfo
	}
	var files []parsedFile

	err := fs.WalkProductionGoFiles(func(rel string, src []byte) error {
		if isBillingFinalConvergenceExcluded(rel, src, doc.ExcludedGlobs) {
			return nil
		}
		if _, ok := fileSet[rel]; ok {
			return nil
		}
		for _, rp := range rootPaths {
			if billingFinalConvergenceIsUnder(rel, rp) {
				return nil
			}
		}
		fset, f, perr := ParseGoSource(rel, src)
		if perr != nil {
			return fmt.Errorf("%s: %w", rel, perr)
		}
		files = append(files, parsedFile{rel: rel, decl: billingFinalConvergenceFileDecls(fs, fset, f, rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}

	included := make(map[string]billingFinalConvergenceDeclInfo)
	causes := make(map[string]string)
	for {
		changed := false
		for _, pf := range files {
			for _, di := range pf.decl {
				key := pf.rel + "\x00" + di.names[0]
				if _, seen := included[key]; seen {
					continue
				}
				matched := billingFinalConvergenceFirstMatch(di, seeds, initialSeeds, economicDirs)
				if matched == "" {
					continue
				}
				included[key] = di
				causes[key] = matched
				for _, n := range di.names {
					if isGenericSymbol(n) {
						continue
					}
					seeds[di.pkgName+"."+n] = struct{}{}
				}
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	out := make([]BillingFinalConvergenceDeclaration, 0, len(included))
	for key, di := range included {
		out = append(out, BillingFinalConvergenceDeclaration{
			File:          di.file,
			Name:          di.names[0],
			Kind:          di.kind,
			StartLine:     di.startLine,
			EndLine:       di.endLine,
			Loc:           di.endLine - di.startLine + 1,
			DeclaredNames: di.names,
			Cause:         "symbol-followed:v1",
			CausedBy:      causes[key],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].StartLine < out[j].StartLine
	})
	return out, nil
}

func billingFinalConvergenceFirstMatch(di billingFinalConvergenceDeclInfo, seeds map[string]struct{}, initialSeeds map[string]bool, economicDirs map[string]bool) string {
	var best string
	consider := func(qname string) {
		parts := strings.Split(qname, ".")
		var pkg, name string
		if len(parts) > 1 {
			pkg = parts[0]
			name = parts[1]
		} else {
			name = qname
		}

		if isGenericSymbol(name) {
			return
		}

		if _, ok := seeds[qname]; ok {
			if best == "" || qname < best {
				best = qname
			}
			return
		}

		if initialSeeds[name] && billingFinalConvergenceEconomicPath(pkg, economicDirs) {
			if best == "" || qname < best {
				best = qname
			}
			return
		}
	}
	for _, n := range di.names {
		consider(di.pkgName + "." + n)
	}
	for r := range di.references {
		consider(r)
	}
	return best
}

func billingFinalConvergenceEconomicPath(pkg string, roots map[string]bool) bool {
	for root := range roots {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	return false
}

func isGenericSymbol(name string) bool {
	if len(name) <= 2 {
		return true
	}
	switch name {
	// Built-in types
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	// Built-in functions/constants
	case "append", "cap", "close", "complex", "copy", "delete", "false", "imag",
		"iota", "len", "make", "new", "nil", "panic", "print", "println", "real", "recover", "true":
		return true
	// Common generic names
	case "Config", "Name", "Type", "Status", "State", "Ctx", "Context", "Data", "Value", "Err",
		"args", "req", "res", "key", "val", "object", "client", "server", "runner", "task":
		return true
	}
	return false
}
