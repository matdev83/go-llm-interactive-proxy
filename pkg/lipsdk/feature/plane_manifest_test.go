package feature_test

import (
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
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneAttemptTransforms.Rules.GenerationBinder)
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneStreamObserverFactories.Rules.GenerationBinder)

	// Exclusive plane: TerminalDecisionProvider
	assert.Equal(t, feature.CombExclusive, feature.PlaneTerminalDecisionProvider.Rules.Feature)
	assert.Equal(t, feature.MultExclusive, feature.PlaneTerminalDecisionProvider.Multiplicity)
}
