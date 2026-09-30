package largebody_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
)

// This implementation-time baseline uses an existing MetadataOnly plane as a
// stand-in for the not-yet-declared session classifier plane. When the
// classifier plane is added, keep this access/disposition behavior and extend
// the census-backed coverage to its ID.
func TestSessionClassificationBaseline_OccupiedMetadataOnlyPlane(t *testing.T) {
	t.Parallel()

	const generationID = "gen-session-classification-baseline"
	summary := compile35(t, occupy35Plane(eligible35Input(generationID), "session_openers"))
	disposition, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   generationID,
		ThresholdBytes: 1,
		ContentLength:  2,
	})

	require.Equal(t, largebody.NeedsRequestAssessment, disposition)
	require.Equal(t, largebody.StaticWireReasonNone, reason)
}

// Before Task 7.1, neither the bounded proof nor its runtime facts carry
// classification evidence. Task 7.1 should replace the absence assertions
// with bounded-evidence validation while retaining the no-shadow-Call guard.
func TestSessionClassificationBaseline_WireFactsHaveNoClassificationEvidenceOrShadowCall(t *testing.T) {
	t.Parallel()

	for _, typ := range []reflect.Type{
		reflect.TypeFor[largebody.Proof](),
		reflect.TypeFor[largebody.WireSessionFacts](),
		reflect.TypeFor[largebody.WireTurnFacts](),
	} {
		for i := range typ.NumField() {
			name := typ.Field(i).Name
			require.NotContains(t, name, "Classification", "%s unexpectedly carries classification evidence", typ)
			require.NotEqual(t, "ClientUserAgent", name, "%s unexpectedly carries a raw client identity", typ)
			require.NotEqual(t, "ToolCategories", name, "%s unexpectedly carries tool classification bits", typ)
		}
	}

	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}
