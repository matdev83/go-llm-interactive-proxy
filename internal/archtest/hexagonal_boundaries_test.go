package archtest

import (
	"testing"
)

// TestInternalCoreProductionClosureExcludesCompositionHelpers enforces dependency
// direction from introduce-hexagonal-architecture: the policy core must not import
// composition or assembly packages (bounded exceptions flow inward, not outward).
func TestInternalCoreProductionClosureExcludesCompositionHelpers(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "internal/core must not depend on pluginreg (composition root; invert inward only)",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "internal/core must not depend on runtimebundle (assembly outside core)",
		},
	})
}
