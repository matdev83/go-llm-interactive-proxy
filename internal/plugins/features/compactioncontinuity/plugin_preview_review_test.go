package compactioncontinuity

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/observability"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

func TestBeforeRequest_previewObservationsUseRequestBoundaryCorrelation(t *testing.T) {
	t.Parallel()
	plugin, _, background := openFixture(t)
	plugin.cfg.Preserve.Plan = true
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	call := lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, ID: "plan-call", ToolCall: &lipapi.ToolCallItem{Name: "update_plan", CallID: "call-1", Arguments: []byte(`{"plan":[{"step":"preserve boundary","status":"in_progress"}]}`)}},
	}}
	boundary := "request-boundary"
	if err := plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: boundary}, openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeRequest: %v", err)
	}
	want := observability.HashID(boundary)
	for _, sample := range recorder.Snapshot() {
		if sample.Stage == observability.StageCarrier || sample.Stage == observability.StageCapsule {
			if sample.CorrelationHash != want {
				t.Fatalf("%s correlation hash=%q want request boundary hash %q", sample.Stage, sample.CorrelationHash, want)
			}
		}
	}
}

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
