package archtest

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 15.1 certification: the minimal public billing binding in
// pkg/lipsdk/billing exposes only typed ports/DTOs built from public
// packages. It must not import internal/SQL/driver/concrete-provider
// packages, declare a generic provider-shaped normalizer or service-map/any
// payload, or smuggle raw bodies and executors through the contract.

// TestBillingBindingImportClosureIsPublicAndNeutral proves the binding package
// (including its transitive closure) stays on public contracts.
func TestBillingBindingImportClosureIsPublicAndNeutral(t *testing.T) {
	t.Parallel()
	out, err := cachedGoList(t, "-deps", "-test=false", "-f", "{{.ImportPath}}",
		"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/billing")
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		imp := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(imp, "github.com/matdev83/go-llm-interactive-proxy/internal/") {
			t.Fatalf("pkg/lipsdk/billing must not import internal packages: %s", imp)
		}
		for _, forbid := range []string{
			"database/sql",
			"github.com/uptrace/bun",
			"/internal/plugins/",
			"/connectors/",
			"/connector-support/",
			"github.com/openai/",
			"github.com/anthropics/",
			"github.com/aws/",
			"google.golang.org/genai",
		} {
			if strings.Contains(imp, forbid) {
				t.Fatalf("pkg/lipsdk/billing must not depend on %q: %s", forbid, imp)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan go list output: %v", err)
	}
}

// TestBillingBindingContractStaysTypedAndNeutral scans the binding sources for
// the forbidden contract shapes: provider-shaped normalizer ports,
// service-map/any payloads, generic maps in the typed API, and raw
// body/executor members. Local validation maps inside function bodies are
// unaffected; only exported API surface (struct fields and interface methods)
// is checked.
func TestBillingBindingContractStaysTypedAndNeutral(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "lipsdk", "billing")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok.String() != "type" {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				typeName := ts.Name.Name
				found[typeName] = true
				if strings.Contains(typeName, "Normaliz") || strings.Contains(typeName, "ServiceMap") {
					t.Fatalf("%s declares forbidden generic normalizer/service-map port %q", name, typeName)
				}
				switch typ := ts.Type.(type) {
				case *ast.StructType:
					for _, field := range typ.Fields.List {
						for _, ident := range field.Names {
							for _, forbid := range []string{"Body", "RawBody", "RawRequest", "ServiceMap", "Executor"} {
								if ident.Name == forbid {
									t.Fatalf("%s: %s must not carry raw/executor member %q", name, typeName, forbid)
								}
							}
						}
						assertTypedAPIType(t, name, typeName, field.Type)
					}
				case *ast.InterfaceType:
					for _, method := range typ.Methods.List {
						fn, ok := method.Type.(*ast.FuncType)
						if !ok {
							continue
						}
						for _, param := range fn.Params.List {
							assertTypedAPIType(t, name, typeName, param.Type)
						}
						if fn.Results != nil {
							for _, result := range fn.Results.List {
								assertTypedAPIType(t, name, typeName, result.Type)
							}
						}
					}
				}
			}
		}
	}
	// Lock the approved C7 port map: identity/version, cheap screen,
	// quote/admit, terminal handoff/ack, and explicit lifecycle ownership.
	for _, required := range []string{
		"Binding", "CreditScreener", "CreditScreenInput", "CreditScreenResult",
		"ExposureAdmitter", "ExposureAdmissionInput", "ExposureHandle",
		"TerminalSink", "TerminalEnvelope", "TerminalAck",
		"Lifecycle", "OwnedResource", "BorrowedRef",
	} {
		if !found[required] {
			t.Fatalf("pkg/lipsdk/billing must declare %s (C7 port map)", required)
		}
	}
}

// assertTypedAPIType rejects opaque any/empty-interface and generic map types
// on the exported API surface. Typed slices, pointers, and named public types
// (including func callbacks on OwnedResource) remain allowed.
func assertTypedAPIType(t *testing.T, file, owner string, expr ast.Expr) {
	t.Helper()
	switch typ := expr.(type) {
	case *ast.Ident:
		if typ.Name == "any" {
			t.Fatalf("%s: %s must not use any in its typed API", file, owner)
		}
	case *ast.InterfaceType:
		if len(typ.Methods.List) == 0 {
			t.Fatalf("%s: %s must not use empty interface{} in its typed API", file, owner)
		}
	case *ast.MapType:
		t.Fatalf("%s: %s must not use a generic map in its typed API", file, owner)
	case *ast.ArrayType:
		assertTypedAPIType(t, file, owner, typ.Elt)
	case *ast.StarExpr:
		assertTypedAPIType(t, file, owner, typ.X)
	}
}
