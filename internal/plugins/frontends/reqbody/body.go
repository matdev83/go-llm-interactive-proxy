// Package reqbody centralizes bounded HTTP request body reads for frontend handlers.
package reqbody

import (
	"io"
	"net/http"
	"strings"
)

// DefaultMaxBytes is the maximum request body size when no explicit limit is set.
const DefaultMaxBytes int64 = 8 << 20

// ReadAll reads r.Body using http.MaxBytesReader. On limit exceeded it returns a non-nil err
// for which TooLarge returns true; callers should respond with HTTP 413 without treating it as JSON parse failure.
func ReadAll(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	lr := http.MaxBytesReader(w, r.Body, maxBytes)
	defer lr.Close()
	return io.ReadAll(lr)
}

// TooLarge reports whether err is from exceeding MaxBytesReader's limit.
func TooLarge(err error) bool {
	if err == nil {
		return false
	}
	// net/http returns this stable string from MaxBytesReader (see net/http request.go).
	return strings.Contains(err.Error(), "request body too large")
}
