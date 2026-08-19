package compactioncontinuity

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/observability"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

func TestBeforeResponseRelease_capsuleObservationRetainsConsumedPendingJobID(t *testing.T) {
	t.Parallel()
	plugin, _, background := pendingResponseFixture(t)
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	background.awaitResult = semanticResult()

	if err := plugin.BeforeResponseRelease(context.Background(), &lipapi.Event{Kind: lipapi.EventResponseFinished}, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeResponseRelease: %v", err)
	}

	want := observability.HashID("job-1")
	for _, sample := range recorder.Snapshot() {
		if sample.Stage == observability.StageCapsule && sample.Outcome == observability.OutcomeCompleted {
			if sample.CorrelationHash != want {
				t.Fatalf("capsule correlation hash=%q, want %q", sample.CorrelationHash, want)
			}
			return
		}
	}
	t.Fatalf("missing completed capsule observation: %#v", recorder.Snapshot())
}
