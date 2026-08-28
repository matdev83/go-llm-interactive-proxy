package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateRequestMaterializerExpr tests the AST validation for RequestMaterializer expressions.
func TestValidateRequestMaterializerExpr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		exprSrc   string
		wantError bool
	}{
		{name: "FuncLit is accepted", exprSrc: `func(v []string) []string { return v }`},
		{name: "Ident is accepted", exprSrc: `materializeFunc`},
		{name: "SelectorExpr is accepted", exprSrc: `request.MaterializeAttemptsSorted`},
		{name: "ParenExpr wrapping SelectorExpr is accepted", exprSrc: `(request.MaterializeAttemptsSorted)`},
		{name: "ParenExpr wrapping FuncLit is accepted", exprSrc: `((func(v []string) []string { return v }))`},
		{name: "BasicLit string is rejected", exprSrc: `"invalid_string"`, wantError: true},
		{name: "CallExpr is rejected", exprSrc: `createMaterializer()`, wantError: true},
		{name: "BinaryExpr is rejected", exprSrc: `1 + 2`, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsedExpr, err := parser.ParseExpr(tt.exprSrc)
			require.NoError(t, err, "ParseExpr failed on %s", tt.exprSrc)

			err = validateRequestMaterializerExpr(parsedExpr, "PlaneTest")
			if tt.wantError {
				assert.Error(t, err, "expected error for expr %s", tt.exprSrc)
			} else {
				assert.NoError(t, err, "expected no error for expr %s", tt.exprSrc)
			}
		})
	}
}

// TestPlaneGenerator_RequestBorrowValidation tests generator validation of RequestBorrow constraint.
func TestPlaneGenerator_RequestBorrowValidation(t *testing.T) {
	t.Parallel()

	makeManifest := func(typeParam, mult, rules string, hasMat, reqBorrow bool) string {
		matField := ""
		if hasMat {
			matField = "\n\tRequestMaterializer: func(v " + typeParam + ") " + typeParam + " { return v },"
		}
		borrowField := ""
		if reqBorrow {
			borrowField = "\n\tRequestBorrow: true,"
		}
		return `package feature
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneTest = Plane[` + typeParam + `]{
	ID: "test_policies", Multiplicity: ` + mult + `, Rules: SourceRules{Feature: ` + rules + `},
	NilPolicy: NilReject,
	Identity: func(v ` + typeParam + `) (string, bool) { return "id", true },
	Validate: func(v ` + typeParam + `) error { return nil },
	Combine: func(s SourceKind, c, in ` + typeParam + `) (` + typeParam + `, error) { return in, nil },` + matField + borrowField + `
}
var StandardPlanes = []any{PlaneTest}
`
	}

	_, err := GenerateFeaturePlanesCode([]byte(makeManifest("[]toolpolicy.Policy", "MultOrdered", "CombConcatenate", true, true)))
	require.NoError(t, err, "valid slice plane with RequestBorrow and RequestMaterializer should succeed")

	// Non-slice plane with RequestBorrow: true must fail
	_, err = GenerateFeaturePlanesCode([]byte(makeManifest("toolpolicy.Policy", "MultExclusive", "CombExclusive", true, true)))
	require.Error(t, err, "non-slice plane with RequestBorrow: true must fail")
	assert.Contains(t, err.Error(), "RequestBorrow cannot be used on non-slice plane type")

	// Slice plane with RequestBorrow: true but no RequestMaterializer must fail
	_, err = GenerateFeaturePlanesCode([]byte(makeManifest("[]toolpolicy.Policy", "MultOrdered", "CombConcatenate", false, true)))
	require.Error(t, err, "slice plane with RequestBorrow: true but no RequestMaterializer must fail")
	assert.Contains(t, err.Error(), "RequestBorrow requires non-nil RequestMaterializer")
}

// TestPlaneGenerator_FreezeRequestMaterializerWiring verifies that slice planes with RequestMaterializer
// emit freezeRequest using materializeRequestSlice rather than calling RequestMaterializer directly.
func TestPlaneGenerator_FreezeRequestMaterializerWiring(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import (
	"context"
	"fmt"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneSyntheticSlice = Plane[[]toolpolicy.Policy]{
	ID: "synthetic_slice", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	RequestMaterializer: func(v []toolpolicy.Policy) []toolpolicy.Policy { return v },
}
var StandardPlanes = []any{PlaneSyntheticSlice}
`
	generatedBytes, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.NoError(t, err)
	generatedCode := string(generatedBytes)

	assert.Contains(t, generatedCode, "syntheticSlice: materializeRequestSlice(gf.syntheticSlice, PlaneSyntheticSlice.RequestMaterializer),")
	assert.NotContains(t, generatedCode, "cloneSlice(PlaneSyntheticSlice.RequestMaterializer(gf.syntheticSlice))")
	assert.NotContains(t, generatedCode, "syntheticSlice: PlaneSyntheticSlice.RequestMaterializer(gf.syntheticSlice)")
}

// TestPlaneGenerator_DisposableDiagnosticPlaneProof verifies that a synthetic manifest
// with one disposable diagnostic plane automatically generates projection logic in ProjectDiagnostics
// without any code in internal/core/diag/inventory_extensions.go knowing about it.
func TestPlaneGenerator_DisposableDiagnosticPlaneProof(t *testing.T) {
	t.Parallel()

	syntheticManifest := `package feature
import (
	"context"
	"fmt"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneDisposableProbe = Plane[[]toolpolicy.Policy]{
	ID: "disposable_probe", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, CoalesceGroup: "probe_group", Order: 85,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return []DiagnosticOccupant{{Label: "probe_label"}} },
		Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}} },
	},
}
var PlaneNonDiag = Plane[[]toolpolicy.Policy]{
	ID: "non_diag_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneDisposableProbe, PlaneNonDiag}
`
	generatedBytes, err := GenerateFeaturePlanesCode([]byte(syntheticManifest))
	require.NoError(t, err)
	code := string(generatedBytes)

	assert.Contains(t, code, "func ProjectDiagnostics(")
	assert.Contains(t, code, "PlaneDisposableProbe.MaterializeOccupants(")
	assert.Contains(t, code, "PlaneDisposableProbe.ProjectPrivileges(")
	assert.Contains(t, code, "PlaneDisposableProbe.Diagnostics.Order")
	assert.Contains(t, code, "PlaneDisposableProbe.Diagnostics.CoalesceGroup")
	assert.NotContains(t, code, "PlaneNonDiag.MaterializeOccupants(")

	root := repoRoot(t)
	diagPath := filepath.Join(root, "internal", "core", "diag", "inventory_extensions.go")
	prodSrc, err := os.ReadFile(diagPath)
	require.NoError(t, err)
	assert.NotContains(t, string(prodSrc), "disposable_probe", "production reducer must never reference disposable probe plane")
}

// TestPlaneGenerator_DiagnosticsValidationErrors verifies cross-plane and privilege AST validation.
func TestPlaneGenerator_DiagnosticsValidationErrors(t *testing.T) {
	t.Parallel()

	diagManifest := func(diagBody string) string {
		return `package feature
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagBody + `},
}
var StandardPlanes = []any{PlaneA}
`
	}

	twoPlaneManifest := func(diagA, diagB string) string {
		return `package feature
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagA + `},
}
var PlaneB = Plane[[]toolpolicy.Policy]{
	ID: "plane_b", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagB + `},
}
var StandardPlanes = []any{PlaneA, PlaneB}
`
	}

	tests := []struct {
		name        string
		manifest    string
		wantErrPart string
	}{
		{
			name:        "duplicate positive order without coalesce group fails",
			manifest:    twoPlaneManifest("StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }", "StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }"),
			wantErrPart: "duplicate diagnostic order 10",
		},
		{
			name:        "same coalesce group with mismatching StageID fails",
			manifest:    twoPlaneManifest(`StageID: StageIDToolEventReaction, Order: 10, CoalesceGroup: "shared_group", Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }`, `StageID: StageIDPreRequest, Order: 20, CoalesceGroup: "shared_group", Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }`),
			wantErrPart: "mismatching stage IDs for coalesce group",
		},
		{
			name:        "unknown privilege flag string is rejected",
			manifest:    diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{"invalid_privilege_typo"}} }`),
			wantErrPart: `plane PlaneA: unknown privilege flag "invalid_privilege_typo"`,
		},
		{
			name:        "unknown privilege flag identifier is rejected",
			manifest:    diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{PrivilegeTypo}} }`),
			wantErrPart: `plane PlaneA: unknown privilege flag identifier "PrivilegeTypo"`,
		},
		{
			name:        "foreign selector privilege is rejected",
			manifest:    diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{foreign.PrivilegeRawCapture}} }`),
			wantErrPart: `plane PlaneA: privilege selector expression "foreign.PrivilegeRawCapture" not allowed; must use bare identifier or string literal`,
		},
		{
			name:        "foo selector privilege is rejected",
			manifest:    diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{foo.PrivilegeAuxiliaryRequests}} }`),
			wantErrPart: `plane PlaneA: privilege selector expression "foo.PrivilegeAuxiliaryRequests" not allowed; must use bare identifier or string literal`,
		},
		{
			name:        "order provided without StageID is rejected",
			manifest:    diagManifest(`Order: 10,`),
			wantErrPart: "diagnostics StageID must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(tt.manifest))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)
		})
	}
}

// TestPlaneGenerator_PrivilegesFunctionValidation verifies the strict control-flow AST validation
// for Privileges function definitions against evasion techniques and dynamic bypasses.
func TestPlaneGenerator_PrivilegesFunctionValidation(t *testing.T) {
	t.Parallel()

	privManifest := func(privFuncBody string) string {
		return `package feature
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, Order: 10,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil },
		Privileges: ` + privFuncBody + `,
	},
}
var StandardPlanes = []any{PlaneA}
`
	}

	tests := []struct {
		name        string
		body        string
		wantErrPart string
	}{
		{
			name: "1. Valid current if+two static returns accepted",
			body: `func(v []toolpolicy.Policy) PrivilegeProjection { if len(v) > 0 { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
		},
		{
			name:        "2. Assignment bypass rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { p := PrivilegeProjection{}; p.Flags = []string{PrivilegeRawCapture}; return p }`,
			wantErrPart: "plane PlaneA: unsupported statement type *ast.AssignStmt",
		},
		{
			name:        "3. Foreign selector assignment rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { p := PrivilegeProjection{}; p.Flags = []string{foreign.PrivilegeRawCapture}; return p }`,
			wantErrPart: "plane PlaneA: unsupported statement type *ast.AssignStmt",
		},
		{
			name:        "4. Dead static projection plus dynamic return rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { _ = PrivilegeProjection{Flags: []string{PrivilegeRawCapture}}; return helper(v) }`,
			wantErrPart: "plane PlaneA: unsupported statement type *ast.AssignStmt",
		},
		{
			name:        "5. Direct dynamic return rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return helper(v) }`,
			wantErrPart: "plane PlaneA: unsupported return expression (*ast.CallExpr)",
		},
		{
			name:        "6. Identifier return rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return p }`,
			wantErrPart: "plane PlaneA: unsupported return expression (*ast.Ident)",
		},
		{
			name:        "7. Foreign projection type rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return foreign.PrivilegeProjection{Flags: []string{"raw_capture"}} }`,
			wantErrPart: `plane PlaneA: foreign projection type "foreign.PrivilegeProjection" not allowed; must use local PrivilegeProjection`,
		},
		{
			name:        "8. Unsupported loop statement rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { for i := 0; i < len(v); i++ { return PrivilegeProjection{} }; return PrivilegeProjection{} }`,
			wantErrPart: "plane PlaneA: unsupported statement type *ast.ForStmt",
		},
		{
			name:        "9. Unsupported switch statement rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { switch len(v) { case 0: return PrivilegeProjection{}; default: return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} } }`,
			wantErrPart: "plane PlaneA: unsupported statement type *ast.SwitchStmt",
		},
		{
			name: "10. Direct static canonical literal/identifier returns accepted",
			body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{"raw_capture", "auxiliary_requests"}} }`,
		},
		{
			name:        "11. Condition helper call rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if helper(v) { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: call to helper function "helper" is not allowed in privilege condition`,
		},
		{
			name:        "12. Condition foreign selector call rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if foreign.Mutate(v) { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: selector/method call "foreign.Mutate" is not allowed in privilege condition`,
		},
		{
			name:        "13. Condition len with helper argument rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if len(helper(v)) > 0 { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: len argument in privilege condition must be a bare parameter identifier, got *ast.CallExpr`,
		},
		{
			name:        "14. Condition method call rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if v.Check() { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: selector/method call "v.Check" is not allowed in privilege condition`,
		},
		{
			name:        "15. If statement with init statement rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if x := 1; len(v) > x { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: unsupported statement type *ast.AssignStmt`,
		},
		{
			name:        "16. Condition nested function call rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { if func() bool { return true }() { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
			wantErrPart: `plane PlaneA: dynamic function literal call in privilege condition is not allowed`,
		},
		{
			name: "17. Valid parenthesized condition with boolean literal accepted",
			body: `func(v []toolpolicy.Policy) PrivilegeProjection { if (len(v) > 0) && true { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`,
		},
		{
			name:        "18. Untyped elided Flags literal rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: {"raw_capture"}} }`,
			wantErrPart: `plane PlaneA: Flags must use explicit []string literal`,
		},
		{
			name:        "19. Array Flags type rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: [1]string{"raw_capture"}} }`,
			wantErrPart: `plane PlaneA: Flags must be a slice of string ([]string), got *ast.ArrayType`,
		},
		{
			name:        "20. Foreign Flags element type rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []foreign.String{"raw_capture"}} }`,
			wantErrPart: `plane PlaneA: Flags element type must be string, got *ast.SelectorExpr`,
		},
		{
			name:        "21. Non-string Flags element type rejected",
			body:        `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []int{1}} }`,
			wantErrPart: `plane PlaneA: Flags element type must be string, got *ast.Ident`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(privManifest(tt.body)))
			if tt.wantErrPart != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrPart)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidatePrivilegeCondition tests the AST grammar validation for privilege if conditions.
func TestValidatePrivilegeCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condSrc   string
		wantError bool
		errSubstr string
	}{
		{name: "bare true boolean literal is accepted", condSrc: `true`},
		{name: "bare false boolean literal is accepted", condSrc: `false`},
		{name: "unary not boolean is accepted", condSrc: `!false`},
		{name: "standard len comparison is accepted", condSrc: `len(v) > 0`},
		{name: "parenthesized len comparison is accepted", condSrc: `(len(v) >= 1)`},
		{name: "parenthesized len argument is accepted", condSrc: `len((v)) == 0`},
		{name: "binary and with comparisons is accepted", condSrc: `len(v) > 0 && len(v) <= 10`},
		{name: "binary or with comparisons is accepted", condSrc: `len(v) == 0 || len(v) != 5`},
		{name: "complex boolean expression with parens and bool literals", condSrc: `(len(v) > 0) && (true || !false)`},
		{name: "arbitrary identifier in boolean position is rejected", condSrc: `isValid`, wantError: true, errSubstr: `unsupported condition identifier "isValid"`},
		{name: "direct len call without comparison is rejected", condSrc: `len(v)`, wantError: true, errSubstr: `len call cannot be used as boolean condition directly`},
		{name: "helper function call is rejected", condSrc: `helper(v)`, wantError: true, errSubstr: `call to helper function "helper" is not allowed`},
		{name: "selector call is rejected", condSrc: `foreign.Mutate(v)`, wantError: true, errSubstr: `selector/method call "foreign.Mutate" is not allowed`},
		{name: "method call is rejected", condSrc: `v.Check()`, wantError: true, errSubstr: `selector/method call "v.Check" is not allowed`},
		{name: "len with call argument is rejected", condSrc: `len(helper(v)) > 0`, wantError: true, errSubstr: `len argument in privilege condition must be a bare parameter identifier`},
		{name: "len with multiple arguments is rejected", condSrc: `len(v, x) > 0`, wantError: true, errSubstr: `len call in privilege condition must have exactly 1 argument`},
		{name: "len with selector argument is rejected", condSrc: `len(foreign.V) > 0`, wantError: true, errSubstr: `len argument in privilege condition must be a bare parameter identifier`},
		{name: "arithmetic in scalar operand is rejected", condSrc: `len(v) + 1 > 0`, wantError: true, errSubstr: `arithmetic/binary expression`},
		{name: "slice indexing is rejected", condSrc: `v[0] == 0`, wantError: true, errSubstr: `index or slice expression is not allowed`},
		{name: "non-integer literal in comparison is rejected", condSrc: `len(v) > "zero"`, wantError: true, errSubstr: `unsupported literal "zero"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsedExpr, err := parser.ParseExpr(tt.condSrc)
			require.NoError(t, err, "ParseExpr failed on %s", tt.condSrc)

			err = validatePrivilegeCondition("TestPlane", parsedExpr)
			if tt.wantError {
				assert.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidatePrivilegeReturnStmt_DirectAST validates direct AST properties of return statements.
func TestValidatePrivilegeReturnStmt_DirectAST(t *testing.T) {
	t.Parallel()

	t.Run("untyped composite literal Flags rejected directly", func(t *testing.T) {
		t.Parallel()
		retStmt := &ast.ReturnStmt{
			Results: []ast.Expr{
				&ast.CompositeLit{
					Type: &ast.Ident{Name: "PrivilegeProjection"},
					Elts: []ast.Expr{
						&ast.KeyValueExpr{
							Key: &ast.Ident{Name: "Flags"},
							Value: &ast.CompositeLit{
								Type: nil,
								Elts: []ast.Expr{
									&ast.BasicLit{Kind: token.STRING, Value: `"raw_capture"`},
								},
							},
						},
					},
				},
			},
		}

		err := validatePrivilegeReturnStmt("TestPlane", retStmt)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "plane TestPlane: Flags must use explicit []string literal")
	})
}
