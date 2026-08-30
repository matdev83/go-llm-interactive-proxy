//go:build red

package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRED_HookProjectionRatchet_RealBuildFeatureHooksRejectedWithoutExemption characterizes Requirement 3.4, 3.5, 5.1 (Task 1.3):
// The real production build_feature_hooks.go (specifically HooksConfigFromFrozen) must be rejected
// as a forbidden mirror when no hook allowlist bypass applies.
// On baseline before Task 3.3 & 3.4, build_feature_hooks.go contains handwritten per-plane Get reads for all 4 hook planes.
// This test uses the real ScanFileForForbiddenMirrors scanner and fails on the review baseline, then turns green
// when Task 3.3 removes handwritten projections.
func TestRED_HookProjectionRatchet_RealBuildFeatureHooksRejectedWithoutExemption(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	relPath := "internal/infra/runtimebundle/build_feature_hooks.go"
	absPath := filepath.Join(root, filepath.FromSlash(relPath))

	src, err := os.ReadFile(absPath)
	require.NoError(t, err, "failed to read production file %s", absPath)

	// Rename the exact allowlisted function symbol to a non-allowlisted symbol whose lowercase name still
	// contains "hooksconfig", ensuring ScanFileForForbiddenMirrors routes it to projection body inspection
	// without mutating global maps or referencing an allowlist symbol that Task 3.4 will delete.
	modifiedSrc := strings.Replace(string(src), "func HooksConfigFromFrozen(", "func UnexemptedHooksConfigFromFrozen(", 1)

	fset, f, err := ParseGoSource(absPath, []byte(modifiedSrc))
	require.NoError(t, err, "failed to parse production file %s", absPath)

	t.Run("Wave1_HookBus_ScanWithoutAllowlist", func(t *testing.T) {
		t.Parallel()
		findings := ScanFileForForbiddenMirrors(relPath, []byte(modifiedSrc), fset, f, Wave1_HookBus)
		// On baseline, this asserts 0 findings and fails because HooksConfigFromFrozen has 5 forbidden projection branches.
		require.Empty(t, findings, "production build_feature_hooks.go must contain zero forbidden hook projection mirrors without allowlist exemption")
	})

	t.Run("Wave5c_Residual_ScanWithoutAllowlist", func(t *testing.T) {
		t.Parallel()
		findings := ScanFileForForbiddenMirrors(relPath, []byte(modifiedSrc), fset, f, Wave5c_Residual)
		require.Empty(t, findings, "production build_feature_hooks.go must contain zero forbidden hook projection mirrors at Wave5c")
	})
}

// TestRED_HookProjectionRatchet_SyntheticHandwrittenProjectionRejected characterizes Requirement 3.4, 5.1 (Task 1.3):
// Any synthetic handwritten hook projection that reads the four declared hook planes via Get is detected
// and rejected with MirrorProjectionBranch violations at Wave 1.
func TestRED_HookProjectionRatchet_SyntheticHandwrittenProjectionRejected(t *testing.T) {
	t.Parallel()

	syntheticHookProjSrc := `package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func ManualHookProjection(f lipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks:       lipfeature.Get(f, lipfeature.PlaneSubmitHooks),
		RequestPartHooks:  lipfeature.Get(f, lipfeature.PlaneRequestPartHooks),
		ResponsePartHooks: lipfeature.Get(f, lipfeature.PlaneResponsePartHooks),
		ToolReactors:      lipfeature.Get(f, lipfeature.PlaneToolReactors),
	}
}
`

	fset, f, err := ParseGoSource("internal/infra/runtimebundle/build_feature_hooks.go", []byte(syntheticHookProjSrc))
	require.NoError(t, err)

	findings := ScanFileForForbiddenMirrors("internal/infra/runtimebundle/build_feature_hooks.go", []byte(syntheticHookProjSrc), fset, f, Wave1_HookBus)

	require.Len(t, findings, 4, "must detect exactly four forbidden hook projection branches")

	planeIDsFound := make(map[string]bool)
	for _, finding := range findings {
		assert.Equal(t, MirrorProjectionBranch, finding.ShapeKind)
		planeIDsFound[finding.PlaneID] = true
	}

	assert.True(t, planeIDsFound["submit_hooks"], "must find forbidden projection branch for submit_hooks")
	assert.True(t, planeIDsFound["request_part_hooks"], "must find forbidden projection branch for request_part_hooks")
	assert.True(t, planeIDsFound["response_part_hooks"], "must find forbidden projection branch for response_part_hooks")
	assert.True(t, planeIDsFound["tool_reactors"], "must find forbidden projection branch for tool_reactors")
	assert.False(t, planeIDsFound["tool_reactor_error_policy"], "host tool reactor error policy is not a feature plane")
	assert.Equal(t, map[string]bool{
		"submit_hooks":        true,
		"request_part_hooks":  true,
		"response_part_hooks": true,
		"tool_reactors":       true,
	}, planeIDsFound, "must find exactly the four declared feature hook plane IDs")
}
