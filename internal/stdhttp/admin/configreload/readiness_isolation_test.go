package configreload_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	mgmtreload "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/configreload"
)

// TestReadiness_IndependentOfReloadControlFailure proves req 13.1-13.2 / 14.8:
// a failed reload status remains visible on the management surface while the
// data-plane health probe stays healthy for the last-good active generation.
func TestReadiness_IndependentOfReloadControlFailure(t *testing.T) {
	t.Parallel()

	health := diag.HealthHandler()
	rrHealth := httptest.NewRecorder()
	health.ServeHTTP(rrHealth, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rrHealth.Code != http.StatusOK {
		t.Fatalf("health status=%d", rrHealth.Code)
	}
	if !strings.Contains(rrHealth.Body.String(), `"status":"ok"`) {
		t.Fatalf("health body=%s", rrHealth.Body.String())
	}

	st := configreload.ReloadStatus{
		ActiveGeneration: 11,
		Busy:             false,
		LastResult: configreload.ReloadResult{
			Category:         configreload.ResultInvalid,
			AttemptID:        9,
			ActiveGeneration: 11,
			ReasonCategory:   configreload.StageLoad,
		},
		LastFailure: configreload.ReloadResult{
			Category:         configreload.ResultInvalid,
			AttemptID:        9,
			ActiveGeneration: 11,
			ReasonCategory:   configreload.StageLoad,
		},
		LastSuccess: configreload.ReloadResult{
			Category:         configreload.ResultPublished,
			AttemptID:        8,
			ActiveGeneration: 11,
		},
		SourceIntegrity:     "ok",
		RetainedGenerations: 1,
		ControlDegraded:     true,
	}
	dto := mgmtreload.StatusFrom(st, "/fixed/config.yaml")
	if dto.FixedSourcePath != "/fixed/config.yaml" {
		t.Fatalf("fixed_source_path=%q", dto.FixedSourcePath)
	}
	if dto.ActiveGeneration != 11 {
		t.Fatalf("active=%d", dto.ActiveGeneration)
	}
	if dto.LastResult.Category != string(configreload.ResultInvalid) {
		t.Fatalf("last_result=%q", dto.LastResult.Category)
	}
	if !dto.ControlDegraded {
		t.Fatal("expected control_degraded visible on management status")
	}
	rr2 := httptest.NewRecorder()
	health.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("health after failed reload status=%d", rr2.Code)
	}
}
