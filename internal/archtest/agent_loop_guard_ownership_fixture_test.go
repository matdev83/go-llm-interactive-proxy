package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Negative fixtures for the per-file ALG ownership ratchets: generic core
// (design ratchet 1), the generic control-tool SDK contract (design ratchet 2),
// and the control-tool plane declaration and execution view (design ratchet 3).
//
// A ratchet that scans the current tree and finds nothing proves the property
// holds today. It does not prove the ratchet WOULD CATCH a violation. Each test
// below therefore feeds a deliberately violating sample to the SAME validator the
// live-tree scan runs and asserts both halves of the verdict: the violating
// sample must be rejected for its own intended defect, and the correct sample
// must pass. This mirrors the established shapes in this package -
// request_attempt_state_baseline_test.go's temporary AST overlay and
// final_stream_observation_order_fixture_test.go's verdict fixtures.
//
// The feature-side fixtures (design ratchets 4, 5, 6) live in
// agent_loop_guard_feature_ownership_fixture_test.go and the strategy-isolation
// fixtures (design ratchets 7, 8) in
// agent_loop_guard_strategy_isolation_fixture_test.go.

// algFixtureHasFinding reports whether findings contain want, so a fixture can
// assert its own specific defect rather than merely "not empty".
func algFixtureHasFinding(findings []RuleFinding, rule, want string) bool {
	for _, finding := range findings {
		if finding.Rule == rule && strings.Contains(finding.Detail, want) {
			return true
		}
	}
	return false
}

func algFixtureRender(findings []RuleFinding) string {
	rendered := make([]string, 0, len(findings))
	for _, finding := range findings {
		rendered = append(rendered, finding.String())
	}
	return strings.Join(rendered, "\n")
}

// TestTask121FixtureCoreRejectsALGOwnership proves design ratchet 1 fires on
// each way generic core could name or branch on the concrete ALG feature, and
// that a neutral generic file still passes.
func TestTask121FixtureCoreRejectsALGOwnership(t *testing.T) {
	t.Parallel()
	const neutral = `package runtime

func admissionEnabled() bool {
	return true
}
`
	t.Run("positive neutral generic core passes", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, "internal/core/runtime/neutral.go", neutral))
	})

	for _, tc := range []struct {
		name    string
		relPath string
		src     string
		want    string
	}{
		{
			name:    "imports the concrete feature",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nimport _ \"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard\"\n",
			want:    "imports the concrete ALG feature",
		},
		{
			name:    "imports a concrete feature subpackage",
			relPath: "internal/core/routing/sub/bypass.go",
			src:     "package routing\n\nimport _ \"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier\"\n",
			want:    "imports the concrete ALG feature",
		},
		{
			name:    "names the feature identifier",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nvar agentLoopGuard bool\n",
			want:    `names the concrete ALG seam "agentLoopGuard"`,
		},
		{
			name:    "names the feature type",
			relPath: "internal/core/extensions/renamed.go",
			src:     "package extensions\n\ntype agentLoopGuardEnabled struct{}\n",
			want:    `names the concrete ALG seam "agentLoopGuardEnabled"`,
		},
		{
			name:    "names the retired guard predicate",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nfunc isLoopGuardEnabled() bool { return false }\n",
			want:    `names the concrete ALG seam "isLoopGuardEnabled"`,
		},
		{
			name:    "branches on the legacy strategy selector",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nfunc branch(strategy string) string {\n\tswitch strategy {\n\tcase \"semantic_verifier\":\n\t\treturn \"legacy\"\n\tdefault:\n\t\treturn \"other\"\n\t}\n}\n",
			want:    `embeds the concrete ALG literal "semantic_verifier"`,
		},
		{
			name:    "names the proxy-owned completion tool",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nfunc isCompletion(name string) bool { return name == \"attempt_completion\" }\n",
			want:    `embeds the concrete ALG literal "attempt_completion"`,
		},
		{
			name:    "embeds the hyphenated feature identity",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nconst algIdentity = \"agent-loop-guard\"\n",
			want:    `embeds the concrete ALG literal "agent-loop-guard"`,
		},
		{
			name:    "embeds the separated-underscore feature identity",
			relPath: "internal/core/runtime/renamed.go",
			src:     "package runtime\n\nconst algIdentity = \"agent_loop_guard\"\n",
			want:    `embeds the concrete ALG literal "agent_loop_guard"`,
		},
		{
			name:    "passes the separated-underscore identity to the suppression seam",
			relPath: "internal/core/runtime/conversation_view.go",
			src:     "package runtime\n\nfunc suppressed(ctx context.Context) bool { return execctx.IsSuppressedPluginID(ctx, \"agent_loop_guard\") }\n",
			want:    `embeds the concrete ALG literal "agent_loop_guard"`,
		},
		{
			name:    "compares a snapshot overlay against the ALG-only recovery identity",
			relPath: "internal/core/runtime/conversation_view.go",
			src:     "package runtime\n\nfunc stale(ov Overlay) bool { return ov.OverlayID == \"alg-rec\" && ov.Active }\n",
			want:    `embeds the concrete ALG literal "alg-rec"`,
		},
		{
			name:    "deactivates the ALG-only recovery overlay",
			relPath: "internal/core/runtime/conversation_view.go",
			src:     "package runtime\n\nfunc drop(ctx context.Context, w Writer) error {\n\t_, err := w.Deactivate(ctx, steering.OverlayID(\"alg-rec\"))\n\treturn err\n}\n",
			want:    `embeds the concrete ALG literal "alg-rec"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanAlgOwnershipSyntheticSource(t, tc.relPath, tc.src)
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGCoreOwnership, tc.want),
				"core ownership ratchet must reject this violation for its own reason:\n%s", algFixtureRender(findings))
		})
	}

	// The pre-fix shape of internal/core/runtime/conversation_view.go is pinned
	// here as a REJECTED sample: the concrete ALG plugin ID and the ALG-only
	// recovery overlay identity were hardcoded there, in generic core, at three
	// sites. The ratchet waives nothing, so the same file path carries no
	// exemption: the fixed production source is a synthetic sample whose verdict
	// is asserted rather than a live allowlist entry.
	t.Run("no core file is exempt from the vocabulary", func(t *testing.T) {
		t.Parallel()
		const preFixCoreSource = `package runtime

func suppressed(ctx context.Context) bool {
	return !execctx.IsSuppressedPluginID(ctx, "agent_loop_guard")
}

func stale(snap conversationprojection.Snapshot) bool {
	hasActiveAlgRec := false
	for _, ov := range snap.Steering {
		if ov.OverlayID == "alg-rec" && ov.Active {
			hasActiveAlgRec = true
			break
		}
	}
	return hasActiveAlgRec
}

func drop(ctx context.Context, w steeringWriter) error {
	_, derr := w.Deactivate(ctx, steering.OverlayID("alg-rec"))
	return derr
}
`
		findings := scanAlgOwnershipSyntheticSource(t, "internal/core/runtime/conversation_view.go", preFixCoreSource)
		require.NotEmpty(t, findings, algFixtureRender(findings))
		for _, want := range []string{
			`embeds the concrete ALG literal "agent_loop_guard"`,
			`embeds the concrete ALG literal "alg-rec"`,
		} {
			assert.True(t, algFixtureHasFinding(findings, RuleALGCoreOwnership, want),
				"core ownership ratchet must reject the pre-fix core source for %s:\n%s", want, algFixtureRender(findings))
		}
	})
}

// TestTask121FixtureControlToolSDKRejectsALGDependency proves design ratchet 2
// fires on any internal import or ALG name in the generic contract.
func TestTask121FixtureControlToolSDKRejectsALGDependency(t *testing.T) {
	t.Parallel()
	const neutral = `package controltool

func ValidateSpec(spec Spec) error { return nil }
`
	t.Run("positive generic contract passes", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, "pkg/lipsdk/controltool/validate_extra.go", neutral))
	})

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "imports the concrete feature",
			src:  "package controltool\n\nimport _ \"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard\"\n",
			want: "imports internal package",
		},
		{
			name: "imports generic runtime",
			src:  "package controltool\n\nimport _ \"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime\"\n",
			want: "imports internal package",
		},
		{
			name: "pins the feature identity",
			src:  "package controltool\n\nconst providerIdentity = \"agent-loop-guard\"\n",
			want: `embeds the concrete ALG literal "agent-loop-guard"`,
		},
		{
			name: "names the feature",
			src:  "package controltool\n\nvar agentLoopGuard bool\n",
			want: `names the concrete ALG seam "agentLoopGuard"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanAlgOwnershipSyntheticSource(t, "pkg/lipsdk/controltool/validate_extra.go", tc.src)
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGControlToolSDKOwnership, tc.want),
				"control-tool SDK ratchet must reject this violation for its own reason:\n%s", algFixtureRender(findings))
		})
	}
}

// TestTask121FixtureControlToolPlaneRejectsSharedSlot proves the exclusive-slot
// ratchet fires on every way the control-tool plane could admit a second
// occupant, while the live declaration passes.
func TestTask121FixtureControlToolPlaneRejectsSharedSlot(t *testing.T) {
	t.Parallel()
	live := liveAlgControlToolPlaneShape()
	t.Run("positive live declaration is exclusive", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, ValidateAlgControlToolExclusivePlane(live))
	})

	for _, tc := range []struct {
		name   string
		mutate func(algControlToolPlaneShape) algControlToolPlaneShape
		want   string
	}{
		{
			name:   "ordered multiplicity admits a second occupant",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.Multiplicity = algMultOrdered; return s },
			want:   "multiplicity = \"ordered\"",
		},
		{
			name:   "concatenating feature rule admits a second feature",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.FeatureRule = "concatenate"; return s },
			want:   "feature combine rule = \"concatenate\"",
		},
		{
			name:   "nil-skip policy publishes a typed nil",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.NilPolicy = "skip"; return s },
			want:   "nil policy = \"skip\"",
		},
		{
			name:   "missing identity loses attribution",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.HasIdentity = false; return s },
			want:   "must require a stable identity",
		},
		{
			name:   "missing plane id",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.ID = ""; return s },
			want:   "must declare a stable plane ID",
		},
		{
			name:   "plane absent from the standard list",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.StandardPlaneUses = 0; return s },
			want:   "appears 0 times in the standard plane list",
		},
		{
			name:   "plane duplicated in the standard list",
			mutate: func(s algControlToolPlaneShape) algControlToolPlaneShape { s.StandardPlaneUses = 2; return s },
			want:   "appears 2 times in the standard plane list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := ValidateAlgControlToolExclusivePlane(tc.mutate(live))
			require.NotEmpty(t, findings, algFixtureRender(findings))
			assert.True(t, algFixtureHasFinding(findings, RuleALGExclusiveControlToolPlane, tc.want),
				"exclusive-slot ratchet must reject this shape for its own reason:\n%s", algFixtureRender(findings))
		})
	}
}

// TestTask121FixtureControlToolPlaneRejectsFrontendAccessor proves the second
// half of design ratchet 3: exposing the control-tool plane on the
// request-execution view would surface it to the frontend/client tool path.
func TestTask121FixtureControlToolPlaneRejectsFrontendAccessor(t *testing.T) {
	t.Parallel()
	t.Run("positive request-execution accessors pass", func(t *testing.T) {
		t.Parallel()
		src := `package feature

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

func (v RequestExecutionView) ToolCallPolicies() []toolpolicy.Policy { return nil }
`
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, "pkg/lipsdk/feature/plane_generated.go", src))
	})

	t.Run("control-tool accessor on the execution view is rejected", func(t *testing.T) {
		t.Parallel()
		src := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

func (v RequestExecutionView) ControlToolProvider() controltool.Provider { return nil }
`
		findings := scanAlgOwnershipSyntheticSource(t, "pkg/lipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings, algFixtureRender(findings))
		assert.True(t, algFixtureHasFinding(findings, RuleALGControlToolPlaneExecution,
			"exposes the proxy-owned control-tool provider to the frontend execution path"),
			"plane-execution ratchet must reject this accessor:\n%s", algFixtureRender(findings))
	})

	t.Run("unrelated plane accessor is not affected", func(t *testing.T) {
		t.Parallel()
		src := `package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"

func ControlToolProviderForTest(v FrozenPlaneSet) controltool.Provider { return nil }
`
		assert.Empty(t, scanAlgOwnershipSyntheticSource(t, "pkg/lipsdk/feature/plane_generated.go", src))
	})
}
