package extensions_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
)

func TestLegalPipelineStageNames_interleavedShapingOutsideExtensionStages(t *testing.T) {
	t.Parallel()
	stages := extensions.LegalPipelineStageNames()
	if !slices.Contains(stages, extensions.StagePreRequest) {
		t.Fatal("pre-request must remain in legal pipeline")
	}
	if idx := slices.Index(stages, extensions.StageAttemptLifecycle); idx < 0 {
		t.Fatal("attempt lifecycle must remain in legal pipeline")
	}
	preIdx := slices.Index(stages, extensions.StagePreRequest)
	attemptIdx := slices.Index(stages, extensions.StageAttemptLifecycle)
	if preIdx >= attemptIdx {
		t.Fatalf("pre-request (%d) must precede attempt lifecycle (%d)", preIdx, attemptIdx)
	}
	for _, s := range stages {
		if strings.Contains(strings.ToLower(s), "interleaved") || strings.Contains(strings.ToLower(s), "thinker") {
			t.Fatalf("interleaved shaping must not be an extension stage, found %q", s)
		}
	}
}
