//go:build red

package archtest

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- AST Inspection Helpers for Generator RED Tests ---

type hookGenExpectedField struct {
	Name string
	Type string
}

func hookGenFindStructType(f *ast.File, name string) *ast.StructType {
	if f == nil {
		return nil
	}
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == name {
				if st, ok := typeSpec.Type.(*ast.StructType); ok {
					return st
				}
			}
		}
	}
	return nil
}

func hookGenFindFuncDecl(f *ast.File, name string) *ast.FuncDecl {
	if f == nil {
		return nil
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func hookGenExprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if err := format.Node(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

func hookGenAssertStructFields(t *testing.T, st *ast.StructType, expected []hookGenExpectedField) {
	require.NotNil(t, st, "HookConfig struct must not be nil")
	require.NotNil(t, st.Fields, "HookConfig struct fields must not be nil")
	require.Equal(t, len(expected), len(st.Fields.List),
		"HookConfig field count mismatch: expected %d fields, got %d", len(expected), len(st.Fields.List))

	seenNames := make(map[string]bool, len(st.Fields.List))
	actualFields := make(map[string]string, len(st.Fields.List))

	for _, field := range st.Fields.List {
		require.Len(t, field.Names, 1, "each struct field must declare exactly one name")
		name := field.Names[0].Name
		assert.False(t, seenNames[name], "duplicate field name %s in HookConfig struct", name)
		seenNames[name] = true
		actualFields[name] = hookGenExprToString(field.Type)
	}

	for _, exp := range expected {
		actualType, exists := actualFields[exp.Name]
		assert.True(t, exists, "expected field %s missing from HookConfig struct", exp.Name)
		assert.Equal(t, exp.Type, actualType, "type mismatch for field %s in HookConfig", exp.Name)
	}
}

func hookGenAssertProjectHookConfigSignature(t *testing.T, fn *ast.FuncDecl) {
	require.NotNil(t, fn, "generated code must declare ProjectHookConfig function")
	require.Nil(t, fn.Recv, "ProjectHookConfig must be a package-level function (no receiver)")
	require.NotNil(t, fn.Type, "ProjectHookConfig must have a function signature type")

	require.NotNil(t, fn.Type.Params, "ProjectHookConfig parameters must not be nil")
	require.Len(t, fn.Type.Params.List, 2, "ProjectHookConfig must accept exactly 2 parameter fields")

	require.Len(t, fn.Type.Params.List[0].Names, 1, "first param field must declare exactly one parameter name")
	assert.Equal(t, "frozen", fn.Type.Params.List[0].Names[0].Name, "first parameter name must be 'frozen'")
	assert.Equal(t, "FrozenPlaneSet", hookGenExprToString(fn.Type.Params.List[0].Type), "first parameter type must be 'FrozenPlaneSet'")

	require.Len(t, fn.Type.Params.List[1].Names, 1, "second param field must declare exactly one parameter name")
	assert.Equal(t, "policy", fn.Type.Params.List[1].Names[0].Name, "second parameter name must be 'policy'")
	assert.Equal(t, "hooks.ToolReactorErrorPolicy", hookGenExprToString(fn.Type.Params.List[1].Type), "second parameter type must be 'hooks.ToolReactorErrorPolicy'")

	require.NotNil(t, fn.Type.Results, "ProjectHookConfig return results must not be nil")
	require.Len(t, fn.Type.Results.List, 1, "ProjectHookConfig must return exactly 1 result field")
	assert.Equal(t, "HookConfig", hookGenExprToString(fn.Type.Results.List[0].Type), "return type must be 'HookConfig'")
}

func hookGenExtractReturnHookConfigLit(t *testing.T, fn *ast.FuncDecl) *ast.CompositeLit {
	require.NotNil(t, fn, "function declaration must not be nil")
	require.NotNil(t, fn.Body, "function body must not be nil")
	require.Len(t, fn.Body.List, 1, "function body must contain exactly 1 statement")

	retStmt, ok := fn.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok, "single statement in body must be a return statement, got %T", fn.Body.List[0])
	require.Len(t, retStmt.Results, 1, "return statement must return exactly 1 result")

	compLit, ok := retStmt.Results[0].(*ast.CompositeLit)
	require.True(t, ok, "returned result must be a composite literal, got %T", retStmt.Results[0])

	typeIdent, ok := compLit.Type.(*ast.Ident)
	require.True(t, ok, "returned composite literal type must be an identifier, got %T", compLit.Type)
	assert.Equal(t, "HookConfig", typeIdent.Name, "returned composite literal type must be HookConfig")

	return compLit
}

func hookGenAssertHookConfigLiteral(t *testing.T, comp *ast.CompositeLit, expectedHookPlanes map[string]string) {
	require.NotNil(t, comp, "composite literal must not be nil")
	typeIdent, ok := comp.Type.(*ast.Ident)
	require.True(t, ok, "composite literal type must be an identifier, got %T", comp.Type)
	assert.Equal(t, "HookConfig", typeIdent.Name, "composite literal type must be HookConfig")

	expectedTotalKeys := len(expectedHookPlanes) + 1
	require.Equal(t, expectedTotalKeys, len(comp.Elts),
		"HookConfig literal must contain exactly all-and-only expected keys (%d expected, got %d)",
		expectedTotalKeys, len(comp.Elts))

	seenKeys := make(map[string]bool, len(comp.Elts))

	for _, elt := range comp.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		require.True(t, ok, "composite literal element must be KeyValueExpr, got %T", elt)

		keyIdent, ok := kv.Key.(*ast.Ident)
		require.True(t, ok, "key in composite literal must be an identifier, got %T", kv.Key)
		keyName := keyIdent.Name

		assert.False(t, seenKeys[keyName], "duplicate key %s in HookConfig literal", keyName)
		seenKeys[keyName] = true

		if keyName == "ToolReactorErrorPolicy" {
			valIdent, ok := kv.Value.(*ast.Ident)
			require.True(t, ok, "ToolReactorErrorPolicy value expression must be an AST identifier node, got %T", kv.Value)
			assert.Equal(t, "policy", valIdent.Name, "ToolReactorErrorPolicy value must be exactly the identifier 'policy'")
			continue
		}

		expectedPlane, isExpected := expectedHookPlanes[keyName]
		require.True(t, isExpected, "unexpected key %s in HookConfig literal (not in expected hook targets)", keyName)

		call, ok := kv.Value.(*ast.CallExpr)
		require.True(t, ok, "field %s value expression must be a CallExpr (*ast.CallExpr), got %T", keyName, kv.Value)

		fnIdent, ok := call.Fun.(*ast.Ident)
		require.True(t, ok, "called function for %s must be an identifier (*ast.Ident), got %T", keyName, call.Fun)
		assert.Equal(t, "Get", fnIdent.Name, "called function for %s must be exactly 'Get'", keyName)

		require.Len(t, call.Args, 2, "call to Get for %s must have exactly 2 arguments (frozen, %s)", keyName, expectedPlane)

		arg0Ident, ok := call.Args[0].(*ast.Ident)
		require.True(t, ok, "first argument to Get for %s must be an identifier node 'frozen', got %T", keyName, call.Args[0])
		assert.Equal(t, "frozen", arg0Ident.Name, "first argument to Get for %s must be exactly 'frozen'", keyName)

		arg1Ident, ok := call.Args[1].(*ast.Ident)
		require.True(t, ok, "second argument to Get for %s must be an identifier node '%s', got %T", keyName, expectedPlane, call.Args[1])
		assert.Equal(t, expectedPlane, arg1Ident.Name, "second argument to Get for %s must be exactly '%s'", keyName, expectedPlane)
	}

	for expKey := range expectedHookPlanes {
		assert.True(t, seenKeys[expKey], "expected key %s was not present in HookConfig literal", expKey)
	}
	assert.True(t, seenKeys["ToolReactorErrorPolicy"], "expected ToolReactorErrorPolicy was not present in HookConfig literal")
}

// TestPlaneGenerator_HookViewMetadata_DeterministicEmission_RED characterizes Requirement 3.3, 3.6 (Task 1.3):
// Valid hook-view declaration metadata emits deterministic HookConfig struct and ProjectHookConfig function
// in the generated plane code without requiring manual hook projection.
// On baseline before Task 3.1 & 3.2, plane generator does not parse HookTarget metadata or emit HookConfig.
func TestPlaneGenerator_HookViewMetadata_DeterministicEmission_RED(t *testing.T) {
	t.Parallel()

	validHookManifest := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "SubmitHooks",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var PlaneRequestPartHooks = Plane[[]hooks.RequestPartHook]{
	ID: "request_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "RequestPartHooks",
	Combine: func(s SourceKind, c, in []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) { return append(c, in...), nil },
}
var PlaneResponsePartHooks = Plane[[]hooks.ResponsePartHook]{
	ID: "response_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "ResponsePartHooks",
	Combine: func(s SourceKind, c, in []hooks.ResponsePartHook) ([]hooks.ResponsePartHook, error) { return append(c, in...), nil },
}
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{
	ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "ToolReactors",
	Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmitHooks, PlaneRequestPartHooks, PlaneResponsePartHooks, PlaneToolReactors}
`

	t.Run("EmitsHookConfigAndProjectHookConfig", func(t *testing.T) {
		t.Parallel()
		codeBytes, err := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err, "GenerateFeaturePlanesCode should succeed for valid hook metadata")

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err, "generated code must be parseable Go source")

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st, "generated code must declare HookConfig struct")
		expectedFields := []hookGenExpectedField{
			{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
			{Name: "RequestPartHooks", Type: "[]hooks.RequestPartHook"},
			{Name: "ResponsePartHooks", Type: "[]hooks.ResponsePartHook"},
			{Name: "ToolReactors", Type: "[]hooks.ToolReactor"},
			{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
		}
		hookGenAssertStructFields(t, st, expectedFields)

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn)

		comp := hookGenExtractReturnHookConfigLit(t, fn)
		hookGenAssertHookConfigLiteral(t, comp, map[string]string{
			"SubmitHooks":       "PlaneSubmitHooks",
			"RequestPartHooks":  "PlaneRequestPartHooks",
			"ResponsePartHooks": "PlaneResponsePartHooks",
			"ToolReactors":      "PlaneToolReactors",
		})
	})

	t.Run("DeterministicEmission", func(t *testing.T) {
		t.Parallel()
		pass1, err1 := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err1)
		pass2, err2 := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err2)
		assert.True(t, bytes.Equal(pass1, pass2), "repeated generation must produce byte-for-byte deterministic output")
	})
}

// TestPlaneGenerator_HookViewMetadata_SubsetManifest_RED characterizes Requirement 3.3, 5.1 (Task 1.3):
// When only a subset of hook planes is annotated with HookTarget, the generator emits all and only
// the annotated hook targets plus ToolReactorErrorPolicy, proving output is not hardcoded to four planes.
func TestPlaneGenerator_HookViewMetadata_SubsetManifest_RED(t *testing.T) {
	t.Parallel()

	subsetManifest := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "SubmitHooks",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{
	ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "ToolReactors",
	Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmitHooks, PlaneToolReactors}
`

	codeBytes, err := GenerateFeaturePlanesCode([]byte(subsetManifest))
	require.NoError(t, err, "GenerateFeaturePlanesCode should succeed for subset hook metadata")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
	require.NoError(t, err, "generated code must be parseable Go source")

	st := hookGenFindStructType(f, "HookConfig")
	require.NotNil(t, st, "generated code must declare HookConfig struct for subset manifest")
	expectedSubsetFields := []hookGenExpectedField{
		{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
		{Name: "ToolReactors", Type: "[]hooks.ToolReactor"},
		{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
	}
	hookGenAssertStructFields(t, st, expectedSubsetFields)

	fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
	hookGenAssertProjectHookConfigSignature(t, fn)

	comp := hookGenExtractReturnHookConfigLit(t, fn)
	hookGenAssertHookConfigLiteral(t, comp, map[string]string{
		"SubmitHooks":  "PlaneSubmitHooks",
		"ToolReactors": "PlaneToolReactors",
	})
}

// TestPlaneGenerator_HookViewMetadata_ValidationRejections_RED characterizes Requirement 3.3, 5.1, 5.2 (Task 1.3):
// Plane generator must reject duplicate hook targets, unknown hook targets, and incompatible value types against
// the closed HookTarget table.
func TestPlaneGenerator_HookViewMetadata_ValidationRejections_RED(t *testing.T) {
	t.Parallel()

	t.Run("DuplicateHookTarget_Rejected", func(t *testing.T) {
		t.Parallel()
		dupManifest := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneSubmit1 = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks_1", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "SubmitHooks",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var PlaneSubmit2 = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks_2", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "SubmitHooks",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmit1, PlaneSubmit2}
`
		_, err := GenerateFeaturePlanesCode([]byte(dupManifest))
		require.Error(t, err, "duplicate HookTarget SubmitHooks must be rejected by generator")
		assert.Contains(t, err.Error(), "SubmitHooks", "error must identify the duplicate hook target")
	})

	t.Run("UnknownHookTarget_Rejected", func(t *testing.T) {
		t.Parallel()
		unknownManifest := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"

var PlaneUnknown = Plane[[]hooks.SubmitHook]{
	ID: "unknown_hook_target", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "UnknownTargetTypo",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneUnknown}
`
		_, err := GenerateFeaturePlanesCode([]byte(unknownManifest))
		require.Error(t, err, "unknown HookTarget UnknownTargetTypo must be rejected by generator")
		assert.Contains(t, err.Error(), "UnknownTargetTypo", "error must identify the unknown hook target")
	})

	t.Run("IncompatibleHookType_Rejected", func(t *testing.T) {
		t.Parallel()
		incompatibleManifest := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"

var PlaneIncompatible = Plane[[]session.Opener]{
	ID: "incompatible_opener_as_submit_hook", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, HookTarget: "SubmitHooks",
	Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneIncompatible}
`
		_, err := GenerateFeaturePlanesCode([]byte(incompatibleManifest))
		require.Error(t, err, "incompatible plane type []session.Opener with HookTarget SubmitHooks must be rejected")
		assert.Contains(t, err.Error(), "SubmitHooks", "error must identify the incompatible hook target")
	})
}
