package gemini

import (
	"encoding/json"
	"net/http"
)

type wireAPIError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// WriteErrorJSON writes a Google-style JSON error for generateContent failures.
func WriteErrorJSON(w http.ResponseWriter, status int, message string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Error.Code = status
	we.Error.Message = message
	switch {
	case status == http.StatusBadRequest:
		we.Error.Status = "INVALID_ARGUMENT"
	case status >= 500:
		we.Error.Status = "INTERNAL"
	default:
		we.Error.Status = "UNKNOWN"
	}
	return json.NewEncoder(w).Encode(we)
}
