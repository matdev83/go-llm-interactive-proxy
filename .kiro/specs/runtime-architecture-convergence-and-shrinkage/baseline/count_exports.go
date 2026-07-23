//go:build ignore

// count_exports counts exported symbols the same way scripts/arch-report.go does
// (AST scan of non-test .go files for exported type/value/func declarations).
//
// Usage (from any directory; pass absolute or repo-relative package dirs):
//
//	go run count_exports.go <pkgDir> [<pkgDir>...]
//
// Prints one JSON object: {"<pkgDir>": <count>, ...} with deterministic key order
// matching the argument order.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: go run count_exports.go <pkgDir> [<pkgDir>...]\n")
		os.Exit(2)
	}
	out := make(map[string]int, len(os.Args)-1)
	order := make([]string, 0, len(os.Args)-1)
	for _, dir := range os.Args[1:] {
		n, err := exportedSymbols(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "count_exports: %s: %v\n", dir, err)
			os.Exit(1)
		}
		out[dir] = n
		order = append(order, dir)
	}
	// Preserve argument order in JSON by emitting manually.
	fmt.Print("{")
	for i, k := range order {
		if i > 0 {
			fmt.Print(",")
		}
		kb, _ := json.Marshal(k)
		fmt.Printf("%s:%d", kb, out[k])
	}
	fmt.Println("}")
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
