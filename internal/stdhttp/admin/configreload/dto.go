package configreload

import (
	"encoding/json"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// ResultDTO is the secret-safe JSON reload outcome (req 12.8, 12.10).
// It never carries YAML, credentials, DSNs, or configuration values.
type ResultDTO struct {
	Category           string   `json:"category"`
	AttemptID          int64    `json:"attempt_id"`
	ActiveGeneration   int64    `json:"active_generation"`
	PreviousGeneration int64    `json:"previous_generation,omitempty"`
	RestartFields      []string `json:"restart_required_fields,omitempty"`
	RestartFieldCount  int      `json:"restart_required_field_count,omitempty"`
	ReasonCategory     string   `json:"reason_category,omitempty"`
	CoalescedSignals   int64    `json:"coalesced_signals,omitempty"`
}

// StatusDTO is the secret-safe JSON status snapshot (req 12.10, 13.1-13.2, 14.1, 14.8).
type StatusDTO struct {
	ActiveGeneration    int64     `json:"active_generation"`
	LastResult          ResultDTO `json:"last_result"`
	LastSuccess         ResultDTO `json:"last_success,omitempty"`
	LastFailure         ResultDTO `json:"last_failure,omitempty"`
	SourceIntegrity     string    `json:"source_integrity,omitempty"`
	RetainedGenerations int       `json:"retained_generations,omitempty"`
	RetentionPressure   bool      `json:"retention_pressure,omitempty"`
	ControlDegraded     bool      `json:"control_degraded,omitempty"`
	ModelGeneration     string    `json:"model_generation,omitempty"`
	Busy                bool      `json:"busy"`
	FixedSourcePath     string    `json:"fixed_source_path"`
	PendingSignal       bool      `json:"pending_signal,omitempty"`
	CoalescedSignals    int64     `json:"coalesced_signals,omitempty"`
}

// HTTPStatusFor maps a terminal result category to the management HTTP status (req 12.8).
func HTTPStatusFor(category configreload.ResultCategory) int {
	switch category {
	case configreload.ResultPublished, configreload.ResultNoop:
		return http.StatusOK
	case configreload.ResultBusy, configreload.ResultRestartRequired, configreload.ResultRetentionBlocked:
		return http.StatusConflict
	case configreload.ResultInvalid, configreload.ResultSourceIntegrity:
		return http.StatusUnprocessableEntity
	case configreload.ResultCanceled, configreload.ResultPreparationFailed, configreload.ResultInternalFailed:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func resultDTO(res configreload.ReloadResult) ResultDTO {
	return ResultDTO{
		Category:           string(res.Category),
		AttemptID:          res.AttemptID,
		ActiveGeneration:   res.ActiveGeneration,
		PreviousGeneration: res.PreviousGeneration,
		RestartFields:      res.RestartFields,
		RestartFieldCount:  res.RestartFieldCount,
		ReasonCategory:     res.ReasonCategory,
		CoalescedSignals:   res.CoalescedSignals,
	}
}

func statusDTO(st configreload.ReloadStatus) StatusDTO {
	return StatusDTO{
		ActiveGeneration:    st.ActiveGeneration,
		LastResult:          resultDTO(st.LastResult),
		LastSuccess:         resultDTO(st.LastSuccess),
		LastFailure:         resultDTO(st.LastFailure),
		SourceIntegrity:     st.SourceIntegrity,
		RetainedGenerations: st.RetainedGenerations,
		RetentionPressure:   st.RetentionPressure,
		ControlDegraded:     st.ControlDegraded,
		ModelGeneration:     st.ModelGeneration,
		Busy:                st.Busy,
		FixedSourcePath:     st.FixedSourcePath,
		PendingSignal:       st.PendingSignal,
		CoalescedSignals:    st.CoalescedSignals,
	}
}

// StatusFrom projects a coordinator status snapshot into the management DTO.
func StatusFrom(st configreload.ReloadStatus) StatusDTO {
	return statusDTO(st)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Never emit permissive CORS (req 12.7).
	w.Header().Del("Access-Control-Allow-Origin")
	w.Header().Del("Access-Control-Allow-Credentials")
	w.Header().Del("Access-Control-Allow-Headers")
	w.Header().Del("Access-Control-Allow-Methods")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeCategory(w http.ResponseWriter, status int, category string) {
	writeJSON(w, status, map[string]string{"category": category})
}
