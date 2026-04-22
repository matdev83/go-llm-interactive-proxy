// Package reqbody centralizes bounded HTTP request body reads for frontend handlers.
package reqbody

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxBytes is the maximum request body size when no explicit limit is set.
const DefaultMaxBytes int64 = 8 << 20

// ReadAll reads r.Body using http.MaxBytesReader. On limit exceeded it returns a non-nil err
// for which TooLarge returns true; callers should respond with HTTP 413 without treating it as JSON parse failure.
func ReadAll(w http.ResponseWriter, r *http.Request, maxBytes int64) (data []byte, err error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	lr := http.MaxBytesReader(w, r.Body, maxBytes)
	defer func() {
		if cerr := lr.Close(); cerr != nil {
			closeErr := fmt.Errorf("reqbody: close body reader: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	data, err = io.ReadAll(lr)
	return data, err
}

// TooLarge reports whether err is from exceeding MaxBytesReader's limit.
// It uses errors.As so any error in the chain that unwraps to *http.MaxBytesError matches.
func TooLarge(err error) bool {
	if err == nil {
		return false
	}
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
