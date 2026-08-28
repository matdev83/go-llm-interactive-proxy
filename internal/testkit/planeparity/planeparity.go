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
