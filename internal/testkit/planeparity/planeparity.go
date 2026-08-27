package planeparity

import (
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertMergedSurfacesEqual verifies that a legacy MergedFeatureSurface and a GeneratedMergeSurface
// have byte-equivalent values and ordering across unmigrated declared extension planes and the
// Lifecycles side channel.
func AssertMergedSurfacesEqual(tb testing.TB, legacy featurebundle.MergedFeatureSurface, gen featurebundle.GeneratedMergeSurface) {
	tb.Helper()

	// 5. SessionOpeners
	genSessionOpeners := lipfeature.Get(gen.Frozen, lipfeature.PlaneSessionOpeners)
	assert.Equal(tb, legacy.SessionOpeners, genSessionOpeners, "SessionOpeners mismatch")
	assert.Equal(tb, legacy.SessionOpeners == nil, genSessionOpeners == nil, "SessionOpeners nilness mismatch")

	// 6. WorkspaceResolvers
	genWorkspaceResolvers := lipfeature.Get(gen.Frozen, lipfeature.PlaneWorkspaceResolvers)
	assert.Equal(tb, legacy.WorkspaceResolvers, genWorkspaceResolvers, "WorkspaceResolvers mismatch")
	assert.Equal(tb, legacy.WorkspaceResolvers == nil, genWorkspaceResolvers == nil, "WorkspaceResolvers nilness mismatch")

	// 7. ToolCatalogFilters
	genToolCatalogFilters := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCatalogFilters)
	assert.Equal(tb, legacy.ToolCatalogFilters, genToolCatalogFilters, "ToolCatalogFilters mismatch")
	assert.Equal(tb, legacy.ToolCatalogFilters == nil, genToolCatalogFilters == nil, "ToolCatalogFilters nilness mismatch")

	// 8. ToolCallPolicies
	genToolCallPolicies := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallPolicies)
	assert.Equal(tb, legacy.ToolCallPolicies, genToolCallPolicies, "ToolCallPolicies mismatch")
	assert.Equal(tb, legacy.ToolCallPolicies == nil, genToolCallPolicies == nil, "ToolCallPolicies nilness mismatch")

	// 9. ToolCallFinalizers
	genToolCallFinalizers := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers)
	assert.Equal(tb, legacy.ToolCallFinalizers, genToolCallFinalizers, "ToolCallFinalizers mismatch")
	assert.Equal(tb, legacy.ToolCallFinalizers == nil, genToolCallFinalizers == nil, "ToolCallFinalizers nilness mismatch")

	// 10. ToolCallFinalizationMaxArgsBytes
	genFinalizationCap := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(tb, legacy.ToolCallFinalizationMaxArgsBytes, genFinalizationCap, "ToolCallFinalizationMaxArgsBytes mismatch")

	// 11. RequestTransforms
	genRequestTransforms := lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms)
	assert.Equal(tb, legacy.RequestTransforms, genRequestTransforms, "RequestTransforms mismatch")
	assert.Equal(tb, legacy.RequestTransforms == nil, genRequestTransforms == nil, "RequestTransforms nilness mismatch")

	// 12. PreRequestHandlers
	genPreRequestHandlers := lipfeature.Get(gen.Frozen, lipfeature.PlanePreRequestHandlers)
	assert.Equal(tb, legacy.PreRequestHandlers, genPreRequestHandlers, "PreRequestHandlers mismatch")
	assert.Equal(tb, legacy.PreRequestHandlers == nil, genPreRequestHandlers == nil, "PreRequestHandlers nilness mismatch")

	// 13. RouteHintProviders
	genRouteHintProviders := lipfeature.Get(gen.Frozen, lipfeature.PlaneRouteHintProviders)
	assert.Equal(tb, legacy.RouteHintProviders, genRouteHintProviders, "RouteHintProviders mismatch")
	assert.Equal(tb, legacy.RouteHintProviders == nil, genRouteHintProviders == nil, "RouteHintProviders nilness mismatch")

	// 14. CompletionGates
	genCompletionGates := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)
	assert.Equal(tb, legacy.CompletionGates, genCompletionGates, "CompletionGates mismatch")
	assert.Equal(tb, legacy.CompletionGates == nil, genCompletionGates == nil, "CompletionGates nilness mismatch")

	// 15. AttemptTransforms
	genAttemptTransforms := lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms)
	assert.Equal(tb, legacy.AttemptTransforms, genAttemptTransforms, "AttemptTransforms mismatch")
	assert.Equal(tb, legacy.AttemptTransforms == nil, genAttemptTransforms == nil, "AttemptTransforms nilness mismatch")

	// 21. CompactionObservers
	genCompactionObservers := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompactionObservers)
	assert.Equal(tb, legacy.CompactionObservers, genCompactionObservers, "CompactionObservers mismatch")
	assert.Equal(tb, legacy.CompactionObservers == nil, genCompactionObservers == nil, "CompactionObservers nilness mismatch")

	// 22. CompactionPreservers
	genCompactionPreservers := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompactionPreservers)
	assert.Equal(tb, legacy.CompactionPreservers, genCompactionPreservers, "CompactionPreservers mismatch")
	assert.Equal(tb, legacy.CompactionPreservers == nil, genCompactionPreservers == nil, "CompactionPreservers nilness mismatch")

	// 23. SecretGuards
	genSecretGuards := lipfeature.Get(gen.Frozen, lipfeature.PlaneSecretGuards)
	assert.Equal(tb, legacy.SecretGuards, genSecretGuards, "SecretGuards mismatch")
	assert.Equal(tb, legacy.SecretGuards == nil, genSecretGuards == nil, "SecretGuards nilness mismatch")

	// 24. LocalTurnHandlers
	genLocalTurnHandlers := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	assert.Equal(tb, legacy.LocalTurnHandlers, genLocalTurnHandlers, "LocalTurnHandlers mismatch")
	assert.Equal(tb, legacy.LocalTurnHandlers == nil, genLocalTurnHandlers == nil, "LocalTurnHandlers nilness mismatch")

	// 25. TerminalDecisionProvider
	genTerminalProvider := lipfeature.Get(gen.Frozen, lipfeature.PlaneTerminalDecisionProvider)
	assert.Equal(tb, legacy.TerminalDecisionProvider, genTerminalProvider, "TerminalDecisionProvider mismatch")
	genID, hasID := lipfeature.FrozenIdentity(gen.Frozen, lipfeature.PlaneTerminalDecisionProvider)
	if legacy.TerminalDecisionProvider != nil {
		assert.True(tb, hasID, "expected frozen identity to be present for TerminalDecisionProvider")
		assert.NotEmpty(tb, genID, "expected non-empty frozen identity for TerminalDecisionProvider")
	} else {
		assert.False(tb, hasID, "expected no frozen identity when TerminalDecisionProvider is nil")
	}

	// 26. Lifecycles (Side-channel)
	assert.Equal(tb, legacy.Lifecycles, gen.Lifecycles, "Lifecycles side-channel mismatch")
	assert.Equal(tb, legacy.Lifecycles == nil, gen.Lifecycles == nil, "Lifecycles nilness mismatch")

	// 27. Full Projection Equivalence
	projected := gen.ToMergedFeatureSurface()
	assert.True(tb, reflect.DeepEqual(legacy, projected), "ToMergedFeatureSurface projection must be DeepEqual to legacy")
}

// AssertDualPathParity runs bundles through both legacy MergeBundlesChecked and generated
// MergeBundlesGenerated, asserting identical success/failure outcomes and byte-equivalent outputs on success.
//
// On failure, error text parity is intentionally NOT byte-equivalent: the generated path wraps failures
// in lipfeature.AttributedError with plugin ID and plane ID attribution, whereas the legacy path returns
// unadorned featurebundle errors. Per Requirement 4.2, operator-visible error SHAPE (both provider IDs present
// as substrings + sentinel error matchable via errors.Is) is the compatibility contract, not raw string equality.
func AssertDualPathParity(tb testing.TB, bundles ...lipfeature.FeatureBundle) {
	tb.Helper()

	legacy, legacyErr := featurebundle.MergeBundlesChecked(bundles...)
	gen, genErr := featurebundle.MergeBundlesGenerated(bundles...)

	if legacyErr == nil {
		require.NoError(tb, genErr, "generated merge should succeed when legacy succeeds")
		AssertMergedSurfacesEqual(tb, legacy, gen)
	} else {
		require.Error(tb, genErr, "generated merge should fail when legacy fails")
		require.Equal(tb, featurebundle.GeneratedMergeSurface{}, gen, "generated merge must discard candidate on failure")

		var attrErr *lipfeature.AttributedError
		require.ErrorAs(tb, genErr, &attrErr, "generated merge error must be an AttributedError")
		require.Equal(tb, lipfeature.PlaneTerminalDecisionProvider.ID, attrErr.PlaneID, "attributed error plane ID must match PlaneTerminalDecisionProvider")

		if errors.Is(legacyErr, featurebundle.ErrTerminalDecisionProviderConflict) {
			assert.ErrorIs(tb, genErr, lipfeature.ErrExclusiveConflict, "generated merge should wrap ErrExclusiveConflict")

			var providerIDs []string
			for _, b := range bundles {
				if b.TerminalDecisionProvider != nil {
					if id, err := terminaldecision.ProviderIdentity(b.TerminalDecisionProvider); err == nil && id != "" {
						providerIDs = append(providerIDs, id)
					}
				}
			}
			if len(providerIDs) >= 2 {
				assert.Contains(tb, legacyErr.Error(), providerIDs[0], "legacy error must contain first provider ID")
				assert.Contains(tb, legacyErr.Error(), providerIDs[1], "legacy error must contain second provider ID")
				assert.Contains(tb, genErr.Error(), providerIDs[0], "generated error must contain first provider ID")
				assert.Contains(tb, genErr.Error(), providerIDs[1], "generated error must contain second provider ID")
			}
		}
	}
}
