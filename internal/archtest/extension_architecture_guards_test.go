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
	backendplugin.FeatureAccountingEvidence,
	backendplugin.FeatureSemanticExtensions,
}

var genericABIFieldTerms = map[string]bool{
	"custom": true, "extension": true, "extensions": true, "semantic": true,
	"capability": true, "capabilities": true, "session": true, "owned": true,
	"proxy": true, "reasoning": true, "parts": true, "items": true, "ordered": true,
	"exact":      true,
	"accounting": true,
	"evidence":   true,
}

// KnownProtocolIdentifiers is retained for characterization callers. Structural
// checks do not depend on a closed provider-family list.
var KnownProtocolIdentifiers = []string{"openresponses", "openai", "anthropic", "gemini", "bedrock", "codex", "acp"}

var neutralABITerms = map[string]bool{
	"semantic": true, "protocol": true, "feature": true, "features": true,
	"custom": true, "extension": true, "extensions": true, "capability": true,
	"capabilities": true, "session": true, "owned": true, "proxy": true,
	"reasoning": true, "parts": true, "items": true, "ordered": true, "exact": true,
	"usage": true, "metadata": true, "transport": true, "dialect": true,
	"requirement": true, "requirements": true, "invocation": true, "canonical": true,
	"wire": true, "json": true, "support": true, "runtime": true, "policy": true,
	"close": true, "instance": true, "response": true, "request": true,
	"resolved": true, "execute": true, "list": true, "models": true, "model": true,
	"tool": true, "def": true, "disable": true, "parameters": true, "prompt": true,
	"cache": true, "key": true, "message": true, "messages": true, "credential": true,
	"mode": true, "access": true, "scope": true, "process": true, "sharing": true,
	"role": true, "part": true, "kind": true, "event": true, "terminal": true,
	"status": true, "cancel": true, "client": true, "server": true, "frame": true,
	"error": true, "code": true, "reason": true, "name": true, "id": true,
	"minor": true, "major": true, "plugin": true, "host": true, "version": true,
	"build": true, "description": true, "prefixes": true, "dynamic": true,
	"static": true, "structured": true, "outputs": true, "parallel": true,
	"video": true, "annotations": true, "media": true, "keepalive": true,
	"bidirectional": true, "config": true, "yaml": true, "secrets": true,
	"timeout": true, "allowed": true, "env": true, "retries": true, "evidence": true,
	"source": true, "after": true, "fetch": true, "refresh": true, "unix": true,
	"quality": true, "optional": true, "generation": true, "route": true,
	"prefix": true, "fields": true, "field": true, "supports": true, "supported": true,
	"output": true, "assistant": true, "health": true, "graceful": true,
	"shutdown": true, "describe": true, "negotiate": true, "configure": true,
	"resolve": true, "count": true, "finalize": true, "profile": true, "state": true,
	"data": true, "input": true, "result": true, "factory": true, "descriptor": true,
	"schema": true, "payload": true, "carrier": true, "content": true, "summary": true,
	"unknown": true, "raw": true, "value": true, "default": true, "max": true,
	"local": true, "only": true, "none": true, "unspecified": true, "deadline": true,
	"start": true, "oauth": true, "user": true, "workload": true, "diagnostic": true,
	"provider": true, "transient": true, "aborted": true, "cancelled": true,
	"internal": true, "invalid": true, "argument": true, "not": true, "found": true,
	"permission": true, "denied": true, "resource": true, "exhausted": true,
	"unauthenticated": true, "unavailable": true, "file": true, "ref": true,
	"image": true, "delta": true, "signature": true, "warning": true, "started": true,
	"finished": true, "text": true, "call": true, "outcome": true, "accepted": true,
	"violation": true, "annotation": true, "refusal": true, "failure": true,
	"success": true, "developer": true, "system": true,
	"meta": true, "delivery": true, "operation": true, "param": true,
	"per": true, "shared": true, "artifact": true, "index": true, "phase": true,
	"refs": true, "y": true, "a": true, "m": true, "l": true, "j": true, "s": true,
	"o": true, "n": true, "i": true, "acknowledged": true, "detail": true,
	// Package and language vocabulary is neutral even when it is not a wire term.
	"grpc": true, "backend": true, "lip": true, "sdk": true, "any": true,
	"apply": true, "billing": true, "configured": true,
	"err": true, "forward": true,
	"g": true, "has": true, "must": true, "negotiation": true, "new": true, "open": true,
	"require": true, "restore": true, "secret": true, "service": true,
	"stream": true, "token": true, "validate": true,
}

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
		if neutralABITerms[w] && (w == "fields" || w == "schema" || w == "dialect") {
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
