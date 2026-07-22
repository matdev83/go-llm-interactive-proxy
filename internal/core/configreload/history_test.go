package configreload_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

func TestStatusHistory_BoundedRing(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(3)
	for i := int64(1); i <= 5; i++ {
		h.Append(configreload.HistoryEntry{
			AttemptID:        i,
			Trigger:          configreload.TriggerAPI,
			Stage:            configreload.StagePublish,
			Category:         configreload.ResultPublished,
			ActiveGeneration: i,
			DurationMs:       i * 10,
			RecordedAt:       time.Unix(i, 0).UTC(),
		})
	}
	snap := h.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len=%d want 3", len(snap))
	}
	if snap[0].AttemptID != 3 || snap[2].AttemptID != 5 {
		t.Fatalf("ring order=%v want attempts 3..5", []int64{snap[0].AttemptID, snap[1].AttemptID, snap[2].AttemptID})
	}
}

func TestStatusHistory_ZeroCapacityDefaults(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(0)
	h.Append(configreload.HistoryEntry{AttemptID: 1, Category: configreload.ResultNoop})
	if len(h.Snapshot()) != 1 {
		t.Fatalf("default capacity should accept at least one entry")
	}
}
