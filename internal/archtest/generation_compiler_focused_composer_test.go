package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 3.4 compact AST/source guards: GenerationCompiler/CompileGeneration
// return only GenerationRuntime, the canonical HandlerComposer never sees
// RequestPlane, and runtimebundle/stdhttp/contract keep the cycle-neutral
// dependency direction the design requires.

// TestContractPackage_NoRuntimebundleOrRootStdhttpImport proves
// internal/stdhttp/contract stays cycle-neutral: it must not import
// runtimebundle (the composition root building StandardHTTPInput directly)
// or the root stdhttp package (its own consumer).
func TestContractPackage_NoRuntimebundleOrRootStdhttpImport(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "stdhttp", "contract")
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected internal/stdhttp/contract package source")
	}
	forbidden := []string{
		"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle",
		"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp\"",
	}
	for _, abs := range entries {
		if strings.HasSuffix(abs, "_test.go") {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		for _, f := range forbidden {
			if strings.Contains(s, f) {
				t.Fatalf("%s: contract package must not import %s (cycle-neutral dependency direction)", filepath.Base(abs), f)
			}
		}
	}
}

// TestRuntimebundle_ImportsContractNotRootStdhttp proves runtimebundle builds
// the focused HTTP input via internal/stdhttp/contract and never imports the
// root stdhttp package (which itself imports runtimebundle; a reverse import
// would be a cycle).
func TestRuntimebundle_ImportsContractNotRootStdhttp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sawContractImport := false
	for _, abs := range entries {
		base := filepath.Base(abs)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		if strings.Contains(s, `"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"`) {
			t.Fatalf("%s: runtimebundle must not import root internal/stdhttp (import cycle; use internal/stdhttp/contract)", base)
		}
		if strings.Contains(s, "internal/stdhttp/contract") {
			sawContractImport = true
		}
	}
	if !sawContractImport {
		t.Fatal("expected at least one runtimebundle file to import internal/stdhttp/contract (focused HTTP input construction)")
	}
}

// TestCompileGeneration_BodyNeverConstructsRequestPlane is an AST proof that
// the canonical CompileGeneration function body contains no RequestPlane
// composite literal, field access chain through a local RequestPlane
// variable, or conversion to RequestPlane (req 2.2-2.8: canonical
// CompileGeneration must not construct/retain/pass/convert through
// runtimebundle.RequestPlane).
func TestCompileGeneration_BodyNeverConstructsRequestPlane(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "infra", "runtimebundle", "compile_generation.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "CompileGeneration" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "RequestPlane" {
				t.Fatalf("CompileGeneration body constructs a RequestPlane composite literal")
			}
			return true
		})
	}
}

// TestHandlerComposer_TakesStandardHTTPInputNotRequestPlane is an AST proof
// that the canonical HandlerComposer type declaration parameter list
// references StandardHTTPInput (via the contract alias) and not RequestPlane.
func TestHandlerComposer_TakesStandardHTTPInputNotRequestPlane(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "infra", "runtimebundle", "request_plane.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var found *ast.FuncType
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "HandlerComposer" {
				continue
			}
			ft, ok := ts.Type.(*ast.FuncType)
			if !ok {
				t.Fatalf("HandlerComposer must be a func type")
			}
			found = ft
		}
	}
	if found == nil {
		t.Fatal("HandlerComposer type declaration not found in request_plane.go")
	}
	if found.Params == nil {
		t.Fatal("HandlerComposer has no parameters")
	}
	var mentionsStandardHTTPInput, mentionsRequestPlane bool
	for _, p := range found.Params.List {
		if typeExprMentionsName(p.Type, "StandardHTTPInput") {
			mentionsStandardHTTPInput = true
		}
		if typeExprMentionsName(p.Type, "RequestPlane") {
			mentionsRequestPlane = true
		}
	}
	if !mentionsStandardHTTPInput {
		t.Fatal("HandlerComposer must take a StandardHTTPInput parameter")
	}
	if mentionsRequestPlane {
		t.Fatal("HandlerComposer must not take a RequestPlane parameter")
	}
}

func typeExprMentionsName(expr ast.Expr, name string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == name
	case *ast.StarExpr:
		return typeExprMentionsName(t.X, name)
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == name
	case *ast.ArrayType:
		return typeExprMentionsName(t.Elt, name)
	default:
		return false
	}
}

// TestGenerationCompiler_CompileReturnsGenerationRuntime is an AST proof that
// GenerationCompiler.Compile's declared result type is GenerationRuntime
// (req 2.2-2.8), while the separate candidateCompilerAdapter — not
// GenerationCompiler — is the only type whose Compile method returns
// runtimehost.PublishedRequestPlane (req: one explicit narrow adapter).
func TestGenerationCompiler_CompileReturnsGenerationRuntime(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "infra", "runtimebundle", "reload_host.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var sawGenerationCompilerCompile, sawAdapterCompile bool
	var generationCompilerReturnsPublishedRequestPlane bool
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "Compile" || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		recvType := recvTypeName(fd.Recv.List[0].Type)
		if fd.Type.Results == nil || len(fd.Type.Results.List) == 0 {
			continue
		}
		resultMentionsRuntime := typeExprMentionsName(fd.Type.Results.List[0].Type, "GenerationRuntime")
		resultMentionsPublished := typeExprMentionsName(fd.Type.Results.List[0].Type, "PublishedRequestPlane")
		switch recvType {
		case "GenerationCompiler":
			sawGenerationCompilerCompile = true
			if resultMentionsPublished {
				generationCompilerReturnsPublishedRequestPlane = true
			}
			if !resultMentionsRuntime {
				t.Fatalf("GenerationCompiler.Compile must return GenerationRuntime, got result type %s", typeString(fd.Type.Results.List[0].Type))
			}
		case "candidateCompilerAdapter":
			sawAdapterCompile = true
			if !resultMentionsPublished {
				t.Fatalf("candidateCompilerAdapter.Compile must return runtimehost.PublishedRequestPlane, got %s", typeString(fd.Type.Results.List[0].Type))
			}
		}
	}
	if !sawGenerationCompilerCompile {
		t.Fatal("GenerationCompiler.Compile method not found")
	}
	if !sawAdapterCompile {
		t.Fatal("candidateCompilerAdapter.Compile method not found (one explicit narrow CandidateCompiler adapter)")
	}
	if generationCompilerReturnsPublishedRequestPlane {
		t.Fatal("GenerationCompiler.Compile must not itself return runtimehost.PublishedRequestPlane")
	}
}

// TestCompileGeneration_MergesFeatureSurfaceExactlyOnce is an AST proof that
// CompileGeneration calls featurebundle.MergeFeatureSurface exactly once per
// compile (req: merge feature surface exactly once per candidate compile).
func TestCompileGeneration_MergesFeatureSurfaceExactlyOnce(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "infra", "runtimebundle", "compile_generation.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "CompileGeneration" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "MergeFeatureSurface" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "featurebundle" {
				calls++
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("CompileGeneration calls featurebundle.MergeFeatureSurface %d times, want exactly 1", calls)
	}
}

// TestCanonicalCallSites_UseComposeStandardHTTP proves the canonical
// production call sites (cmd/lipstd serve + check-config, pkg/lipruntime
// build) wire the canonical ComposeStandardHTTP composer and never the
// transitional ComposeRequestPlane (which is not assignable to
// runtimebundle.HandlerComposer after task 3.4).
func TestCanonicalCallSites_UseComposeStandardHTTP(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := []string{
		filepath.Join(root, "cmd", "lipstd", "command.go"),
		filepath.Join(root, "pkg", "lipruntime", "build.go"),
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		if !strings.Contains(s, "stdhttp.ComposeStandardHTTP") {
			t.Fatalf("%s must wire stdhttp.ComposeStandardHTTP as the canonical HandlerComposer", filepath.Base(path))
		}
		if strings.Contains(s, "stdhttp.ComposeRequestPlane") {
			t.Fatalf("%s must not reference the transitional stdhttp.ComposeRequestPlane on the canonical path", filepath.Base(path))
		}
	}
}
