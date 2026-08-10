package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// LegacyABIAllowlist is the exact set of additive legacy ABI feature values.
var LegacyABIAllowlist = []string{
	backendplugin.FeatureExactReasoningParts,
	backendplugin.FeatureOrderedItems,
	backendplugin.FeatureExactOpenResponsesFields,
	backendplugin.FeatureProxyOwnedSessionID,
}

var genericABIFieldTerms = map[string]bool{
	"custom": true, "extension": true, "extensions": true, "semantic": true,
	"capability": true, "capabilities": true, "session": true, "owned": true,
	"proxy": true, "reasoning": true, "parts": true, "items": true, "ordered": true,
	"exact": true,
}

// KnownProtocolIdentifiers is retained for characterization callers. Structural
// checks do not depend on a closed provider-family list.
var KnownProtocolIdentifiers = []string{"openresponses", "openai", "anthropic", "gemini", "bedrock", "codex", "acp"}

var neutralABITerms = map[string]bool{"semantic": true, "protocol": true, "feature": true, "features": true, "custom": true, "extension": true, "extensions": true, "capability": true, "capabilities": true, "session": true, "owned": true, "proxy": true, "reasoning": true, "parts": true, "items": true, "ordered": true, "exact": true, "usage": true, "metadata": true, "transport": true, "dialect": true, "requirement": true, "requirements": true, "invocation": true, "canonical": true, "wire": true, "raw": true, "json": true, "support": true}

func identifierWords(value string) []string {
	value = strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(value)
	var words []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == ' ' }) {
		start := 0
		for i := 1; i < len(part); i++ {
			if part[i] >= 'A' && part[i] <= 'Z' {
				words = append(words, strings.ToLower(part[start:i]))
				start = i
			}
		}
		if start < len(part) {
			words = append(words, strings.ToLower(part[start:]))
		}
	}
	return words
}
func providerSpecificABIIdentifier(value string) bool {
	words := identifierWords(value)
	if len(words) < 2 {
		return false
	}
	for _, w := range words[1:] {
		if neutralABITerms[w] {
			return !neutralABITerms[words[0]]
		}
	}
	return false
}

func ValidateABIFeatureSymbol(featureName string) error {
	name := strings.ToLower(strings.TrimSpace(featureName))
	if slices.Contains(LegacyABIAllowlist, name) {
		return nil
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	if len(parts) == 0 {
		return fmt.Errorf("archtest: empty backend-plugin ABI feature %q", featureName)
	}
	for _, part := range parts {
		if !genericABIFieldTerms[part] {
			return fmt.Errorf("archtest: backend-plugin ABI feature %q contains non-generic term %q", featureName, part)
		}
	}
	return nil
}

// DetectDuplicateContinuationStructs parses pkg/lipsdk/continuation and internal/core/continuation
// and returns duplicate struct definitions across both packages.
func DetectDuplicateContinuationStructs(repoRoot string) ([]string, error) {
	fset := token.NewFileSet()

	sdkDir := filepath.Join(repoRoot, "pkg", "lipsdk", "continuation")
	sdkPkgs, err := parser.ParseDir(fset, sdkDir, nil, 0)
	if err != nil {
		return nil, err
	}

	coreDir := filepath.Join(repoRoot, "internal", "core", "continuation")
	corePkgs, err := parser.ParseDir(fset, coreDir, nil, 0)
	if err != nil {
		return nil, err
	}

	findStructs := func(pkgs map[string]*ast.Package) map[string]string {
		structs := make(map[string]string)
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				for _, decl := range fileNode.Decls {
					genDecl, ok := decl.(*ast.GenDecl)
					if !ok || genDecl.Tok != token.TYPE {
						continue
					}
					for _, spec := range genDecl.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						if _, isStruct := typeSpec.Type.(*ast.StructType); isStruct {
							structs[typeSpec.Name.Name] = fname
						}
					}
				}
			}
		}
		return structs
	}

	sdkStructs := findStructs(sdkPkgs)
	coreStructs := findStructs(corePkgs)

	var duplicateStructs []string
	for structName := range sdkStructs {
		if _, exists := coreStructs[structName]; exists {
			duplicateStructs = append(duplicateStructs, structName)
		}
	}
	slices.Sort(duplicateStructs)
	return duplicateStructs, nil
}

func DetectDuplicateAuthoritativeRegistries(repoRoot string) (map[string]string, error) {
	dirs := []string{
		filepath.Join(repoRoot, "internal", "standardplugins"),
		filepath.Join(repoRoot, "internal", "pluginreg"),
		filepath.Join(repoRoot, "internal", "infra", "runtimebundle"),
		filepath.Join(repoRoot, "internal", "providerprofiles"),
	}
	fset := token.NewFileSet()
	out := make(map[string]string)
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, nil, 0)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				rel, _ := filepath.Rel(repoRoot, fname)
				rel = filepath.ToSlash(rel)
				ast.Inspect(fileNode, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.ValueSpec:
						for i, name := range x.Names {
							if ast.IsExported(name.Name) && i < len(x.Values) && isContributionIdentityExpr(name.Name, x.Type, x.Values[i]) {
								out[name.Name] = rel
							}
						}
					case *ast.FuncDecl:
						if x.Body == nil || !ast.IsExported(x.Name.Name) || !isContributionAuthorityName(x.Name.Name) {
							return true
						}
						for _, result := range functionResultTypes(x) {
							if isIdentityType(result) && functionReturnsIdentityLiteral(x) {
								out[x.Name.Name] = rel
							}
						}
					}
					return true
				})
			}
		}
	}
	return out, nil
}

func isContributionAuthorityName(name string) bool {
	lower := strings.ToLower(name)
	for _, denied := range []string{"pool", "route_registry", "scenario", "health", "affinity", "generation", "config", "state"} {
		if strings.Contains(lower, denied) {
			return false
		}
	}
	if strings.Contains(lower, "contribution") || strings.Contains(lower, "registration") || strings.Contains(lower, "routeclaim") || strings.Contains(lower, "route_claim") {
		return true
	}
	if strings.Contains(lower, "essentialbackend") || strings.Contains(lower, "compatiblebackend") || strings.Contains(lower, "providerprofile") || strings.Contains(lower, "backendfamily") {
		return true
	}
	return strings.Contains(lower, "frontend") && (strings.Contains(lower, "claim") || strings.Contains(lower, "registration"))
}

func isIdentityType(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.ArrayType:
		return x.Len == nil && isStringExpr(x.Elt)
	case *ast.MapType:
		return isStringExpr(x.Key) && (isStringExpr(x.Value) || isFuncExpr(x.Value))
	}
	return false
}

func isStringExpr(expr ast.Expr) bool { id, ok := expr.(*ast.Ident); return ok && id.Name == "string" }
func isFuncExpr(expr ast.Expr) bool   { _, ok := expr.(*ast.FuncType); return ok }

func isContributionIdentityExpr(name string, typ ast.Expr, value ast.Expr) bool {
	if !isContributionAuthorityName(name) || !isIdentityType(typ) {
		// Inferred declarations still qualify when their literal has string identity keys.
		if !isContributionAuthorityName(name) {
			return false
		}
	}
	if typ != nil && isIdentityType(typ) {
		return true
	}
	switch x := value.(type) {
	case *ast.CompositeLit:
		if _, ok := x.Type.(*ast.ArrayType); ok {
			return true
		}
		if _, ok := x.Type.(*ast.MapType); ok {
			return true
		}
	case *ast.CallExpr:
		return isContributionAuthorityName(identName(x.Fun))
	}
	return false
}

func functionResultTypes(fn *ast.FuncDecl) []ast.Expr {
	if fn.Type.Results == nil {
		return nil
	}
	var out []ast.Expr
	for _, field := range fn.Type.Results.List {
		out = append(out, field.Type)
	}
	return out
}

func functionReturnsIdentityLiteral(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for _, result := range ret.Results {
				if lit, ok := result.(*ast.CompositeLit); ok {
					if isIdentityType(lit.Type) {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

func routeKindIsProtocolOwned(name, value string) bool {
	suffix := strings.TrimPrefix(name, "RouteKind")
	neutral := map[string]bool{"Create": true, "Compact": true, "Cancel": true, "WebSocket": true, "Invoke": true, "Health": true}
	if !neutral[suffix] {
		return true
	}
	parts := strings.Split(strings.ToLower(value), "_")
	return len(parts) > 1 && parts[0] != "generic" && parts[0] != "semantic"
}

func DetectCentralProtocolRouteKinds(repoRoot string) (map[string]string, error) {
	contractDir := filepath.Join(repoRoot, "internal", "stdhttp", "contract")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, contractDir, nil, 0)
	if err != nil {
		return nil, err
	}

	discoveredRouteKinds := make(map[string]string)
	for _, pkg := range pkgs {
		for fname, fileNode := range pkg.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			ast.Inspect(fileNode, func(n ast.Node) bool {
				vSpec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range vSpec.Names {
					if strings.HasPrefix(name.Name, "RouteKind") && i < len(vSpec.Values) {
						if lit, ok := vSpec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							val := strings.ToLower(strings.Trim(lit.Value, `"`))
							if routeKindIsProtocolOwned(name.Name, val) {
								discoveredRouteKinds[name.Name] = val
							}
						}
					}
				}
				return true
			})
		}
	}
	return discoveredRouteKinds, nil
}

// DetectCentralProtocolDiagnosticsDebt scans central surfaces for protocol-specific diagnostic DTO rows, switches, or projectors.
func DetectCentralProtocolDiagnosticsDebt(repoRoot string) (map[string]string, error) {
	fset := token.NewFileSet()
	targets := []string{
		filepath.Join(repoRoot, "internal", "core", "diag"),
		filepath.Join(repoRoot, "internal", "infra", "runtimebundle"),
	}

	debtItems := make(map[string]string)

	for _, targetDir := range targets {
		pkgs, err := parser.ParseDir(fset, targetDir, nil, 0)
		if err != nil && !os.IsNotExist(err) {
			continue
		}
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				relPath, _ := filepath.Rel(repoRoot, fname)

				ast.Inspect(fileNode, func(n ast.Node) bool {
					// Check DTO struct names
					if typeSpec, ok := n.(*ast.TypeSpec); ok {
						if _, isStruct := typeSpec.Type.(*ast.StructType); isStruct {
							nameLower := strings.ToLower(typeSpec.Name.Name)
							// Only the core diagnostic package is an ownership debt
							// surface; config/default-model DTOs remain ordinary state.
							if strings.Contains(strings.ToLower(relPath), "internal/core/diag") &&
								(strings.Contains(nameLower, "row") || strings.Contains(nameLower, "diagnostic") || strings.Contains(nameLower, "diag")) {
								debtItems[typeSpec.Name.Name] = relPath
							}
						}
					}
					// Check switch case string literals
					if fn, ok := n.(*ast.FuncDecl); ok && strings.Contains(strings.ToLower(fn.Name.Name), "diagnostic") && strings.Contains(strings.ToLower(relPath), "runtimebundle") {
						debtItems[fn.Name.Name] = relPath
					}
					if switchStmt, ok := n.(*ast.SwitchStmt); ok {
						for _, stmt := range switchStmt.Body.List {
							if caseClause, ok := stmt.(*ast.CaseClause); ok {
								for _, expr := range caseClause.List {
									if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
										val := strings.Trim(lit.Value, `"`)
										if (strings.Contains(strings.ToLower(relPath), "diag") || strings.Contains(strings.ToLower(relPath), "diagnostic")) && (val == "openai-responses" || val == "openresponses" || val == "anthropic" || val == "gemini") {
											debtItems[fmt.Sprintf("switch-case:%s", val)] = relPath
										}
									}
								}
							}
						}
					}
					return true
				})
			}
		}
	}

	return debtItems, nil
}

func TestBackendPluginABI_LegacyAllowlistOnly(t *testing.T) {
	t.Parallel()

	if symbols, err := ScanPublicBackendPluginABI(filepath.Join("..", "..")); err != nil {
		t.Fatalf("scan public backend-plugin ABI: %v", err)
	} else if err := ValidatePublicBackendPluginABIMutation(symbols); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join("..", "..", "pkg", "lipsdk", "backendplugin")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse pkg/lipsdk/backendplugin: %v", err)
	}

	discoveredFeatures := make(map[string]string)
	for _, pkg := range pkgs {
		for fname, fileNode := range pkg.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			for _, decl := range fileNode.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.CONST {
					continue
				}
				for _, spec := range genDecl.Specs {
					vSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vSpec.Names {
						if strings.HasPrefix(name.Name, "Feature") && i < len(vSpec.Values) {
							if lit, ok := vSpec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								val := strings.Trim(lit.Value, `"`)
								discoveredFeatures[name.Name] = val
							}
						}
					}
				}
			}
		}
	}

	if len(discoveredFeatures) == 0 {
		t.Fatalf("discovered zero Feature* constants in pkg/lipsdk/backendplugin")
	}

	for constName, val := range discoveredFeatures {
		if err := ValidateABIFeatureSymbol(val); err != nil {
			t.Fatalf("constant %s = %q failed ABI allowlist policy: %v", constName, val, err)
		}
	}
}

func TestBackendPluginABI_ProtoFieldsScanned(t *testing.T) {
	t.Parallel()

	protoPath := filepath.Join("..", "..", "api", "backendplugin", "v1", "backend.proto")
	content, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatalf("failed to read backend.proto at %s: %v", protoPath, err)
	}
	if err := ValidateProtoSchema(string(content)); err != nil {
		t.Fatalf("backend.proto structural ABI policy failed: %v", err)
	}
}

func TestBackendPluginABI_DetectorRejectsNewProtocolNamedFeature(t *testing.T) {
	t.Parallel()

	unauthorizedFeatures := []string{
		"exact_anthropic_fields",
		"gemini_thinking_v2",
		"openai_custom_schema",
		"exact_codex_fields",
		"acp_custom_capability",
	}

	for _, f := range unauthorizedFeatures {
		if err := ValidateABIFeatureSymbol(f); err == nil {
			t.Fatalf("expected architecture guard to reject protocol-named ABI feature %q, but it passed", f)
		}
	}

	// Verify pre-v1.3 versioned classification
	if err := ValidateABIFeatureSymbol("exact_reasoning_parts"); err != nil {
		t.Fatalf("expected v1.1 exact_reasoning_parts to pass: %v", err)
	}
	if err := ValidateABIFeatureSymbol("ordered_items"); err != nil {
		t.Fatalf("expected v1.2 ordered_items to pass: %v", err)
	}
	if err := ValidateABIFeatureSymbol("exact_openresponses_fields"); err != nil {
		t.Fatalf("expected v1.3 exact_openresponses_fields legacy exception to pass: %v", err)
	}
}

func TestCoreExcludesProviderProfilesAndSDKs(t *testing.T) {
	t.Parallel()

	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{Substr: "/internal/providerprofiles", ErrMsg: "internal/core must not import providerprofiles"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "internal/core must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "internal/core must not import Anthropic SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "internal/core must not import AWS SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "internal/core must not import Gemini SDK"},
	})
}

func TestDiagnostics_CoreDiagExcludesConcretePlugins(t *testing.T) {
	t.Parallel()

	assertDepsExcludeForbidden(t, []string{"./internal/core/diag/..."}, []forbiddenDep{
		{Substr: "/internal/plugins/frontends/", ErrMsg: "internal/core/diag must not import concrete frontend plugins"},
		{Substr: "/internal/plugins/backends/", ErrMsg: "internal/core/diag must not import concrete backend plugins"},
	})
}

func TestArchGuard_DetectorMutations(t *testing.T) {
	t.Parallel()

	// 1. Proto line mutations
	cleanProto := "string custom_field = 1;"
	if err := validateProtoMutationLine(cleanProto); err != nil {
		t.Fatalf("expected clean proto line to pass: %v", err)
	}
	badProto := "string anthropic_custom_field = 2;"
	if err := validateProtoMutationLine(badProto); err == nil {
		t.Fatalf("expected bad proto line to fail ValidateProtoLine")
	}

	// 2. Synthetic AST route kind mutation
	cleanRouteSrc := `package contract
const RouteKindCreate = "create"`
	badRouteSrc := `package contract
const RouteKindBedrock = "bedrock_invoke"`

	fset := token.NewFileSet()
	fClean, _ := parser.ParseFile(fset, "clean_route.go", cleanRouteSrc, 0)
	fBad, _ := parser.ParseFile(fset, "bad_route.go", badRouteSrc, 0)

	checkRouteAST := func(f *ast.File) bool {
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if vSpec, ok := n.(*ast.ValueSpec); ok {
				for i, name := range vSpec.Names {
					if strings.HasPrefix(name.Name, "RouteKind") && i < len(vSpec.Values) {
						if lit, ok := vSpec.Values[i].(*ast.BasicLit); ok {
							val := strings.ToLower(strings.Trim(lit.Value, `"`))
							for _, proto := range KnownProtocolIdentifiers {
								if strings.Contains(val, proto) {
									found = true
								}
							}
						}
					}
				}
			}
			return true
		})
		return found
	}

	if checkRouteAST(fClean) {
		t.Fatalf("expected clean route AST to find no protocol route kinds")
	}
	if !checkRouteAST(fBad) {
		t.Fatalf("expected bad route AST to discover protocol route kind")
	}
}
