// Package utils contains shared utilities for reference backend servers.
package utils

import (
	"io"
	"net/http"
)

// TryWriteForcedHTTPError writes a forced HTTP error if status is non-zero.
// If it returns true, the caller should return and stop processing.
func TryWriteForcedHTTPError(w http.ResponseWriter, status int, retryAfter string, body string, defaultBody func(int) string) bool {
	if status == 0 {
		return false
	}
	if retryAfter != "" && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == "" && defaultBody != nil {
		body = defaultBody(status)
	}
	_, _ = io.WriteString(w, body)
	return true
}
