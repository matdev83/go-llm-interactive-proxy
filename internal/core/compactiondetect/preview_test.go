package compactiondetect

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPreviewRequestIsPureAndSharesCommittedAuthority(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	call := textCall("context checkpoint compaction", 0)
	meta := reqMeta("preview-request")

	preview := d.PreviewRequest(meta, call)
	if preview.Kind != PreviewStartCandidate || preview.RuleID != "codex.local_checkpoint.v1" {
		t.Fatalf("preview=%+v", preview)
	}
	d.mu.Lock()
	legCount := len(d.legs)
	d.mu.Unlock()
	if legCount != 0 {
		t.Fatalf("request preview mutated detector legs: %d", legCount)
	}
	if again := d.PreviewRequest(meta, call); again.Kind != preview.Kind || again.RuleID != preview.RuleID {
		t.Fatalf("repeated preview changed: first=%+v second=%+v", preview, again)
	}
	if events := d.RequestOpened(meta, call); len(events) != 1 || events[0].Phase != "started" {
		t.Fatalf("committed request events=%v", events)
	}
}

func TestPreviewResponseIsPureAndCommittedReleaseStillSeesEvent(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	meta := reqMeta("preview-response")
	call := textCall("context checkpoint compaction", 0)
	if events := d.RequestOpened(meta, call); len(events) != 1 {
		t.Fatalf("start events=%v", events)
	}
	release := lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleAssistant,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "context checkpoint compaction"}},
	}}
	preview := d.PreviewResponse(meta, release)
	if preview.Kind != PreviewCompletionCandidate || preview.RuleID != "codex.local_checkpoint.v1" {
		t.Fatalf("preview=%+v", preview)
	}
	d.mu.Lock()
	legCount := len(d.legs)
	active := d.legs[meta.ALegID]
	activeCompleted := active != nil && active.active != nil && active.active.completed
	d.mu.Unlock()
	if legCount != 1 || active == nil || activeCompleted {
		t.Fatalf("response preview mutated detector state: legs=%d state=%+v", legCount, active)
	}
	if events := d.ResponseReleased(meta, release); len(events) != 1 || events[0].Phase != "completed" {
		t.Fatalf("committed response events=%v", events)
	}
	if events := d.ResponseReleased(meta, release); len(events) != 0 {
		t.Fatalf("duplicate committed response events=%v", events)
	}
}

func TestPreviewRequestHistoryBoundaryIsStableAndNonCommitting(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	prior := itemCall(bigText(40000), bigText(8000), bigText(8000))
	current := itemCall(bigText(4000), bigText(8000), bigText(8000))
	if events := d.RequestOpened(reqMeta("history-prior"), prior); len(events) != 0 {
		t.Fatalf("unexpected prior events=%v", events)
	}
	meta := reqMeta("history-current")
	preview := d.PreviewRequest(meta, current)
	if preview.Kind != PreviewCompletionCandidate || preview.Evidence != "history_heuristic" || preview.BoundaryFingerprint == "" {
		t.Fatalf("preview=%+v", preview)
	}
	d.mu.Lock()
	active := d.legs[meta.ALegID]
	lastTrace := ""
	if active != nil {
		lastTrace = active.lastFP.TraceID
	}
	d.mu.Unlock()
	if active == nil || lastTrace != "history-prior" {
		t.Fatalf("preview changed last fingerprint: leg=%+v", active)
	}
	if events := d.RequestOpened(meta, current); len(events) != 1 || events[0].Phase != "completed" {
		t.Fatalf("committed history events=%v", events)
	}
}
