package extensions_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
)

// wantLegalPipelineOrder is the canonical extension pipeline. Keep aligned with ADR 0006 plus
// pre-request admission before route planning.
var wantLegalPipelineOrder = []string{
	extensions.StageTransportAuth,
	extensions.StageSessionOpen,
	extensions.StageSubmit,
	extensions.StageToolCatalog,
	extensions.StageRequestWide,
	extensions.StagePreRequest,
	extensions.StageRouteHinting,
	extensions.StageAttemptLifecycle,
	extensions.StageStreamEventMutation,
	extensions.StageToolEventReaction,
	extensions.StageCompletionGating,
	extensions.StageTrafficObservation,
	extensions.StageEgressEncoding,
}

func TestLegalPipelineStageNames_matchesR2CanonicalOrder_RED(t *testing.T) {
	t.Parallel()
	got := extensions.LegalPipelineStageNames()
	if len(got) != len(wantLegalPipelineOrder) {
		t.Fatalf("RED stage four: LegalPipelineStageNames must return %d R2 stages (got %d)",
			len(wantLegalPipelineOrder), len(got))
	}
	if !slices.Equal(got, wantLegalPipelineOrder) {
		t.Fatalf("RED stage four: stage order mismatch\ngot  %#v\nwant %#v", got, wantLegalPipelineOrder)
	}
}
