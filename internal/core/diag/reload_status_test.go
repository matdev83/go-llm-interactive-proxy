package diag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func TestReloadDiagnostics_GenerationCorrelationProtected(t *testing.T) {
	t.Parallel()
	src := &staticReloadStatusSource{status: configreload.ReloadStatus{
		ActiveGeneration:    7,
		RetainedGenerations: 2,
		LastSuccess: configreload.ReloadResult{
			Category:         configreload.ResultPublished,
			AttemptID:        3,
			ActiveGeneration: 7,
		},
		LastFailure: configreload.ReloadResult{
			Category:       configreload.ResultInvalid,
			AttemptID:      4,
			ReasonCategory: configreload.StageLoad,
		},
		LastResult: configreload.ReloadResult{
			Category:       configreload.ResultInvalid,
			AttemptID:      4,
			ReasonCategory: configreload.StageLoad,
		},
		SourceIntegrity: "ok",
		ModelGeneration: "model-gen-9",
		History: []configreload.HistoryEntry{{
			AttemptID:           4,
			Trigger:             configreload.TriggerAPI,
			Stage:               configreload.StageLoad,
			Category:            configreload.ResultInvalid,
			ActiveGeneration:    7,
			CandidateGeneration: 0,
			RestartFieldCount:   0,
			ReasonCategory:      configreload.StageLoad,
		}},
	}}
	h, err := diag.ReloadStatusHandler(src)
	if err != nil {
		t.Fatal(err)
	}
	protected := diag.WrapDiagnosticsProtect("diag-secret-12chars", h)

	req := httptest.NewRequest(http.MethodGet, "/diagnostics/reload", nil)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unprotected status=%d want 403", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/diagnostics/reload", nil)
	req2.Header.Set(diag.HeaderDiagnosticsSecret, "diag-secret-12chars")
	rr2 := httptest.NewRecorder()
	protected.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active_generation"] != float64(7) {
		t.Fatalf("active_generation=%v", body["active_generation"])
	}
	if body["model_generation"] != "model-gen-9" {
		t.Fatalf("model_generation=%v", body["model_generation"])
	}
	raw := rr2.Body.String()
	for _, bad := range []string{"sk-", "password=", "postgres://", "api_key="} {
		if strings.Contains(strings.ToLower(raw), bad) {
			t.Fatalf("diagnostics leaked %q: %s", bad, raw)
		}
	}
}

type staticReloadStatusSource struct {
	status configreload.ReloadStatus
}

func (s *staticReloadStatusSource) ReloadStatus() configreload.ReloadStatus {
	return s.status
}
