package feature_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// TestStandardPlanes_ManifestCompletenessAndValidation tests that the hand-authored
// plane manifest contains all 25 standard feature planes in stable ordinal order,
// and that all declarations pass ValidateDeclaration and ValidateManifest without error.
func TestStandardPlanes_ManifestCompletenessAndValidation(t *testing.T) {
	t.Parallel()

	require.Len(t, feature.StandardPlanes, 25, "manifest must declare exactly 25 standard planes")

	// Validate the entire manifest
	err := feature.ValidateManifest(feature.StandardPlanes...)
	require.NoError(t, err, "StandardPlanes manifest must pass ValidateManifest")

	seenIDs := make(map[string]bool, len(feature.StandardPlanes))
	for _, p := range feature.StandardPlanes {
		assert.False(t, seenIDs[p.PlaneID()], "duplicate plane ID in manifest: %s", p.PlaneID())
		seenIDs[p.PlaneID()] = true
	}

	expectedPlanes := []struct {
		id           string
		multiplicity feature.Multiplicity
		featComb     feature.Combination
		hasDiagStage bool
	}{
		{id: "submit_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "request_part_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "response_part_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_reactors", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "session_openers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "workspace_resolvers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_catalog_filters", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_policies", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_finalizers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_finalization_max_args_bytes", multiplicity: feature.MultOrdered, featComb: feature.CombReduce, hasDiagStage: false},
		{id: "request_transforms", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "pre_request_handlers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "route_hint_providers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "completion_gates", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "attempt_transforms", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "stream_observer_factories", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "traffic_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "usage_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "raw_capture_sinks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "traffic_redactors", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "compaction_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "compaction_preservers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "secret_guards", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "local_turn_handlers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "terminal_decision_provider", multiplicity: feature.MultExclusive, featComb: feature.CombExclusive, hasDiagStage: false},
	}

	for i, exp := range expectedPlanes {
		decl := feature.StandardPlanes[i]
		exp := exp
		t.Run(exp.id, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, exp.id, decl.PlaneID())

			err := decl.ValidateDeclaration()
			assert.NoError(t, err)
		})
	}
}

// TestStandardPlanes_SourceRulesPins verifies per-source rules (Feature, Host, GenerationBinder)
// for planes that require host injection or generation binder replace-by-identity semantics.
func TestStandardPlanes_SourceRulesPins(t *testing.T) {
	t.Parallel()

	// Host injection planes: TrafficObservers, UsageObservers
	assert.Equal(t, feature.CombConcatenate, feature.PlaneTrafficObservers.Rules.Host)
	assert.Equal(t, feature.CombConcatenate, feature.PlaneUsageObservers.Rules.Host)

	// GenerationBinder replace-by-identity planes: CompactionPreservers, AttemptTransforms, StreamObserverFactories
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneCompactionPreservers.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneCompactionPreservers.ValidateIdentity)
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneAttemptTransforms.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneAttemptTransforms.ValidateIdentity)
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneStreamObserverFactories.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneStreamObserverFactories.ValidateIdentity)

	// Exclusive plane: TerminalDecisionProvider
	assert.Equal(t, feature.CombExclusive, feature.PlaneTerminalDecisionProvider.Rules.Feature)
	assert.Equal(t, feature.MultExclusive, feature.PlaneTerminalDecisionProvider.Multiplicity)
	assert.Equal(t, feature.ErrTerminalDecisionProviderConflict, feature.PlaneTerminalDecisionProvider.ExclusiveConflictError)
	assert.NotNil(t, feature.PlaneTerminalDecisionProvider.ValidateIdentity)
}

// TestStandardCandidatePlanes_CanonicalDeclaration verifies the exact canonical candidate plane IDs.
func TestStandardCandidatePlanes_CanonicalDeclaration(t *testing.T) {
	t.Parallel()

	expected := []string{
		"session_openers",
		"workspace_resolvers",
		"tool_catalog_filters",
		"tool_call_policies",
		"tool_call_finalizers",
		"tool_call_finalization_max_args_bytes",
		"request_transforms",
		"pre_request_handlers",
		"route_hint_providers",
		"completion_gates",
		"attempt_transforms",
		"secret_guards",
		"compaction_observers",
		"compaction_preservers",
		"local_turn_handlers",
		"terminal_decision_provider",
	}
	assert.Equal(t, expected, feature.StandardCandidatePlanes)
}

// TestStandardCandidatePlanes_GeneratedMapDispatchCurrency verifies that the generated
// map candidate dispatch logic in plane_generated.go contains branches exactly matching StandardCandidatePlanes.
func TestStandardCandidatePlanes_GeneratedMapDispatchCurrency(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)
	genPath := filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_generated.go")
	genContentBytes, err := os.ReadFile(genPath)
	require.NoError(t, err)
	genContent := string(genContentBytes)

	sections := strings.Split(genContent, "func contributeCandidateMapTo(")
	require.Len(t, sections, 2, "plane_generated.go must contain contributeCandidateMapTo")
	mapMethodBody := strings.Split(sections[1], "\nfunc init() {\n")[0]

	// Map candidate branches must exactly match StandardCandidatePlanes
	for _, candID := range feature.StandardCandidatePlanes {
		// Convert snake_case ID to PascalCase plane var name e.g. request_transforms -> PlaneRequestTransforms.ID
		parts := strings.Split(candID, "_")
		for i, part := range parts {
			if len(part) > 0 {
				parts[i] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
		varRef := "Plane" + strings.Join(parts, "") + ".ID"
		assert.True(t, strings.Contains(mapMethodBody, varRef),
			"contributeCandidateMapTo must check candidate plane %s (%q)", varRef, candID)
	}
}

// TestStandardPlanes_HookTargetDeclarationAndCompleteness verifies that exactly the four
// canonical hook planes are annotated with their corresponding HookTarget constants,
// and all other standard planes have empty HookTarget metadata.
func TestStandardPlanes_HookTargetDeclarationAndCompleteness(t *testing.T) {
	t.Parallel()

	expectedHookPlanes := map[string]feature.HookTarget{
		"submit_hooks":        feature.HookTargetSubmitHooks,
		"request_part_hooks":  feature.HookTargetRequestPartHooks,
		"response_part_hooks": feature.HookTargetResponsePartHooks,
		"tool_reactors":       feature.HookTargetToolReactors,
	}

	annotatedCount := 0
	for _, decl := range feature.StandardPlanes {
		target := feature.DeclaredHookTargetForTest(decl)
		if expTarget, ok := expectedHookPlanes[decl.PlaneID()]; ok {
			assert.Equal(t, expTarget, target, "plane %s must have expected HookTarget", decl.PlaneID())
			annotatedCount++
		} else {
			assert.Empty(t, target, "plane %s must not have HookTarget annotation", decl.PlaneID())
		}
	}

	assert.Equal(t, 4, annotatedCount, "exactly four canonical hook planes must be annotated")
	assert.Equal(t, feature.HookTargetSubmitHooks, feature.PlaneSubmitHooks.HookTarget)
	assert.Equal(t, feature.HookTargetRequestPartHooks, feature.PlaneRequestPartHooks.HookTarget)
	assert.Equal(t, feature.HookTargetResponsePartHooks, feature.PlaneResponsePartHooks.HookTarget)
	assert.Equal(t, feature.HookTargetToolReactors, feature.PlaneToolReactors.HookTarget)
}

// TestPlaneDeclarationValidation_HookTarget verifies that Plane.ValidateDeclaration validates
// canonical HookTarget constants, rejects unknown HookTarget metadata, and ValidateManifest rejects
// duplicate HookTarget declarations.
func TestPlaneDeclarationValidation_HookTarget(t *testing.T) {
	t.Parallel()

	t.Run("ValidHookTargetsPassValidateDeclaration", func(t *testing.T) {
		t.Parallel()
		validTargets := []feature.HookTarget{
			"",
			feature.HookTargetSubmitHooks,
			feature.HookTargetRequestPartHooks,
			feature.HookTargetResponsePartHooks,
			feature.HookTargetToolReactors,
		}

		for _, target := range validTargets {
			plane := feature.Plane[string]{
				ID:           "test_plane",
				Multiplicity: feature.MultOrdered,
				Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
				Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
				HookTarget:   target,
			}
			assert.NoError(t, plane.ValidateDeclaration(), "valid hook target %q must pass validation", target)
		}
	})

	t.Run("UnknownHookTargetRejectedInValidateDeclaration", func(t *testing.T) {
		t.Parallel()
		invalidTargets := []feature.HookTarget{
			"UnknownTargetTypo",
			"submit_hooks",
			"SubmitHook",
			"ToolReactorErrorPolicy",
		}
		for _, target := range invalidTargets {
			plane := feature.Plane[string]{
				ID:           "test_plane",
				Multiplicity: feature.MultOrdered,
				Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
				Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
				HookTarget:   target,
			}
			err := plane.ValidateDeclaration()
			require.Error(t, err, "invalid hook target %q must fail validation", target)
			assert.ErrorIs(t, err, feature.ErrInvalidPlane)
			assert.Contains(t, err.Error(), string(target))
		}
	})

	t.Run("DuplicateHookTargetRejectedInValidateManifest", func(t *testing.T) {
		t.Parallel()
		plane1 := feature.Plane[string]{
			ID:           "test_plane_1",
			Multiplicity: feature.MultOrdered,
			Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
			Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
			HookTarget:   feature.HookTargetSubmitHooks,
		}
		plane2 := feature.Plane[string]{
			ID:           "test_plane_2",
			Multiplicity: feature.MultOrdered,
			Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
			Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
			HookTarget:   feature.HookTargetSubmitHooks,
		}
		err := feature.ValidateManifest(plane1, plane2)
		require.Error(t, err)
		assert.ErrorIs(t, err, feature.ErrInvalidPlane)
		assert.Contains(t, err.Error(), "SubmitHooks")
	})
}
