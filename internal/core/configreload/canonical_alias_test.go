package configreload_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

func TestCanonicalAliases_ShareUnderlyingTypes(t *testing.T) {
	t.Parallel()
	var (
		_ sdkreload.Trigger        = configreload.ReloadTrigger{}
		_ sdkreload.Result         = configreload.ReloadResult{}
		_ sdkreload.Status         = configreload.ReloadStatus{}
		_ sdkreload.HistoryEntry   = configreload.HistoryEntry{}
		_ sdkreload.TriggerKind    = configreload.TriggerKind("")
		_ sdkreload.ResultCategory = configreload.ResultCategory("")
	)
	if configreload.ResultPublished != sdkreload.ResultPublished {
		t.Fatal("ResultPublished alias drift")
	}
	if configreload.TriggerAPI != sdkreload.TriggerAPI {
		t.Fatal("TriggerAPI alias drift")
	}
	if len(configreload.AllResultCategories) != len(sdkreload.AllResultCategories) {
		t.Fatal("AllResultCategories alias drift")
	}
}

func TestStatus_NoFixedSourcePathField(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[configreload.ReloadStatus]()
	if _, ok := rt.FieldByName("FixedSourcePath"); ok {
		t.Fatal("internal ReloadStatus alias must not expose FixedSourcePath")
	}
	st := configreload.ReloadStatus{ActiveGeneration: 3, Busy: true}
	if st.ActiveGeneration != 3 || !st.Busy {
		t.Fatalf("status fields broken: %+v", st)
	}
}

func TestStatusHistory_SanitizesActorAndUsesCanonicalEntry(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(2)
	h.Append(configreload.HistoryEntry{
		AttemptID:  1,
		Trigger:    configreload.TriggerAPI,
		Category:   configreload.ResultPublished,
		SafeActor:  "password=super-secret-value",
		Stage:      configreload.StagePublish,
		RecordedAt: time.Unix(1, 0).UTC(),
	})
	snap := h.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].SafeActor == "password=super-secret-value" {
		t.Fatal("expected secret-looking actor to be redacted")
	}
	if snap[0].Trigger != sdkreload.TriggerAPI {
		t.Fatalf("trigger=%q", snap[0].Trigger)
	}
}
