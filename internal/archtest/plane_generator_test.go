package archtest

import (
	"go/parser"
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
		{
			name:      "FuncLit is accepted",
			exprSrc:   `func(v []string) []string { return v }`,
			wantError: false,
		},
		{
			name:      "Ident is accepted",
			exprSrc:   `materializeFunc`,
			wantError: false,
		},
		{
			name:      "SelectorExpr is accepted",
			exprSrc:   `request.MaterializeAttemptsSorted`,
			wantError: false,
		},
		{
			name:      "ParenExpr wrapping SelectorExpr is accepted",
			exprSrc:   `(request.MaterializeAttemptsSorted)`,
			wantError: false,
		},
		{
			name:      "ParenExpr wrapping FuncLit is accepted",
			exprSrc:   `((func(v []string) []string { return v }))`,
			wantError: false,
		},
		{
			name:      "BasicLit string is rejected",
			exprSrc:   `"invalid_string"`,
			wantError: true,
		},
		{
			name:      "CallExpr is rejected",
			exprSrc:   `createMaterializer()`,
			wantError: true,
		},
		{
			name:      "BinaryExpr is rejected",
			exprSrc:   `1 + 2`,
			wantError: true,
		},
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

	baseManifest := `package feature
import (
	"context"
	"fmt"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

var PlaneTest = Plane[[]toolpolicy.Policy]{
	ID: "test_policies",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject,
	Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	RequestMaterializer: func(v []toolpolicy.Policy) []toolpolicy.Policy { return v },
	RequestBorrow: true,
}

var StandardPlanes = []any{
	PlaneTest,
}
`
	_, err := GenerateFeaturePlanesCode([]byte(baseManifest))
	require.NoError(t, err, "valid slice plane with RequestBorrow and RequestMaterializer should succeed")

	// Non-slice plane with RequestBorrow: true must fail
	nonSliceManifest := `package feature
import (
	"context"
	"fmt"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

var PlaneTest = Plane[toolpolicy.Policy]{
	ID: "test_policies",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	NilPolicy: NilReject,
	Identity: func(v toolpolicy.Policy) (string, bool) { return "id", true },
	Validate: func(v toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },
	RequestMaterializer: func(v toolpolicy.Policy) toolpolicy.Policy { return v },
	RequestBorrow: true,
}

var StandardPlanes = []any{
	PlaneTest,
}
`
	_, err = GenerateFeaturePlanesCode([]byte(nonSliceManifest))
	require.Error(t, err, "non-slice plane with RequestBorrow: true must fail")
	assert.Contains(t, err.Error(), "RequestBorrow cannot be used on non-slice plane type")

	// Slice plane with RequestBorrow: true but no RequestMaterializer must fail
	missingMatManifest := `package feature
import (
	"context"
	"fmt"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

var PlaneTest = Plane[[]toolpolicy.Policy]{
	ID: "test_policies",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject,
	Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	RequestBorrow: true,
}

var StandardPlanes = []any{
	PlaneTest,
}
`
	_, err = GenerateFeaturePlanesCode([]byte(missingMatManifest))
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
	ID: "synthetic_slice",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject,
	Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	RequestMaterializer: func(v []toolpolicy.Policy) []toolpolicy.Policy { return v },
}

var StandardPlanes = []any{
	PlaneSyntheticSlice,
}
`

	generatedBytes, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.NoError(t, err)
	generatedCode := string(generatedBytes)

	// Assert exact generated assignment line using the helper
	assert.Contains(t, generatedCode, "syntheticSlice: materializeRequestSlice(gf.syntheticSlice, PlaneSyntheticSlice.RequestMaterializer),")

	// Assert old direct materializer forms are NOT present
	assert.NotContains(t, generatedCode, "cloneSlice(PlaneSyntheticSlice.RequestMaterializer(gf.syntheticSlice))")
	assert.NotContains(t, generatedCode, "syntheticSlice: PlaneSyntheticSlice.RequestMaterializer(gf.syntheticSlice)")
}
