package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ProductionScanRoots are the top-level trees walked for production guardrails.
func ProductionScanRoots() []string {
	return []string{"cmd", "internal", "pkg"}
}

// FindRepoRoot walks upward from dir until go.mod is found.
func FindRepoRoot(dir string) (string, error) {
	cur := dir
	for range 16 {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "", os.ErrNotExist
}

// SlashPath normalizes a path to forward slashes.
func SlashPath(p string) string {
	return filepath.ToSlash(p)
}

// PackageDirFromRel returns the repo-relative package directory for a .go file
// (parent of the file, forward slashes).
func PackageDirFromRel(rel string) string {
	rel = SlashPath(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return ""
	}
	return dir
}

// WalkProductionGoFiles walks non-test .go files under ProductionScanRoots.
// Callback receives repo-relative slash path, absolute path, and file bytes.
func WalkProductionGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	for _, top := range ProductionScanRoots() {
		base := filepath.Join(root, top)
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
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return fn(SlashPath(rel), path, src)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ParseGoSource parses src with SkipObjectResolution.
func ParseGoSource(filename string, src []byte) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	return fset, f, nil
}

// FileImportPaths returns the import paths declared in f.
func FileImportPaths(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

// MatchPathPrefix reports whether path equals prefix or is under prefix/.
func MatchPathPrefix(path, prefix string) bool {
	path = SlashPath(path)
	prefix = SlashPath(prefix)
	if prefix == "" || prefix == "*" {
		return true
	}
	if strings.HasSuffix(prefix, "/**") {
		base := strings.TrimSuffix(prefix, "/**")
		return path == base || strings.HasPrefix(path, base+"/")
	}
	if strings.HasSuffix(prefix, "/*") {
		base := strings.TrimSuffix(prefix, "/*")
		if path == base {
			return false
		}
		rest := strings.TrimPrefix(path, base+"/")
		return strings.HasPrefix(path, base+"/") && !strings.Contains(rest, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// MatchImportPattern reports whether importPath matches a simple pattern.
// Patterns may be exact paths, suffix "/name", or contain a substring marker.
func MatchImportPattern(importPath, pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*/") {
		return strings.HasSuffix(importPath, pattern[1:]) || strings.Contains(importPath, pattern[1:])
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(importPath, strings.Trim(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(importPath, strings.TrimPrefix(pattern, "*"))
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(importPath, strings.TrimSuffix(pattern, "*"))
	}
	return importPath == pattern || strings.HasSuffix(importPath, "/"+pattern) || strings.Contains(importPath, pattern)
}

// Unexported aliases keep existing structural tests compiling against the shared scanner.
func productionScanRoots() []string { return ProductionScanRoots() }
func walkProductionGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	return WalkProductionGoFiles(root, fn)
}
func slashPath(p string) string { return SlashPath(p) }
func parseGoSource(filename string, src string) (*token.FileSet, *ast.File, error) {
	return ParseGoSource(filename, []byte(src))
}
func countFileLines(path string) (int, error)     { return CountFileLines(path) }
func countNonTestGoLines(dir string) (int, error) { return CountNonTestGoLines(dir) }
