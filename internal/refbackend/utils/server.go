package utils

import (
	"io"
	"net/http"
)

// TryWriteForcedHTTPError handles writing a forced HTTP error response for reference backends.
// It returns true if an error was written (i.e., status != 0 and is an error status like 401/429).
func TryWriteForcedHTTPError(w http.ResponseWriter, status int, retryAfter, errorJSON string, defaultJSON func(int) string) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusTooManyRequests:
	default:
		// Not a forced error status, or 0
		return false
	}

	if retryAfter != "" && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	body := errorJSON
	if body == "" && defaultJSON != nil {
		body = defaultJSON(status)
	}
	_, _ = io.WriteString(w, body)
	return true
}
