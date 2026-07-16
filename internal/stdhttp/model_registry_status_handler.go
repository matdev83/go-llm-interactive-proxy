package stdhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

type modelRegistryStatusResponse struct {
	Status             string                      `json:"status"`
	Generation         string                      `json:"generation"`
	RefreshedAt        time.Time                   `json:"refreshed_at"`
	ModelCount         int                         `json:"model_count"`
	BackendModelCounts map[string]int              `json:"backend_model_counts"`
	Discoveries        []modelRegistryDiscoveryRow `json:"discoveries"`
}

type modelRegistryDiscoveryRow struct {
	BackendID  string `json:"backend_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	ModelCount int    `json:"model_count"`
	ErrorCode  string `json:"error_code"`
}

// ModelRegistryStatusHandler serves protected GET JSON for live model-registry
// discovery diagnostics. Nil runtime yields unavailable/empty with 200.
type ModelRegistryStatusHandler struct {
	rt *modelregistry.Runtime
}

var _ http.Handler = (*ModelRegistryStatusHandler)(nil)

// NewModelRegistryStatusHandler returns a concrete diagnostics status handler.
func NewModelRegistryStatusHandler(rt *modelregistry.Runtime) *ModelRegistryStatusHandler {
	return &ModelRegistryStatusHandler{rt: rt}
}

func (h *ModelRegistryStatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	var rt *modelregistry.Runtime
	if h != nil {
		rt = h.rt
	}
	if err := enc.Encode(buildModelRegistryStatus(rt)); err != nil {
		slog.Default().ErrorContext(r.Context(), "modelregistry: status encode", "error", err)
	}
}

func buildModelRegistryStatus(rt *modelregistry.Runtime) modelRegistryStatusResponse {
	out := modelRegistryStatusResponse{
		Status:             "unavailable",
		BackendModelCounts: map[string]int{},
		Discoveries:        []modelRegistryDiscoveryRow{},
	}
	if rt == nil {
		return out
	}
	d := rt.Diagnostics()
	if d.Active {
		out.Status = "active"
	}
	out.Generation = d.Generation
	out.RefreshedAt = d.RefreshedAt
	out.ModelCount = d.ModelCount
	if d.BackendModelCounts != nil {
		out.BackendModelCounts = d.BackendModelCounts
	}
	rows := make([]modelRegistryDiscoveryRow, 0, len(d.BackendDiscoveries))
	for _, b := range d.BackendDiscoveries {
		rows = append(rows, modelRegistryDiscoveryRow{
			BackendID:  b.BackendID,
			Kind:       b.Kind,
			Status:     string(b.Status),
			Source:     string(b.Source),
			ModelCount: b.ModelCount,
			ErrorCode:  b.ErrorCode,
		})
	}
	slices.SortFunc(rows, func(a, b modelRegistryDiscoveryRow) int {
		if c := strings.Compare(a.BackendID, b.BackendID); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	out.Discoveries = rows
	return out
}
