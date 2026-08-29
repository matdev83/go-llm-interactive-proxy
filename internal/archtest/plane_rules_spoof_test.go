package archtest

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneRules_SpoofPackageBareGetRejected tests that a spoof sub-package under pkg/lipsdk/feature
// (such as pkg/lipsdk/feature/spoof) attempting to call bare Get is rejected because directory prefix
// matching has been replaced with exact directory matching (Finding 5).
func TestPlaneRules_SpoofPackageBareGetRejected(t *testing.T) {
	t.Parallel()

	spoofSrc := `package spoof

import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func SpoofRead(s lipfeature.FrozenPlaneSet) any {
	return lipfeature.Get(s, lipfeature.PlaneToolCallPolicies)
}
`
	findings := scanSyntheticSource(t, "pkg/lipsdk/feature/spoof/spoof.go", spoofSrc, Wave4_Tools)
	require.NotEmpty(t, findings, "spoof package under pkg/lipsdk/feature/ must NOT be allowed to call bare Get")
	assert.Equal(t, MirrorStageConsumer, findings[0].ShapeKind)
}

// TestPlaneRules_ForeignPackageRequestExecutionRejected tests that isRequestExecutionViewCall
// returns false when RequestExecution is invoked on a foreign package rather than lipsdk/feature (Finding 5).
func TestPlaneRules_ForeignPackageRequestExecutionRejected(t *testing.T) {
	t.Parallel()

	foreignSrc := `package extensions

import foreign "github.com/matdev83/go-llm-interactive-proxy/internal/foreignpkg"

func ForeignCaller(s foreign.SomeType) any {
	return foreign.RequestExecution(s).ToolCallPolicies()
}
`
	fset, f, err := ParseGoSource("internal/core/extensions/snapshot.go", []byte(foreignSrc))
	require.NoError(t, err)

	var callExpr *ast.CallExpr
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "ForeignCaller" {
			if ret, ok := fd.Body.List[0].(*ast.ReturnStmt); ok {
				callExpr, _ = ret.Results[0].(*ast.CallExpr)
			}
		}
	}
	require.NotNil(t, callExpr, "callExpr must be found")
	assert.False(t, isRequestExecutionViewCall("internal/core/extensions/snapshot.go", callExpr, f), "foreign RequestExecution must not be recognized as lipsdk/feature call")
	_ = fset
}

// TestPlaneRules_UnauthorizedStageAccessorExecutionRejected tests that an unauthorized stage accessor
// attempting to call thin execution views without whitelist authorization is flagged.
func TestPlaneRules_UnauthorizedStageAccessorExecutionRejected(t *testing.T) {
	t.Parallel()

	unauthSrc := `package extensions

import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

type RequestRuntimeSnapshot struct {
	planes lipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) UnauthorizedPoliciesExecution() any {
	return lipfeature.RequestExecution(s.planes).ToolCallPolicies()
}
`
	findings := scanSyntheticSource(t, "internal/core/extensions/snapshot.go", unauthSrc, Wave4_Tools)
	require.NotEmpty(t, findings, "unauthorized stage accessor using RequestExecutionView must be rejected")
	assert.Equal(t, MirrorStageConsumer, findings[0].ShapeKind)
}
