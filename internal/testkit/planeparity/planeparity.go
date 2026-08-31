package planeparity

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertGeneratedSurfaceInvariants builds an independent oracle by validating and replaying
// each bundle sequentially into an independent ContributionSet using public lipfeature operations,
// and accumulating lifecycles only on success. It executes MergeBundlesGenerated and asserts exact
// value, nilness, identity, diagnostics, fail-before-mutate, and error attribution invariants.
func AssertGeneratedSurfaceInvariants(tb testing.TB, bundles ...lipfeature.FeatureBundle) {
	tb.Helper()

	// 1. Execute production MergeBundlesGenerated
	gen, genErr := featurebundle.MergeBundlesGenerated(bundles...)

	// 2. Build independent oracle outcome sequentially per-bundle
	oracleCS := lipfeature.NewContributionSet()
	var expectedLifecycles []lipplugin.Lifecycle

	for i, b := range bundles {
		// a. Derive exact production pluginID independently (not calling merge)
		pluginID := fmt.Sprintf("bundle-%d", i)
		if id, ok := lipfeature.FrozenIdentity(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}

		// b. Validate current bundle
		if valErr := b.Validate(); valErr != nil {
			require.Error(tb, genErr, "generated merge should fail when bundle validation fails at index %d", i)
			assert.Equal(tb, featurebundle.GeneratedMergeSurface{}, gen, "generated merge must return zero GeneratedMergeSurface on validation failure")
			assert.Nil(tb, gen.Lifecycles, "generated merge lifecycles must be nil on validation failure")

			assertValidationErrorParity(tb, genErr, valErr, pluginID)
			return
		}

		// c. Snapshot oracleCS before replay via Freeze plus diagnostics
		beforeFrozen := oracleCS.Freeze()
		beforeDiag := lipfeature.ProjectDiagnostics(beforeFrozen)

		// d. ReplayTo oracleCS
		var replayErr error
		if !b.PlaneSet.IsZero() {
			replayErr = b.PlaneSet.ReplayTo(oracleCS, pluginID)
		}

		if replayErr != nil {
			// Assert oracleCS.Freeze deep equals before (all plane Gets/identities/diagnostics)
			afterFrozen := oracleCS.Freeze()
			afterDiag := lipfeature.ProjectDiagnostics(afterFrozen)
			assert.Equal(tb, beforeDiag, afterDiag, "oracle diagnostics must not change on replay failure (fail-before-mutate)")
			assertPlaneCensus(tb, afterFrozen, beforeFrozen)

			// Assert production result zero/lifecycles nil
			require.Error(tb, genErr, "generated merge should fail on replay error at index %d", i)
			assert.Equal(tb, featurebundle.GeneratedMergeSurface{}, gen, "generated merge must return zero GeneratedMergeSurface on replay failure")
			assert.Nil(tb, gen.Lifecycles, "generated merge lifecycles must be nil on replay failure")

			assertReplayErrorParity(tb, genErr, replayErr, pluginID, bundles[:i+1])
			return
		}

		// e. Append lifecycle independently only after success preserving nil/empty and cloning
		if b.Lifecycles != nil {
			if expectedLifecycles == nil {
				expectedLifecycles = make([]lipplugin.Lifecycle, 0, len(b.Lifecycles))
			}
			expectedLifecycles = append(expectedLifecycles, slices.Clone(b.Lifecycles)...)
		}
	}

	// 3. Verify Success Path
	require.NoError(tb, genErr, "generated merge should succeed when oracle succeeds")

	oracleFrozen := oracleCS.Freeze()

	// Lifecycles side-channel invariants (exact shape/order/nilness)
	assert.Equal(tb, expectedLifecycles, gen.Lifecycles, "Lifecycles value mismatch")
	assert.Equal(tb, expectedLifecycles == nil, gen.Lifecycles == nil, "Lifecycles nilness mismatch")

	// Diagnostics projection equivalence
	assert.Equal(tb, lipfeature.ProjectDiagnostics(oracleFrozen), lipfeature.ProjectDiagnostics(gen.Frozen), "Diagnostics projection mismatch")

	// 25 Standard Plane Census
	assertPlaneCensus(tb, gen.Frozen, oracleFrozen)
}

func assertValidationErrorParity(tb testing.TB, genErr, valErr error, pluginID string) {
	tb.Helper()

	assert.Contains(tb, genErr.Error(), pluginID, "production error must contain pluginID %q", pluginID)

	sentinels := []error{
		lipfeature.ErrInvalidContribution,
		lipfeature.ErrNilContribution,
		lipfeature.ErrInvalidPlane,
		lipfeature.ErrUnsupportedSource,
		lipfeature.ErrUnsupportedReplaySource,
		lipfeature.ErrExclusiveConflict,
		lipfeature.ErrTerminalDecisionProviderConflict,
	}
	for _, sentinel := range sentinels {
		if errors.Is(valErr, sentinel) {
			assert.ErrorIs(tb, genErr, sentinel, "production error must wrap sentinel %v", sentinel)
		}
	}

	var expAttrErr *lipfeature.AttributedError
	if errors.As(valErr, &expAttrErr) {
		var genAttrErr *lipfeature.AttributedError
		if assert.ErrorAs(tb, genErr, &genAttrErr, "production error must wrap AttributedError") {
			assert.Equal(tb, expAttrErr.PlaneID, genAttrErr.PlaneID, "AttributedError plane ID mismatch")
			if expAttrErr.PluginID != "" {
				assert.Equal(tb, expAttrErr.PluginID, genAttrErr.PluginID, "AttributedError plugin ID mismatch")
			}
		}
	}
}

func assertReplayErrorParity(tb testing.TB, genErr, replayErr error, pluginID string, processedBundles []lipfeature.FeatureBundle) {
	tb.Helper()

	sentinels := []error{
		lipfeature.ErrExclusiveConflict,
		lipfeature.ErrTerminalDecisionProviderConflict,
		lipfeature.ErrInvalidContribution,
		lipfeature.ErrNilContribution,
		lipfeature.ErrUnsupportedSource,
		lipfeature.ErrUnsupportedReplaySource,
		lipfeature.ErrInvalidPlane,
	}
	for _, sentinel := range sentinels {
		if errors.Is(replayErr, sentinel) {
			assert.ErrorIs(tb, genErr, sentinel, "generated merge error must match sentinel %v", sentinel)
		}
	}

	var expAttrErr *lipfeature.AttributedError
	if errors.As(replayErr, &expAttrErr) {
		var genAttrErr *lipfeature.AttributedError
		if assert.ErrorAs(tb, genErr, &genAttrErr, "generated error must be an AttributedError") {
			assert.Equal(tb, expAttrErr.PlaneID, genAttrErr.PlaneID, "AttributedError plane ID mismatch")
			assert.Equal(tb, expAttrErr.PluginID, genAttrErr.PluginID, "AttributedError plugin ID mismatch")
		}
	}

	if errors.Is(replayErr, lipfeature.ErrExclusiveConflict) {
		var providerIDs []string
		for _, b := range processedBundles {
			if id, ok := lipfeature.FrozenIdentity(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
				providerIDs = append(providerIDs, id)
			}
		}
		if len(providerIDs) >= 2 {
			assert.Contains(tb, genErr.Error(), providerIDs[0], "genErr must contain first provider ID")
			assert.Contains(tb, genErr.Error(), providerIDs[len(providerIDs)-1], "genErr must contain conflicting provider ID")
		}
	}
}

func assertPlaneCensus(tb testing.TB, actual, expected lipfeature.FrozenPlaneSet) {
	tb.Helper()

	assertPlane(tb, actual, expected, lipfeature.PlaneSubmitHooks)
	assertPlane(tb, actual, expected, lipfeature.PlaneRequestPartHooks)
	assertPlane(tb, actual, expected, lipfeature.PlaneResponsePartHooks)
	assertPlane(tb, actual, expected, lipfeature.PlaneToolReactors)
	assertPlane(tb, actual, expected, lipfeature.PlaneSessionOpeners)
	assertPlane(tb, actual, expected, lipfeature.PlaneWorkspaceResolvers)
	assertPlane(tb, actual, expected, lipfeature.PlaneToolCatalogFilters)
	assertPlane(tb, actual, expected, lipfeature.PlaneToolCallPolicies)
	assertPlane(tb, actual, expected, lipfeature.PlaneToolCallFinalizers)
	assertPlane(tb, actual, expected, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assertPlane(tb, actual, expected, lipfeature.PlaneRequestTransforms)
	assertPlane(tb, actual, expected, lipfeature.PlanePreRequestHandlers)
	assertPlane(tb, actual, expected, lipfeature.PlaneRouteHintProviders)
	assertPlane(tb, actual, expected, lipfeature.PlaneCompletionGates)
	assertPlane(tb, actual, expected, lipfeature.PlaneAttemptTransforms)
	assertPlane(tb, actual, expected, lipfeature.PlaneStreamObserverFactories)
	assertPlane(tb, actual, expected, lipfeature.PlaneTrafficObservers)
	assertPlane(tb, actual, expected, lipfeature.PlaneUsageObservers)
	assertPlane(tb, actual, expected, lipfeature.PlaneRawCaptureSinks)
	assertPlane(tb, actual, expected, lipfeature.PlaneTrafficRedactors)
	assertPlane(tb, actual, expected, lipfeature.PlaneCompactionObservers)
	assertPlane(tb, actual, expected, lipfeature.PlaneCompactionPreservers)
	assertPlane(tb, actual, expected, lipfeature.PlaneSecretGuards)
	assertPlane(tb, actual, expected, lipfeature.PlaneLocalTurnHandlers)
	assertPlane(tb, actual, expected, lipfeature.PlaneTerminalDecisionProvider)
}

func assertPlane[T any](tb testing.TB, actual, expected lipfeature.FrozenPlaneSet, p lipfeature.Plane[T]) {
	tb.Helper()

	actVal := lipfeature.Get(actual, p)
	expVal := lipfeature.Get(expected, p)
	assert.Equal(tb, expVal, actVal, "plane %s value mismatch", p.ID)

	actID, actOK := lipfeature.FrozenIdentity(actual, p)
	expID, expOK := lipfeature.FrozenIdentity(expected, p)
	assert.Equal(tb, expOK, actOK, "plane %s identity presence mismatch", p.ID)
	assert.Equal(tb, expID, actID, "plane %s identity value mismatch", p.ID)
}
